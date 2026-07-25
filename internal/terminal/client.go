package terminal

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	defaultTimeout   = 10 * time.Second
	maxPrivateKeyLen = 32 << 10
	maxPassphraseLen = 1024
)

type Config struct {
	Address string
	Timeout time.Duration
}

type Request struct {
	Username   string
	PrivateKey []byte
	Passphrase []byte
	Rows       int
	Columns    int
}

type Client interface {
	Open(context.Context, Request) (Session, error)
}

type Session interface {
	Input() io.WriteCloser
	Output() io.Reader
	Resize(rows, columns int) error
	Close() error
}

type sshClient struct {
	address         string
	timeout         time.Duration
	hostKeyCallback ssh.HostKeyCallback
}

type sshSession struct {
	client  *ssh.Client
	session *ssh.Session
	input   io.WriteCloser
	output  io.Reader
}

func New(config Config) (Client, error) {
	if err := validateAddress(config.Address); err != nil {
		return nil, err
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout < time.Second || timeout > time.Minute {
		return nil, errors.New("SSH terminal timeout must be between one second and one minute")
	}
	return &sshClient{address: config.Address, timeout: timeout, hostKeyCallback: ssh.InsecureIgnoreHostKey()}, nil
}

func (c *sshClient) Open(ctx context.Context, request Request) (Session, error) {
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	signer, err := privateKeySigner(request.PrivateKey, request.Passphrase)
	if err != nil {
		return nil, errors.New("SSH private key could not be used; verify the key format and passphrase")
	}
	dialer := net.Dialer{Timeout: c.timeout}
	connection, err := dialer.DialContext(ctx, "tcp", c.address)
	if err != nil {
		return nil, errors.New("SSH login node is unreachable; verify its address, firewall, and SSH service")
	}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, c.address, &ssh.ClientConfig{
		User:            request.Username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: c.hostKeyCallback,
		Timeout:         c.timeout,
	})
	if err != nil {
		_ = connection.Close()
		return nil, errors.New("SSH authentication failed; verify the username and that this public key is authorized")
	}
	client := ssh.NewClient(clientConnection, channels, requests)
	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, errors.New("SSH terminal session could not be created")
	}
	input, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, errors.New("SSH terminal input could not be created")
	}
	output, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, errors.New("SSH terminal output could not be created")
	}
	if err := session.RequestPty("xterm-256color", request.Rows, request.Columns, ssh.TerminalModes{}); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, errors.New("SSH terminal PTY request failed")
	}
	if err := session.Shell(); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, errors.New("SSH terminal shell could not be started")
	}
	return &sshSession{client: client, session: session, input: input, output: output}, nil
}

func (s *sshSession) Input() io.WriteCloser { return s.input }

func (s *sshSession) Output() io.Reader { return s.output }

func (s *sshSession) Resize(rows, columns int) error {
	if rows < 1 || rows > 200 || columns < 1 || columns > 500 {
		return errors.New("terminal dimensions are invalid")
	}
	return s.session.WindowChange(rows, columns)
}

func (s *sshSession) Close() error {
	return errors.Join(s.session.Close(), s.client.Close())
}

func validateAddress(address string) error {
	if address == "" || strings.TrimSpace(address) != address || len(address) > 320 || strings.ContainsRune(address, '\x00') {
		return errors.New("SSH login node address is invalid")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || strings.ContainsAny(host, " \t\r\n") {
		return errors.New("SSH login node address must be host:port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("SSH login node port is invalid")
	}
	return nil
}

func ValidateAddress(address string) error { return validateAddress(address) }

func validateRequest(request Request) error {
	if !validUsername(request.Username) || len(request.PrivateKey) == 0 || len(request.PrivateKey) > maxPrivateKeyLen || len(request.Passphrase) > maxPassphraseLen {
		return errors.New("SSH terminal request is invalid")
	}
	if request.Rows < 1 || request.Rows > 200 || request.Columns < 1 || request.Columns > 500 {
		return errors.New("terminal dimensions are invalid")
	}
	return nil
}

func privateKeySigner(privateKey, passphrase []byte) (ssh.Signer, error) {
	if len(passphrase) == 0 {
		return ssh.ParsePrivateKey(privateKey)
	}
	return ssh.ParsePrivateKeyWithPassphrase(privateKey, passphrase)
}

func validUsername(username string) bool {
	if username == "" || len(username) > 64 || username == "." || username == ".." {
		return false
	}
	for _, character := range username {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
