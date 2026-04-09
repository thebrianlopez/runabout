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
