package terminal

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestNewValidatesSSHConfiguration(t *testing.T) {
	if _, err := New(Config{Address: "login.example:22"}); err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for _, config := range []Config{
		{Address: "login.example"},
		{Address: "login.example:22", Timeout: 500 * time.Millisecond},
		{Address: "login.example:22", Timeout: 2 * time.Minute},
	} {
		if _, err := New(config); err == nil {
			t.Errorf("New(%#v) error = nil, want rejection", config)
		}
	}
}

func TestOpenStartsKeyAuthenticatedShell(t *testing.T) {
	userPublic, userPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	userKey, err := ssh.NewPublicKey(userPublic)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(userPrivate)
	if err != nil {
		t.Fatal(err)
	}
	encodedPrivateKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey})
	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	address := listener.Addr().String()
	received := make(chan string, 1)
	serverDone := make(chan error, 1)
	go runTerminalTestServer(listener, hostSigner, userKey, received, serverDone)

	client, err := New(Config{Address: address, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.Open(t.Context(), Request{Username: "alice", PrivateKey: encodedPrivateKey, Rows: 24, Columns: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	output := make([]byte, len("ready> "))
	if _, err := io.ReadFull(session.Output(), output); err != nil {
		t.Fatal(err)
	}
	if string(output) != "ready> " {
		t.Fatalf("terminal output = %q", output)
	}
	if _, err := io.WriteString(session.Input(), "echo hello\n"); err != nil {
		t.Fatal(err)
	}
	if err := session.Resize(30, 120); err != nil {
		t.Fatal(err)
	}
	if err := session.Resize(0, 120); err == nil {
		t.Fatal("Resize() error = nil for invalid dimensions")
	}
	select {
	case command := <-received:
		if command != "echo hello\n" {
			t.Fatalf("terminal input = %q", command)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH server did not receive terminal input")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestTerminalRequestValidationAndPrivateKeyParsing(t *testing.T) {
	for _, request := range []Request{
		{Username: "", PrivateKey: []byte("key"), Rows: 24, Columns: 80},
		{Username: "alice", PrivateKey: nil, Rows: 24, Columns: 80},
		{Username: "alice", PrivateKey: bytes.Repeat([]byte("a"), maxPrivateKeyLen+1), Rows: 24, Columns: 80},
		{Username: "alice", PrivateKey: []byte("key"), Rows: 0, Columns: 80},
	} {
		if err := validateRequest(request); err == nil {
			t.Errorf("validateRequest(%#v) error = nil", request)
		}
	}
	if _, err := privateKeySigner([]byte("not a private key"), nil); err == nil {
		t.Fatal("privateKeySigner() error = nil for invalid key")
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := ssh.MarshalPrivateKeyWithPassphrase(privateKey, "terminal", []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := privateKeySigner(pem.EncodeToMemory(encrypted), []byte("passphrase")); err != nil {
		t.Fatalf("privateKeySigner() error = %v", err)
	}
	if err := ValidateAddress("login.example:22"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsUnavailableLoginNode(t *testing.T) {
	privateKey := testPrivateKey(t)
	client := &sshClient{address: "127.0.0.1:1", timeout: 100 * time.Millisecond, hostKeyCallback: ssh.InsecureIgnoreHostKey()}
	if _, err := client.Open(t.Context(), Request{Username: "alice", PrivateKey: privateKey, Rows: 24, Columns: 80}); err == nil {
		t.Fatal("Open() error = nil for unavailable login node")
	}
}

func TestOpenClosesClientWhenSessionChannelIsRejected(t *testing.T) {
	hostSigner := testSSHSigner(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverDone := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		serverConfig := &ssh.ServerConfig{NoClientAuth: true}
		serverConfig.AddHostKey(hostSigner)
		_, channels, requests, err := ssh.NewServerConn(connection, serverConfig)
		if err != nil {
			serverDone <- err
			return
		}
		go ssh.DiscardRequests(requests)
		channel := <-channels
		serverDone <- channel.Reject(ssh.Prohibited, "terminal unavailable")
	}()
	client, err := New(Config{Address: listener.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Open(t.Context(), Request{Username: "alice", PrivateKey: testPrivateKey(t), Rows: 24, Columns: 80}); err == nil {
		t.Fatal("Open() error = nil when SSH session channel is rejected")
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH session rejection server did not finish")
	}
}

func testPrivateKey(t *testing.T) []byte {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
}

func testSSHSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func runTerminalTestServer(listener net.Listener, hostSigner ssh.Signer, userKey ssh.PublicKey, received chan<- string, done chan<- error) {
	connection, err := listener.Accept()
	if err != nil {
		done <- err
		return
	}
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if reflect.DeepEqual(key.Marshal(), userKey.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("unexpected user key")
	}}
	serverConfig.AddHostKey(hostSigner)
	_, channels, requests, err := ssh.NewServerConn(connection, serverConfig)
	if err != nil {
		done <- err
		return
	}
	go ssh.DiscardRequests(requests)
	channelRequest := <-channels
	if channelRequest.ChannelType() != "session" {
		done <- errors.New("unexpected SSH channel")
		return
	}
	channel, requests, err := channelRequest.Accept()
	if err != nil {
		done <- err
		return
	}
	for request := range requests {
		switch request.Type {
		case "pty-req":
			if err := request.Reply(true, nil); err != nil {
				done <- err
				return
			}
		case "shell":
			if err := request.Reply(true, nil); err != nil {
				done <- err
				return
			}
			if _, err := channel.Write([]byte("ready> ")); err != nil {
				done <- err
				return
			}
			buffer := make([]byte, 64)
			count, err := channel.Read(buffer)
			if err != nil {
				done <- err
				return
			}
			received <- string(buffer[:count])
			_ = channel.Close()
			done <- nil
			return
		}
	}
	done <- nil
}
