package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/acdiost/openhpc-web/internal/terminal"
)

func TestTerminalPageCreatesKeyAuthenticatedSession(t *testing.T) {
	client := &stubTerminalClient{session: &stubTerminalSession{}}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, TerminalClient: client})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	page := getAuthenticated(t, handler, "/terminal", "zh")
	assertStatus(t, page, http.StatusOK)
	for _, expected := range []string{`data-terminal-page`, `name="username" value="admin"`, `name="private_key"`, `name="passphrase"`, `data-terminal-connect`} {
		assertBodyContains(t, page, expected)
	}

	session, csrf := loginWithCSRF(t, handler)
	response := postTerminalSession(t, handler, session, csrf, "cluster-user", []byte("private key"), "passphrase")
	assertStatus(t, response, http.StatusCreated)
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.SessionID == "" {
		t.Fatalf("terminal session response = %q, error = %v", response.Body.String(), err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("terminal requests = %d, want 1", len(client.requests))
	}
	request := client.requests[0]
	if request.Username != "cluster-user" || string(request.PrivateKey) != "private key" || string(request.Passphrase) != "passphrase" {
		t.Fatalf("terminal request = %#v", request)
	}
}

func TestTerminalSessionRejectsMissingKeyBeforeSSH(t *testing.T) {
	client := &stubTerminalClient{session: &stubTerminalSession{}}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, TerminalClient: client})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	session, csrf := loginWithCSRF(t, handler)
	response := postTerminalSession(t, handler, session, csrf, testUsername, nil, "")
	assertStatus(t, response, http.StatusBadRequest)
	assertBodyContains(t, response, "SSH credentials are invalid")
	if len(client.requests) != 0 {
		t.Fatalf("terminal requests = %d, want none", len(client.requests))
	}
}

func TestTerminalSessionReturnsSSHFailureDetail(t *testing.T) {
	client := &stubTerminalClient{err: errors.New("SSH authentication failed; verify the username and that this public key is authorized")}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, TerminalClient: client})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	session, csrf := loginWithCSRF(t, handler)
	response := postTerminalSession(t, handler, session, csrf, testUsername, []byte("private key"), "")
	assertStatus(t, response, http.StatusBadGateway)
	assertBodyContains(t, response, "SSH authentication failed")
	assertBodyContains(t, response, "public key is authorized")
}

func TestTerminalSessionStoreBindsAndConsumesSessionIDs(t *testing.T) {
	store := newTerminalSessionStore()
	session := &stubTerminalSession{}
	id, err := store.Add("alice", session)
	if err != nil || id == "" {
		t.Fatalf("Add() = %q, %v", id, err)
	}
	if _, ok := store.Claim(id, "bob"); ok {
		t.Fatal("different user claimed terminal session")
	}
	if _, ok := store.Claim(id, "alice"); !ok {
		t.Fatal("owner could not claim terminal session")
	}
	if _, ok := store.Claim(id, "alice"); ok {
		t.Fatal("terminal session ID was claimed twice")
	}
	store.Release(id)
	if session.closeCalls != 1 {
		t.Fatalf("session close calls = %d, want 1", session.closeCalls)
	}
}

func TestTerminalSessionStoreLimitsSessionsPerUser(t *testing.T) {
	store := newTerminalSessionStore()
	for range 2 {
		if _, err := store.Add("alice", &stubTerminalSession{}); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}
	if _, err := store.Add("alice", &stubTerminalSession{}); !errors.Is(err, errTerminalSessionLimit) {
		t.Fatalf("Add() error = %v, want terminal session limit", err)
	}
	if _, err := store.Add("bob", &stubTerminalSession{}); err != nil {
		t.Fatalf("different user Add() error = %v", err)
	}
}

func TestTerminalWebSocketHandshakeRequiresSameOrigin(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://console.example/terminal/sessions/id/socket", nil)
	request.Host = "console.example"
	request.Header.Set("Origin", "http://console.example")
	if err := sameOriginWebSocketHandshake(nil, request); err != nil {
		t.Fatalf("sameOriginWebSocketHandshake() error = %v", err)
	}
	request.Header.Set("Origin", "https://other.example")
	if err := sameOriginWebSocketHandshake(nil, request); err == nil {
		t.Fatal("sameOriginWebSocketHandshake() error = nil for cross-origin request")
	}
}

func TestTerminalClientMessagesWriteInputAndResizePTY(t *testing.T) {
	session := &stubTerminalSession{}
	if !handleTerminalClientMessage(session, terminalClientMessage{Type: "input", Data: "ls\r"}) {
		t.Fatal("input message closed terminal")
	}
	if got := session.input.String(); got != "ls\r" {
		t.Fatalf("terminal input = %q, want %q", got, "ls\r")
	}
	if !handleTerminalClientMessage(session, terminalClientMessage{Type: "resize", Rows: 36, Columns: 128}) {
		t.Fatal("resize message closed terminal")
	}
	if session.rows != 36 || session.columns != 128 {
		t.Fatalf("terminal size = %dx%d, want 128x36", session.columns, session.rows)
	}
	if handleTerminalClientMessage(session, terminalClientMessage{Type: "unknown"}) {
		t.Fatal("unknown message kept terminal open")
	}
}

func postTerminalSession(t *testing.T, handler http.Handler, session, csrf *http.Cookie, username string, privateKey []byte, passphrase string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("_csrf", csrf.Value); err != nil {
		t.Fatal(err)
	}
	if username != "" {
		if err := writer.WriteField("username", username); err != nil {
			t.Fatal(err)
		}
	}
	if passphrase != "" {
		if err := writer.WriteField("passphrase", passphrase); err != nil {
			t.Fatal(err)
		}
	}
	if len(privateKey) > 0 {
		file, err := writer.CreateFormFile("private_key", "id_ed25519")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(privateKey); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/terminal/sessions", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", csrf.Value)
	request.AddCookie(session)
	request.AddCookie(csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type stubTerminalClient struct {
	requests []terminal.Request
	session  terminal.Session
	err      error
}

func (c *stubTerminalClient) Open(_ context.Context, request terminal.Request) (terminal.Session, error) {
	c.requests = append(c.requests, terminal.Request{
		Username: request.Username, PrivateKey: append([]byte(nil), request.PrivateKey...), Passphrase: append([]byte(nil), request.Passphrase...), Rows: request.Rows, Columns: request.Columns,
	})
	return c.session, c.err
}

type stubTerminalSession struct {
	input         bytes.Buffer
	rows, columns int
	closeCalls    int
}

func (s *stubTerminalSession) Input() io.WriteCloser { return nopTerminalInput{Writer: &s.input} }

func (s *stubTerminalSession) Output() io.Reader { return bytes.NewReader(nil) }

func (s *stubTerminalSession) Resize(rows, columns int) error {
	s.rows, s.columns = rows, columns
	return nil
}

func (s *stubTerminalSession) Close() error {
	s.closeCalls++
	return nil
}

type nopTerminalInput struct{ io.Writer }

func (nopTerminalInput) Close() error { return nil }
