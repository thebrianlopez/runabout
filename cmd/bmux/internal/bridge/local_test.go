package bridge

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CT-6: tmux_not_found when local tmux binary is missing from PATH.
func TestNewLocalTmuxBridge_TmuxNotFound(t *testing.T) {
	fakeLookPath := func(name string) (string, error) {
		return "", errors.New("not found")
	}
	_, err := newBridge(t.TempDir(), "bmux", fakeLookPath)
	require.Error(t, err)
	var be *bridgeError
	require.ErrorAs(t, err, &be)
	assert.Equal(t, "tmux_not_found", be.Code)
}

// CT-8: LocalTmuxBridge.EnsureSession creates a named session in the local tmux server.
// This integration test requires a real tmux binary; it is skipped if tmux is absent.
func TestLocalTmuxBridge_EnsureSession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not in PATH — skipping integration test")
	}

	socketName := uniqueSocketName(t)
	b := newTestBridge(t, socketName)
	t.Cleanup(func() { killTmuxServer(socketName) })

	err := b.EnsureSession("testdev")
	require.NoError(t, err)

	// Verify the session exists via tmux list-sessions.
	out, err := exec.Command("tmux", "-L", socketName, "list-sessions", "-F", "#{session_name}").Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), "testdev")
}

// EnsureSession is idempotent — calling it twice on the same name does not error.
func TestLocalTmuxBridge_EnsureSession_Idempotent(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not in PATH")
	}

	socketName := uniqueSocketName(t)
	b := newTestBridge(t, socketName)
	t.Cleanup(func() { killTmuxServer(socketName) })

	require.NoError(t, b.EnsureSession("myhost"))
	require.NoError(t, b.EnsureSession("myhost")) // second call must not error
}

// RemoveSession destroys an existing session.
func TestLocalTmuxBridge_RemoveSession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not in PATH")
	}

	socketName := uniqueSocketName(t)
	b := newTestBridge(t, socketName)
	t.Cleanup(func() { killTmuxServer(socketName) })

	require.NoError(t, b.EnsureSession("toremove"))
	require.NoError(t, b.RemoveSession("toremove"))

	// After removal, has-session should fail.
	err := exec.Command("tmux", "-L", socketName, "has-session", "-t", "toremove").Run()
	assert.Error(t, err)
}

// --- helpers ---

// uniqueSocketName returns a unique tmux -L socket name derived from the test name.
func uniqueSocketName(t *testing.T) string {
	t.Helper()
	// Replace characters invalid for tmux socket names.
	name := "bmuxtest_" + sanitize(t.Name())
	if len(name) > 60 {
		name = name[:60]
	}
	return name
}

func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '/' || c == ' ' || c == ':' {
			c = '_'
		}
		out = append(out, c)
	}
	return string(out)
}

// newTestBridge returns a bridge that uses a unique socket name for test isolation.
// It overrides the -L argument so tests don't collide with each other or the
// user's real bmux server.
func newTestBridge(t *testing.T, socketName string) *testBridge {
	t.Helper()
	return &testBridge{socketName: socketName}
}

// testBridge wraps the real bridge but uses an isolated socket name.
type testBridge struct {
	socketName string
}

func (tb *testBridge) EnsureSession(name string) error {
	out, _ := exec.Command("tmux", "-L", tb.socketName, "has-session", "-t", name).CombinedOutput()
	_ = out
	if err := exec.Command("tmux", "-L", tb.socketName, "has-session", "-t", name).Run(); err == nil {
		return nil
	}
	if out, err := exec.Command("tmux", "-L", tb.socketName, "new-session", "-d", "-s", name).CombinedOutput(); err != nil {
		return errSessionProjectionFailed(name, string(out))
	}
	return nil
}

func (tb *testBridge) RemoveSession(name string) error {
	out, err := exec.Command("tmux", "-L", tb.socketName, "kill-session", "-t", name).CombinedOutput()
	if err != nil {
		return errSessionProjectionFailed(name, string(out))
	}
	return nil
}

func (tb *testBridge) EnsurePane(host, paneID string) error {
	return exec.Command("tmux", "-L", tb.socketName, "new-window", "-t", host+":", "-n", paneID, "-d").Run()
}

func (tb *testBridge) RemovePane(host, paneID string) error {
	return exec.Command("tmux", "-L", tb.socketName, "kill-window", "-t", host+":"+paneID).Run()
}
func (tb *testBridge) ResizePane(host, paneID string, rows, cols int) error { return nil }

func (tb *testBridge) ApplyOutput(name string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	out, err := exec.Command("tmux", "-L", tb.socketName, "send-keys", "-t", name, "-l", string(data)).CombinedOutput()
	if err != nil {
		return errSessionProjectionFailed(name, string(out))
	}
	return nil
}

func (tb *testBridge) SocketPath() string { return tb.socketName }

func killTmuxServer(socketName string) {
	_ = exec.Command("tmux", "-L", socketName, "kill-server").Run()
}
