package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/acdiost/openhpc-web/internal/platform"
	"github.com/acdiost/openhpc-web/internal/terminal"
	"github.com/labstack/echo/v4"
	"golang.org/x/net/websocket"
)

const (
	maxTerminalPrivateKeyBytes = 8 << 10
	maxTerminalPassphraseBytes = 1024
	maxTerminalInputBytes      = 4 << 10
	terminalSessionLifetime    = 30 * time.Minute
	maxTerminalSessions        = 8
	maxTerminalSessionsPerUser = 2
)

var errTerminalSessionLimit = errors.New("terminal session limit reached")

type terminalView struct {
	appChrome
	Available bool
}

type terminalSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*managedTerminalSession
}

type managedTerminalSession struct {
	owner   string
	session terminal.Session
	claimed bool
	timer   *time.Timer
	close   sync.Once
	err     error
}

func newTerminalSessionStore() *terminalSessionStore {
	return &terminalSessionStore{sessions: map[string]*managedTerminalSession{}}
}

func (a *application) terminalPage(c echo.Context) error {
	lang := language(c)
	module := moduleByPath("/terminal", lang)
	view := terminalView{
		appChrome: a.newAppChrome(c, module.Path, a.terminalClient != nil, pageHeading{
			Eyebrow: "OPENHPC / " + module.Group, Title: module.Label,
			Description: map[bool]string{true: "Connect to the configured login node with your SSH private key", false: "使用 SSH 私钥连接到已配置的登录节点"}[lang == "en"],
		}),
		Available: a.terminalClient != nil,
	}
	return a.render(c, http.StatusOK, "terminal.html", view)
}

func (a *application) createTerminalSession(c echo.Context) error {
	if a.terminalClient == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	privateKey, passphrase, err := terminalCredentials(c.Request())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	defer wipeTerminalSecret(privateKey)
	defer wipeTerminalSecret(passphrase)
	identity := currentPrincipal(c)
	session, openErr := a.terminalClient.Open(c.Request().Context(), terminal.Request{
		Username: identity.Username, PrivateKey: privateKey, Passphrase: passphrase, Rows: 24, Columns: 100,
	})
	if openErr != nil {
		a.recordTerminalAudit(identity.Username, "denied")
		return echo.NewHTTPError(http.StatusBadGateway)
	}
	id, storeErr := a.terminalSessions.Add(identity.Username, session)
	if storeErr != nil {
		_ = session.Close()
		a.recordTerminalAudit(identity.Username, "rate_limited")
		return echo.NewHTTPError(http.StatusTooManyRequests)
	}
	if err := a.recordTerminalAudit(identity.Username, "success"); err != nil {
		a.terminalSessions.Release(id)
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	return c.JSON(http.StatusCreated, map[string]string{"session_id": id})
}

func (a *application) terminalSocket(c echo.Context) error {
	if err := sameOriginWebSocketHandshake(&websocket.Config{}, c.Request()); err != nil {
		return echo.NewHTTPError(http.StatusForbidden)
	}
	identity := currentPrincipal(c)
	id := c.Param("id")
	server := websocket.Server{
		Handler: websocket.Handler(func(connection *websocket.Conn) {
			managed, ok := a.terminalSessions.Claim(id, identity.Username)
			if !ok {
				_ = connection.Close()
				return
			}
			a.streamTerminal(id, identity.Username, managed, connection)
		}),
		Handshake: sameOriginWebSocketHandshake,
	}
	server.ServeHTTP(c.Response(), c.Request())
	return nil
}

func terminalCredentials(request *http.Request) ([]byte, []byte, error) {
	reader, err := request.MultipartReader()
	if err != nil {
		return nil, nil, err
	}
	var privateKey, passphrase []byte
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		name := part.FormName()
		switch name {
		case "private_key":
			if part.FileName() == "" || privateKey != nil {
				return nil, nil, errors.New("invalid SSH private key field")
			}
			privateKey, err = readTerminalPart(part, maxTerminalPrivateKeyBytes)
		case "passphrase":
			if passphrase != nil {
				return nil, nil, errors.New("duplicate SSH passphrase")
			}
			passphrase, err = readTerminalPart(part, maxTerminalPassphraseBytes)
		case "_csrf":
			_, err = io.Copy(io.Discard, io.LimitReader(part, 512))
		default:
			return nil, nil, errors.New("unexpected terminal form field")
		}
		if err != nil {
			return nil, nil, err
		}
	}
	if len(privateKey) == 0 {
		return nil, nil, errors.New("SSH private key is required")
	}
	return privateKey, passphrase, nil
}

func readTerminalPart(part io.Reader, limit int64) ([]byte, error) {
	value, err := io.ReadAll(io.LimitReader(part, limit+1))
	if err != nil || len(value) > int(limit) {
		return nil, errors.New("terminal form field is too large")
	}
	return value, nil
}

func (a *application) streamTerminal(id, username string, managed *managedTerminalSession, connection *websocket.Conn) {
	defer a.terminalSessions.Release(id)
	connection.MaxPayloadBytes = maxTerminalInputBytes
	_ = connection.SetDeadline(time.Now().Add(terminalSessionLifetime))
	session := managed.session
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buffer := make([]byte, maxTerminalInputBytes)
		for {
			count, err := session.Output().Read(buffer)
			if count > 0 {
				if sendErr := websocket.Message.Send(connection, string(buffer[:count])); sendErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		var input string
		if err := websocket.Message.Receive(connection, &input); err != nil || len(input) == 0 || len(input) > maxTerminalInputBytes {
			break
		}
		if _, err := io.WriteString(session.Input(), input); err != nil {
			break
		}
	}
	_ = connection.Close()
	_ = managed.Close()
	select {
	case <-outputDone:
	case <-time.After(time.Second):
	}
	a.recordTerminalAudit(username, "closed")
}

func sameOriginWebSocketHandshake(_ *websocket.Config, request *http.Request) error {
	origin, err := url.Parse(request.Header.Get("Origin"))
	if err != nil || origin.Host == "" || !strings.EqualFold(origin.Host, request.Host) {
		return errors.New("WebSocket origin is not allowed")
	}
	return nil
}

func (s *terminalSessionStore) Add(owner string, session terminal.Session) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) >= maxTerminalSessions {
		return "", errTerminalSessionLimit
	}
	userSessions := 0
	for _, managed := range s.sessions {
		if managed.owner == owner {
			userSessions++
		}
	}
	if userSessions >= maxTerminalSessionsPerUser {
		return "", errTerminalSessionLimit
	}
	id, err := randomToken()
	if err != nil {
		return "", err
	}
	managed := &managedTerminalSession{owner: owner, session: session}
	managed.timer = time.AfterFunc(terminalSessionLifetime, func() { s.Release(id) })
	s.sessions[id] = managed
	return id, nil
}

func (s *terminalSessionStore) Claim(id, owner string) (*managedTerminalSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	managed, found := s.sessions[id]
	if !found || managed.claimed || managed.owner != owner {
		return nil, false
	}
	managed.claimed = true
	return managed, true
}

func (s *terminalSessionStore) Release(id string) {
	s.mu.Lock()
	managed, found := s.sessions[id]
	if found {
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	if found {
		managed.timer.Stop()
		_ = managed.Close()
	}
}

func (s *terminalSessionStore) CloseUsername(username string) {
	s.mu.Lock()
	closing := make([]*managedTerminalSession, 0)
	for id, managed := range s.sessions {
		if managed.owner == username {
			delete(s.sessions, id)
			closing = append(closing, managed)
		}
	}
	s.mu.Unlock()
	for _, managed := range closing {
		managed.timer.Stop()
		_ = managed.Close()
	}
}

func (s *terminalSessionStore) Close() error {
	s.mu.Lock()
	closing := make([]*managedTerminalSession, 0, len(s.sessions))
	for id, managed := range s.sessions {
		delete(s.sessions, id)
		closing = append(closing, managed)
	}
	s.mu.Unlock()
	var err error
	for _, managed := range closing {
		managed.timer.Stop()
		err = errors.Join(err, managed.Close())
	}
	return err
}

func (s *managedTerminalSession) Close() error {
	s.close.Do(func() { s.err = s.session.Close() })
	return s.err
}

func (a *application) recordTerminalAudit(username, outcome string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return a.audit.Record(ctx, platform.AuditEvent{Actor: username, Action: "terminal.session", Outcome: outcome, CreatedAt: time.Now()})
}

func wipeTerminalSecret(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
