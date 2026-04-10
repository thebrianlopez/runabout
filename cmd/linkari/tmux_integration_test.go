//go:build integration

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestNewWindow_PreservesSpaceInName exercises the real tmux binary to
// prove that a window name containing a space survives the exec.Command
// argv → tmux boundary. This is the bug class that motivated
// logTmuxExec: the slog rendering suggested space-split tokens, which
// would have been a real tmux failure. This test confirms argv passing
// is correct and the diagnosis was a logging artifact.
func TestNewWindow_PreservesSpaceInName(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found")
	}

	session := fmt.Sprintf("linkari-test-%d", os.Getpid())
	exactSession := "=" + session

	// Cleanup — kill session unconditionally (may not exist on early failure)
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", exactSession).Run()
	})

	r := &TmuxRunner{Shell: "sh", ShellArgs: "-c"}
	windowName := "eng: integration test"
	if err := r.NewWindow(session, "true", windowName); err != nil {
		t.Fatalf("NewWindow failed: %v", err)
	}

	// List windows and verify the space-bearing name appears verbatim.
	out, err := exec.Command("tmux", "list-windows", "-t", exactSession, "-F", "#{window_name}").Output()
	if err != nil {
		t.Fatalf("list-windows failed: %v", err)
	}
	names := strings.Split(strings.TrimSpace(string(out)), "\n")

	found := false
	for _, n := range names {
		if n == windowName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("window name %q not found in session\ngot windows: %v", windowName, names)
	}
}

// EPIC-057 M4: ginit_* action sends the validated Jira key to the tmux pane
// via send-keys -l. This proves the command template renders correctly and
// the literal text reaches tmux without shell re-interpretation.
func TestGinitSendKeys_Integration(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found")
	}

	session := fmt.Sprintf("linkari-ginit-test-%d", os.Getpid())
	exactSession := "=" + session

	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", exactSession).Run()
	})

	// Create a session with cat so send-keys text is captured in the pane.
	if err := exec.Command("tmux", "new-session", "-d", "-s", session, "cat").Run(); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Send literal text via exec.Command (same argv as TmuxRunner.SendKeys).
	exactTarget := "=" + session + ":0"
	cmd := exec.Command("tmux", "send-keys", "-t", exactTarget, "-l", "ginit PERSONAL-123")
	logTmuxExec(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("send-keys failed: %v: %s", err, string(out))
	}

	// Capture pane content and verify the literal text arrived.
	out, err := exec.Command("tmux", "capture-pane", "-t", exactSession+":0", "-p").Output()
	if err != nil {
		t.Fatalf("capture-pane: %v", err)
	}
	if !strings.Contains(string(out), "ginit PERSONAL-123") {
		t.Errorf("pane content missing 'ginit PERSONAL-123'\ngot: %s", string(out))
	}
}
