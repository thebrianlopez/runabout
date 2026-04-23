package ssh

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blo-grindr/bmux/internal/config"
)

const testTimeout = 5 * time.Second

// hostFromAddr builds a config.HostConfig pointing at addr.
func hostFromAddr(t *testing.T, addr string) config.HostConfig {
	t.Helper()
	h, portStr, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return config.HostConfig{
		Name:    "testhost",
		SSHHost: h,
		SSHUser: "testuser",
		SSHPort: port,
	}
}

// blockingSessionHandler handles PTY + exec but blocks indefinitely so the
// session appears "connected" for the duration of the test.
func blockingSessionHandler(ch gossh.Channel, reqs <-chan *gossh.Request) {
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
			// Block until the channel is torn down.
			io.Copy(io.Discard, ch) //nolint:errcheck
			return
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// rejectExecHandler accepts PTY but rejects exec (simulates missing tmux).
func rejectExecHandler(ch gossh.Channel, reqs <-chan *gossh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		case "exec", "shell":
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			return
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// --- Contract Tests ---

// CT-2: ssh_host_unreachable on TCP failure.
func TestConnect_HostUnreachable(t *testing.T) {
	host := config.HostConfig{
		Name:    "unreachable",
		SSHHost: "127.0.0.1",
		SSHUser: "user",
		SSHPort: 1, // port 1 is reserved and unreachable
	}
	m := newManagerWithDialer(&acceptHostKeyDialer{})
	_, err := m.Connect(context.Background(), host)
	require.Error(t, err)
	var se *SSHError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "ssh_host_unreachable", se.Code)
}

// CT-3: ssh_auth_failed when server rejects all credentials.
func TestConnect_AuthFailed(t *testing.T) {
	srv := newTestServer(t, func(s *testSSHServer) { s.rejectAuth = true })
	host := hostFromAddr(t, srv.addr())

	m := newManagerWithDialer(&acceptHostKeyDialer{})
	_, err := m.Connect(context.Background(), host)
	require.Error(t, err)
	var se *SSHError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "ssh_auth_failed", se.Code)
}

// CT-4: ssh_host_key_mismatch when known_hosts conflicts with server key.
func TestConnect_HostKeyMismatch(t *testing.T) {
	srv := newTestServer(t)
	host := hostFromAddr(t, srv.addr())

	m := newManagerWithDialer(&wrongHostKeyDialer{})
	_, err := m.Connect(context.Background(), host)
	require.Error(t, err)
	var se *SSHError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "ssh_host_key_mismatch", se.Code)
}

// CT-5: control_mode_rejected when remote rejects exec (simulates no/old tmux).
func TestConnect_ControlModeRejected(t *testing.T) {
	srv := newTestServer(t, func(s *testSSHServer) {
		s.sessionHandler = rejectExecHandler
	})
	host := hostFromAddr(t, srv.addr())

	m := newManagerWithDialer(&acceptHostKeyDialer{})
	_, err := m.Connect(context.Background(), host)
	require.Error(t, err)
	var se *SSHError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "control_mode_rejected", se.Code)
}

// CT-7: Session.Disconnected() closes when SSH session EOF.
func TestSession_DisconnectedOnEOF(t *testing.T) {
	// Default server: sends %exit and closes immediately.
	srv := newTestServer(t)
	host := hostFromAddr(t, srv.addr())

	m := newManagerWithDialer(&acceptHostKeyDialer{})
	sess, err := m.Connect(context.Background(), host)
	require.NoError(t, err)

	select {
	case <-sess.Disconnected():
		// Session disconnected as expected.
	case <-time.After(testTimeout):
		t.Fatal("session.Disconnected() did not close within timeout")
	}
}

// CT-10: SSHManager.Sessions() returns all connected sessions, all Connected.
func TestManager_SessionsReturnsAll(t *testing.T) {
	srv1 := newTestServer(t, func(s *testSSHServer) { s.sessionHandler = blockingSessionHandler })
	srv2 := newTestServer(t, func(s *testSSHServer) { s.sessionHandler = blockingSessionHandler })

	host1 := hostFromAddr(t, srv1.addr())
	host1.Name = "host1"
	host2 := hostFromAddr(t, srv2.addr())
	host2.Name = "host2"

	m := newManagerWithDialer(&acceptHostKeyDialer{})
	_, err := m.Connect(context.Background(), host1)
	require.NoError(t, err)
	_, err = m.Connect(context.Background(), host2)
	require.NoError(t, err)

	sessions := m.Sessions()
	require.Len(t, sessions, 2)
	for _, s := range sessions {
		assert.Equal(t, StatusConnected, s.Status())
	}
}

// --- Regression Guard ---

// RG-1: SendInput on a disconnected session returns ErrSessionClosed, no panic.
func TestSession_SendInputOnClosed(t *testing.T) {
	srv := newTestServer(t) // sends %exit → disconnects immediately
	host := hostFromAddr(t, srv.addr())

	m := newManagerWithDialer(&acceptHostKeyDialer{})
	sess, err := m.Connect(context.Background(), host)
	require.NoError(t, err)

	select {
	case <-sess.Disconnected():
	case <-time.After(testTimeout):
		t.Fatal("timeout waiting for session disconnect")
	}

	err = sess.SendInput([]byte("hello"))
	assert.ErrorIs(t, err, ErrSessionClosed)
}

// --- Behavioral Tests ---

// BT-4: SSHManager.Disconnect removes session from Sessions().
func TestManager_Disconnect(t *testing.T) {
	srv := newTestServer(t, func(s *testSSHServer) { s.sessionHandler = blockingSessionHandler })
	host := hostFromAddr(t, srv.addr())

	m := newManagerWithDialer(&acceptHostKeyDialer{})
	_, err := m.Connect(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, m.Sessions(), 1)

	require.NoError(t, m.Disconnect("testhost"))

	// Session removal from the map is triggered by Disconnected() closing,
	// which happens asynchronously. Poll briefly.
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if len(m.Sessions()) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("session not removed from manager after Disconnect")
}

// BT-5: %exit event via control mode parser closes the session.
func TestSession_ExitEventDisconnects(t *testing.T) {
	// Handler that sends the %exit control mode event.
	srv := newTestServer(t, func(s *testSSHServer) {
		s.sessionHandler = defaultSessionHandler // sends %exit
	})
	host := hostFromAddr(t, srv.addr())

	m := newManagerWithDialer(&acceptHostKeyDialer{})
	sess, err := m.Connect(context.Background(), host)
	require.NoError(t, err)

	select {
	case <-sess.Disconnected():
		assert.Equal(t, StatusDisconnected, sess.Status())
	case <-time.After(testTimeout):
		t.Fatal("session did not close after exit event")
	}
}
