package main

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// TmuxRunner wraps tmux operations for sending keys to sessions.
type TmuxRunner struct {
	Debug bool
}

// SendKeys sends text to a tmux target using send-keys -l (literal mode).
// If enter is true, it also sends C-m to execute the line.
func (t *TmuxRunner) SendKeys(target, text string, enter bool) error {
	if target == "" {
		return fmt.Errorf("tmux target is required")
	}

	if t.Debug {
		log.Printf("[DEBUG] tmux: send-keys target=%q text_len=%d enter=%t", target, len(text), enter)
	}

	// Ensure the target session exists, creating it if needed.
	sessionName := strings.Split(target, ":")[0]
	if err := t.createSession(sessionName); err != nil {
		return fmt.Errorf("ensure session: %w", err)
	}

	// send-keys -l sends text literally (no key name interpretation)
	cmd := exec.Command("tmux", "send-keys", "-t", target, "-l", text)
	if t.Debug {
		log.Printf("[DEBUG] tmux: exec %v", cmd.Args)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("send-keys: %w: %s", err, string(out))
	}

	if enter {
		cmd := exec.Command("tmux", "send-keys", "-t", target, "C-m")
		if t.Debug {
			log.Printf("[DEBUG] tmux: exec %v", cmd.Args)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("send-keys C-m: %w: %s", err, string(out))
		}
	}

	if t.Debug {
		log.Printf("[DEBUG] tmux: send-keys complete")
	}
	return nil
}

// createSession creates a named tmux session if it doesn't already exist.
func (t *TmuxRunner) createSession(name string) error {
	if t.sessionExists(name) {
		return nil
	}
	if t.Debug {
		log.Printf("[DEBUG] tmux: creating session %q", name)
	}
	cmd := exec.Command("tmux", "new-session", "-d", "-s", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create session %q: %w: %s", name, err, string(out))
	}
	return nil
}

// NewWindow creates a new tmux window in the given session and runs command in it.
// The window has remain-on-exit set to "failed" so it auto-closes on success
// but stays open when the command exits with a non-zero status.
func (t *TmuxRunner) NewWindow(session, command string) error {
	if session == "" {
		return fmt.Errorf("tmux session name is required")
	}

	if t.Debug {
		log.Printf("[DEBUG] tmux: new-window session=%q command=%q", session, command)
	}

	if err := t.createSession(session); err != nil {
		return fmt.Errorf("ensure session: %w", err)
	}

	// Create new window running the command in fish; exec fish keeps the
	// shell alive after uinit completes (on success, remain-on-exit closes it).
	shellCmd := fmt.Sprintf("%s; exec fish", command)
	cmd := exec.Command("tmux", "new-window", "-a", "-t", session, "fish", "-c", shellCmd)
	if t.Debug {
		log.Printf("[DEBUG] tmux: exec %v", cmd.Args)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("new-window: %w: %s", err, string(out))
	}

	// Set remain-on-exit to "failed" on the newly created (last) window so it
	// only persists when the command fails.
	setCmd := exec.Command("tmux", "set-option", "-p", "-t", session+":{end}", "remain-on-exit", "failed")
	if t.Debug {
		log.Printf("[DEBUG] tmux: exec %v", setCmd.Args)
	}
	if out, err := setCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("set remain-on-exit: %w: %s", err, string(out))
	}

	if t.Debug {
		log.Printf("[DEBUG] tmux: new-window complete")
	}
	return nil
}

// sessionExists checks if a tmux session exists.
func (t *TmuxRunner) sessionExists(name string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", name)
	return cmd.Run() == nil
}

// serverRunning checks if the tmux server is running (any sessions exist).
func (t *TmuxRunner) serverRunning() bool {
	cmd := exec.Command("tmux", "list-sessions")
	return cmd.Run() == nil
}
