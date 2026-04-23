// Package bridge manages the local tmux -L bmux server that projects remote
// sessions onto the local machine.
package bridge

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// LocalTmuxBridge manages the local tmux -L bmux server.
type LocalTmuxBridge interface {
	// EnsureSession creates a local session named after the host if absent.
	EnsureSession(name string) error

	// ApplyOutput writes raw bytes to the named local session's active pane.
	ApplyOutput(name string, data []byte) error

	// RemoveSession destroys the local session for a disconnected host.
	RemoveSession(name string) error

	// EnsurePane creates a local window named <paneID> in the host session.
	// Idempotent: no-op if the window already exists.
	EnsurePane(host, paneID string) error

	// RemovePane destroys the local window named <paneID> in the host session.
	// No-op if the window does not exist.
	RemovePane(host, paneID string) error

	// ResizePane resizes the local window <paneID> in the host session.
	ResizePane(host, paneID string, rows, cols int) error

	// SocketPath returns the absolute path to the tmux -L bmux socket.
	SocketPath() string
}

// bridge is the concrete LocalTmuxBridge implementation.
type bridge struct {
	socketDir  string
	socketName string
	lookPath   func(string) (string, error)
}

// NewLocalTmuxBridge creates a LocalTmuxBridge using the given socket directory
// and socket name (passed as -L to tmux; defaults to "bmux").
// Returns tmux_not_found if the tmux binary is not in PATH.
func NewLocalTmuxBridge(socketDir, socketName string) (LocalTmuxBridge, error) {
	return newBridge(socketDir, socketName, exec.LookPath)
}

// newBridge is the testable constructor that accepts a custom lookPath.
func newBridge(socketDir, socketName string, lookPath func(string) (string, error)) (LocalTmuxBridge, error) {
	if _, err := lookPath("tmux"); err != nil {
		return nil, errTmuxNotFound()
	}
	if socketName == "" {
		socketName = "bmux"
	}
	return &bridge{socketDir: socketDir, socketName: socketName, lookPath: lookPath}, nil
}

// SocketPath returns the OS path where tmux stores the named socket.
// On macOS: /private/tmp/tmux-<uid>/<socketName>
// On Linux: $XDG_RUNTIME_DIR/tmux-<uid>/<socketName>
func (b *bridge) SocketPath() string {
	uid := strconv.Itoa(os.Getuid())
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join("/private/tmp", "tmux-"+uid, b.socketName)
	default:
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		if runtimeDir == "" {
			runtimeDir = "/run/user/" + uid
		}
		return filepath.Join(runtimeDir, "tmux-"+uid, b.socketName)
	}
}

// EnsureSession creates a detached local session named <name> if it does not
// already exist. Idempotent: if the session already exists, returns nil.
func (b *bridge) EnsureSession(name string) error {
	// Check if session exists.
	out, _ := b.tmux("has-session", "-t", name)
	if out == nil {
		// Session already exists.
		return nil
	}
	// Create a new detached session.
	if _, err := b.tmuxErr("new-session", "-d", "-s", name); err != nil {
		return errSessionProjectionFailed(name, err.Error())
	}
	return nil
}

// ApplyOutput writes data to the named session's active pane using send-keys.
func (b *bridge) ApplyOutput(name string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	// Use -l (literal) flag to avoid send-keys interpreting special chars.
	_, err := b.tmuxErr("send-keys", "-t", name, "-l", string(data))
	return err
}

// RemoveSession kills the named local session.
func (b *bridge) RemoveSession(name string) error {
	_, err := b.tmuxErr("kill-session", "-t", name)
	return err
}

// EnsurePane creates a detached window named <paneID> in the <host> session.
func (b *bridge) EnsurePane(host, paneID string) error {
	target := host + ":" + paneID
	if err := b.run("has-session", "-t", target); err == nil {
		return nil // already exists
	}
	_, err := b.tmuxErr("new-window", "-t", host+":", "-n", paneID, "-d")
	return err
}

// RemovePane destroys the window named <paneID> in the <host> session.
func (b *bridge) RemovePane(host, paneID string) error {
	target := host + ":" + paneID
	if err := b.run("has-session", "-t", target); err != nil {
		return nil // already absent
	}
	_, err := b.tmuxErr("kill-window", "-t", target)
	return err
}

// ResizePane resizes the window <paneID> in the <host> session.
func (b *bridge) ResizePane(host, paneID string, rows, cols int) error {
	_, err := b.tmuxErr("resize-window", "-t", host+":"+paneID,
		"-x", fmt.Sprintf("%d", cols), "-y", fmt.Sprintf("%d", rows))
	return err
}

// run executes a tmux command and returns an error if the exit code is non-zero.
func (b *bridge) run(args ...string) error {
	return exec.Command("tmux", append([]string{"-L", b.socketName}, args...)...).Run()
}

// tmux runs a tmux -L <socketName> command and returns combined output + error.
// Returns (nil, nil) on exit code 0, (output, err) on non-zero.
func (b *bridge) tmux(args ...string) ([]byte, error) {
	fullArgs := append([]string{"-L", b.socketName}, args...)
	cmd := exec.Command("tmux", fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, err
	}
	return nil, nil
}

// tmuxErr runs tmux and returns an SSHError on non-zero exit.
func (b *bridge) tmuxErr(args ...string) ([]byte, error) {
	out, err := b.tmux(args...)
	if err != nil {
		return out, fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return out, nil
}

// errTmuxNotFound returns the canonical tmux_not_found error.
// Duplicated here to avoid a circular import with internal/ssh.
func errTmuxNotFound() *bridgeError {
	return &bridgeError{
		Code:    "tmux_not_found",
		Message: "tmux not found in PATH — install tmux to continue",
	}
}

func errSessionProjectionFailed(name, detail string) *bridgeError {
	return &bridgeError{
		Code:    "session_projection_failed",
		Message: fmt.Sprintf("failed to create local session for %s: %s", name, detail),
	}
}

// bridgeError is a typed error from bridge operations.
type bridgeError struct {
	Code    string
	Message string
}

func (e *bridgeError) Error() string { return e.Message }
