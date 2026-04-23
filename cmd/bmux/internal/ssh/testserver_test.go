package ssh

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"net"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

// testSSHServer is a minimal in-process SSH server for unit tests.
type testSSHServer struct {
	listener net.Listener
	cfg      *gossh.ServerConfig
	signer   gossh.Signer // ephemeral host key
	done     chan struct{}

	// rejectAuth causes all authentication attempts to fail.
	rejectAuth bool

	// sessionHandler replaces the default handler for accepted channels.
	sessionHandler func(ch gossh.Channel, reqs <-chan *gossh.Request)
}

// newTestServer starts an in-process SSH server on a random port.
// The server stops automatically via t.Cleanup.
func newTestServer(t *testing.T, opts ...func(*testSSHServer)) *testSSHServer {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("signer from key: %v", err)
	}

	srv := &testSSHServer{done: make(chan struct{}), signer: signer}
	for _, opt := range opts {
		opt(srv)
	}

	srv.cfg = &gossh.ServerConfig{
		NoClientAuth: false,
		PasswordCallback: func(c gossh.ConnMetadata, pass []byte) (*gossh.Permissions, error) {
			if srv.rejectAuth {
				return nil, gossh.ErrNoAuth
			}
			return &gossh.Permissions{}, nil
		},
		PublicKeyCallback: func(conn gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if srv.rejectAuth {
				return nil, gossh.ErrNoAuth
			}
			return &gossh.Permissions{}, nil
		},
	}
	srv.cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv.listener = ln

	go srv.serve()
	t.Cleanup(func() {
		close(srv.done)
		_ = ln.Close()
	})
	return srv
}

func (s *testSSHServer) addr() string { return s.listener.Addr().String() }

func (s *testSSHServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
			default:
			}
			return
		}
		go s.handleConn(conn)
	}
}

func (s *testSSHServer) handleConn(conn net.Conn) {
	sshConn, chans, reqs, err := gossh.NewServerConn(conn, s.cfg)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go gossh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(gossh.UnknownChannelType, "unknown channel type")
			continue
		}
		ch, reqsCh, err := newChan.Accept()
		if err != nil {
			continue
		}
		handler := s.sessionHandler
		if handler == nil {
			handler = defaultSessionHandler
		}
		go handler(ch, reqsCh)
	}
}

// defaultSessionHandler simulates a minimal tmux control mode: accepts pty +
// exec/shell, emits %exit, then closes.
func defaultSessionHandler(ch gossh.Channel, reqs <-chan *gossh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		case "exec", "shell":
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
			_, _ = io.WriteString(ch, "%begin 0 0 0\n%end 0 0 0\n%exit\n")
			return
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// acceptHostKeyDialer dials the test server and accepts any host key.
type acceptHostKeyDialer struct{}

func (d *acceptHostKeyDialer) Dial(ctx context.Context, network, addr string, cfg *gossh.ClientConfig) (*gossh.Client, error) {
	nd := &net.Dialer{}
	conn, err := nd.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	c2 := *cfg
	c2.HostKeyCallback = gossh.InsecureIgnoreHostKey() //nolint:gosec
	c, chans, reqs, err := gossh.NewClientConn(conn, addr, &c2)
	if err != nil {
		return nil, err
	}
	return gossh.NewClient(c, chans, reqs), nil
}

// wrongHostKeyDialer dials but presents a key the server doesn't match.
type wrongHostKeyDialer struct{}

func (d *wrongHostKeyDialer) Dial(ctx context.Context, network, addr string, cfg *gossh.ClientConfig) (*gossh.Client, error) {
	nd := &net.Dialer{}
	conn, err := nd.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	wrongKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	wrongSigner, _ := gossh.NewSignerFromKey(wrongKey)
	c2 := *cfg
	c2.HostKeyCallback = gossh.FixedHostKey(wrongSigner.PublicKey())
	_, _, _, err = gossh.NewClientConn(conn, addr, &c2)
	return nil, err
}
