package main

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// TmuxRunner wraps tmux operations for sending keys to sessions.
// Shell and ShellArgs control which shell runs inside new tmux windows.
// Defaults: Shell="fish", ShellArgs="-c".
type TmuxRunner struct {
	Debug     bool
	Shell     string // shell binary (default: "fish")
	ShellArgs string // shell command flag (default: "-c")
}

// shell returns the configured shell or "fish".
func (t *TmuxRunner) shell() string {
	if t.Shell != "" {
		return t.Shell
	}
	return "fish"
}

// shellArgs returns the configured shell args or "-c".
func (t *TmuxRunner) shellArgs() string {
	if t.ShellArgs != "" {
		return t.ShellArgs
	}
	return "-c"
}

// SendKeys sends text to a tmux target using send-keys -l (literal mode).
// If enter is true, it also sends C-m to execute the line.
func (t *TmuxRunner) SendKeys(target, text string, enter bool) error {
	if target == "" {
		return fmt.Errorf("tmux target is required")
	}

	slog.Debug("tmux send-keys",
		"event_type", "tmux_send_keys",
		"target", target,
		"text_len", len(text),
		"enter", enter,
	)

	// Ensure the target session exists, creating it if needed.
	sessionName := strings.Split(target, ":")[0]
	if err := t.createSession(sessionName); err != nil {
		return fmt.Errorf("ensure session: %w", err)
	}

	// send-keys -l sends text literally (no key name interpretation).
	// Use "=" prefix on the session part for exact matching.
	exactTarget := "=" + target
	cmd := exec.Command("tmux", "send-keys", "-t", exactTarget, "-l", text)
	slog.Debug("tmux exec", "args", cmd.Args)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("send-keys: %w: %s", err, string(out))
	}

	if enter {
		cmd := exec.Command("tmux", "send-keys", "-t", exactTarget, "C-m")
		slog.Debug("tmux exec", "args", cmd.Args)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("send-keys C-m: %w: %s", err, string(out))
		}
	}

	slog.Debug("tmux send-keys complete")
	return nil
}

// createSession creates a named tmux session if it doesn't already exist.
func (t *TmuxRunner) createSession(name string) error {
	if t.sessionExists(name) {
		return nil
	}
	slog.Debug("tmux creating session", "session", name)
	cmd := exec.Command("tmux", "new-session", "-d", "-s", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create session %q: %w: %s", name, err, string(out))
	}
	return nil
}

// NewWindow creates a new tmux window in the given session and runs command in it.
// The optional name parameter sets the window name via -n; if empty, tmux uses its default.
// The window has remain-on-exit set to "failed" so it auto-closes on success
// but stays open when the command exits with a non-zero status.
func (t *TmuxRunner) NewWindow(session, command string, name string) error {
	if session == "" {
		return fmt.Errorf("tmux session name is required")
	}

	slog.Debug("tmux new-window",
		"event_type", "tmux_new_window",
		"session", session,
		"command", command,
		"name", name,
	)

	if err := t.createSession(session); err != nil {
		return fmt.Errorf("ensure session: %w", err)
	}

	// Create new window running the command in the configured shell; exec keeps
	// the shell alive after the command completes (on success, remain-on-exit closes it).
	// Use "=" prefix for exact session matching to prevent tmux from resolving
	// the target to a window with the same name in a different session.
	sh := t.shell()
	shellCmd := fmt.Sprintf("%s; exec %s", command, sh)
	exactSession := "=" + session
	args := []string{"new-window", "-a", "-t", exactSession}
	if name != "" {
		args = append(args, "-n", name)
	}
	args = append(args, sh, t.shellArgs(), shellCmd)
	cmd := exec.Command("tmux", args...)
	slog.Debug("tmux exec", "args", cmd.Args)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("new-window: %w: %s", err, string(out))
	}

	// Set remain-on-exit to "failed" on the newly created (last) window so it
	// only persists when the command fails.
	setCmd := exec.Command("tmux", "set-option", "-p", "-t", exactSession+":{end}", "remain-on-exit", "failed")
	slog.Debug("tmux exec", "args", setCmd.Args)
	if out, err := setCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("set remain-on-exit: %w: %s", err, string(out))
	}

	slog.Debug("tmux new-window complete")
	return nil
}

// sessionExists checks if a tmux session with this exact name exists.
// The "=" prefix forces exact matching — without it, tmux resolves "-t linkari"
// to a window named "linkari" in another session (e.g. local:linkari), causing
// new windows to land in the wrong session.
func (t *TmuxRunner) sessionExists(name string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", "="+name)
	return cmd.Run() == nil
}

// serverRunning checks if the tmux server is running (any sessions exist).
func (t *TmuxRunner) serverRunning() bool {
	cmd := exec.Command("tmux", "list-sessions")
	return cmd.Run() == nil
}
