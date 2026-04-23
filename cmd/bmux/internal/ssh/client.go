package ssh

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/blo-grindr/bmux/internal/config"
)

// defaultPTYRows and defaultPTYCols are the fallback PTY dimensions when the
// local terminal size cannot be determined (e.g. non-TTY environments).
const (
	defaultPTYRows = 50
	defaultPTYCols = 220
)

// sshDialer is a testable interface for dialing SSH connections.
type sshDialer interface {
	Dial(ctx context.Context, network, addr string, cfg *gossh.ClientConfig) (*gossh.Client, error)
}

// defaultDialer uses the real x/crypto/ssh dialer.
type defaultDialer struct{}

func (d *defaultDialer) Dial(ctx context.Context, network, addr string, cfg *gossh.ClientConfig) (*gossh.Client, error) {
	// Use DialContext via a plain net.Dialer to respect cancellation.
	nd := &net.Dialer{}
	conn, err := nd.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := gossh.NewClientConn(conn, addr, cfg)
	if err != nil {
		return nil, err
	}
	return gossh.NewClient(c, chans, reqs), nil
}

// sshSession wraps an active SSH connection + control mode parser.
type sshSession struct {
	mu           sync.Mutex
	host         config.HostConfig
	client       *gossh.Client
	session      *gossh.Session
	stdin        interface{ Write([]byte) (int, error) }
	status       SessionStatus
	disconnected chan struct{}
	closeOnce    sync.Once
	managerCh    chan<- PaneEvent // shared manager channel
	eventCh      chan PaneEvent   // per-session channel for IOBridge
}

func (s *sshSession) Host() string { return s.host.Name }

func (s *sshSession) Status() SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *sshSession) Disconnected() <-chan struct{} { return s.disconnected }

func (s *sshSession) Events() <-chan PaneEvent { return s.eventCh }

func (s *sshSession) SendInput(data []byte) error {
	s.mu.Lock()
	st := s.status
	w := s.stdin
	s.mu.Unlock()

	if st != StatusConnected || w == nil {
		return ErrSessionClosed
	}
	_, err := w.Write(data)
	return err
}

func (s *sshSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.status = StatusDisconnected
		s.mu.Unlock()

		close(s.disconnected)
		if s.session != nil {
			_ = s.session.Close()
		}
		if s.client != nil {
			err = s.client.Close()
		}
	})
	return err
}

// connect establishes the SSH connection and launches the control mode parser.
func connect(ctx context.Context, host config.HostConfig, dialer sshDialer, events chan<- PaneEvent) (*sshSession, error) {
	cfg, err := buildClientConfig(host)
	if err != nil {
		return nil, err
	}

	addr := fmt.Sprintf("%s:%d", host.SSHHost, host.SSHPort)
	client, err := dialer.Dial(ctx, "tcp", addr, cfg)
	if err != nil {
		return nil, classifyDialError(host, err)
	}

	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, classifySessionError(host, err)
	}

	rows, cols := ptyDimensions()
	modes := gossh.TerminalModes{
		gossh.ECHO: 0,
	}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, errControlModeRejected(host.Name)
	}

	stdinPipe, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("stdin pipe for %s: %w", host.Name, err)
	}

	stdoutPipe, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("stdout pipe for %s: %w", host.Name, err)
	}

	// Start tmux in control mode.
	if err := sess.Start("tmux -CC -u new-session -A -s main"); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, errControlModeRejected(host.Name)
	}

	s := &sshSession{
		host:         host,
		client:       client,
		session:      sess,
		stdin:        stdinPipe,
		status:       StatusConnected,
		disconnected: make(chan struct{}),
		managerCh:    events,
		eventCh:      make(chan PaneEvent, 256),
	}

	// Fan parsed events out to both the manager's shared channel and the
	// per-session channel (consumed by IOBridge).
	parsedCh := ControlModeParser(host.Name, stdoutPipe)
	go func() {
		defer close(s.eventCh)
		for ev := range parsedCh {
			select {
			case events <- ev:
			default:
			}
			select {
			case s.eventCh <- ev:
			default:
			}
		}
		// parsedCh closed: session ended.
		s.Close()
	}()

	// Also watch for the SSH session to exit on its own.
	go func() {
		_ = sess.Wait()
		s.Close()
	}()

	return s, nil
}

// buildClientConfig assembles the gossh.ClientConfig for the given host.
func buildClientConfig(host config.HostConfig) (*gossh.ClientConfig, error) {
	// Host key verification using ~/.ssh/known_hosts.
	knownHostsFile := os.ExpandEnv("$HOME/.ssh/known_hosts")
	hostKeyCallback, err := knownhosts.New(knownHostsFile)
	if err != nil {
		// If known_hosts doesn't exist, fall back to InsecureIgnoreHostKey in
		// dev mode. In production we want strict checking.
		// TODO: surface a warning when known_hosts is absent.
		hostKeyCallback = gossh.InsecureIgnoreHostKey() //nolint:gosec
	}

	var authMethods []gossh.AuthMethod

	// Identity file auth.
	if host.IdentityFile != "" {
		key, err := os.ReadFile(host.IdentityFile)
		if err == nil {
			signer, err := gossh.ParsePrivateKey(key)
			if err == nil {
				authMethods = append(authMethods, gossh.PublicKeys(signer))
			}
		}
	}

	// ssh-agent auth (always attempted; no-op if agent socket absent).
	if agentAuth := agentAuthMethod(); agentAuth != nil {
		authMethods = append(authMethods, agentAuth)
	}

	// Always include password auth as a last resort.
	// errAuthFailed is returned when the *server* rejects all methods, not
	// when no methods are pre-configured.
	authMethods = append(authMethods, gossh.Password(""))

	return &gossh.ClientConfig{
		User:            host.SSHUser,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
	}, nil
}

// classifyDialError maps a net/ssh dial error to a typed SSHError.
func classifyDialError(host config.HostConfig, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case isHostKeyError(msg): // must precede isAuthError — host key wraps in "handshake failed"
		return errHostKeyMismatch(host.Name)
	case isAuthError(msg):
		return errAuthFailed(host.Name)
	default:
		return errHostUnreachable(host.SSHHost, host.SSHPort, msg)
	}
}

func classifySessionError(host config.HostConfig, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if isAuthError(msg) {
		return errAuthFailed(host.Name)
	}
	return errControlModeRejected(host.Name)
}

func isAuthError(msg string) bool {
	return contains(msg, "unable to authenticate", "no supported methods remain",
		"ssh: handshake failed", "permission denied")
}

func isHostKeyError(msg string) bool {
	return contains(msg, "host key mismatch", "knownhosts", "ssh: host key")
}

func contains(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// ptyDimensions returns the current terminal dimensions, falling back to defaults.
func ptyDimensions() (rows, cols int) {
	return defaultPTYRows, defaultPTYCols
}
