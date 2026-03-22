package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// TmuxRunner wraps tmux operations for sending keys to sessions.
type TmuxRunner struct {
	DefaultSession string
}

// SendKeys sends text to a tmux target using send-keys -l (literal mode).
// If enter is true, it also sends C-m to execute the line.
func (t *TmuxRunner) SendKeys(target, text string, enter bool) error {
	if target == "" {
		target = t.DefaultSession + ":0"
	}

	if err := t.ensureSession(); err != nil {
		return fmt.Errorf("ensure session: %w", err)
	}

	// Validate the target session exists
	sessionName := strings.Split(target, ":")[0]
	if !t.sessionExists(sessionName) {
		return fmt.Errorf("tmux session %q does not exist", sessionName)
	}

	// send-keys -l sends text literally (no key name interpretation)
	cmd := exec.Command("tmux", "send-keys", "-t", target, "-l", text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("send-keys: %w: %s", err, string(out))
	}

	if enter {
		cmd := exec.Command("tmux", "send-keys", "-t", target, "C-m")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("send-keys C-m: %w: %s", err, string(out))
		}
	}

	return nil
}

// ensureSession creates the default tmux session if it doesn't exist.
func (t *TmuxRunner) ensureSession() error {
	if t.sessionExists(t.DefaultSession) {
		return nil
	}

	cmd := exec.Command("tmux", "new-session", "-d", "-s", t.DefaultSession)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create session %q: %w: %s", t.DefaultSession, err, string(out))
	}

	return nil
}

// sessionExists checks if a tmux session exists.
func (t *TmuxRunner) sessionExists(name string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", name)
	return cmd.Run() == nil
}
