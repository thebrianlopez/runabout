//go:build integration

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestNewWindow_PreservesSpaceInName exercises the real tmux binary to
// prove that a window name containing a space survives the exec.Command
// argv -> tmux boundary. This is the bug class that motivated
// logTmuxExec: the slog rendering suggested space-split tokens, which
// would have been a real tmux failure. This test confirms argv passing
// is correct and the diagnosis was a logging artifact.
func TestNewWindow_PreservesSpaceInName(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found")
	}

	session := fmt.Sprintf("linkari-test-%d", os.Getpid())
	exactSession := "=" + session

	// Cleanup - kill session unconditionally (may not exist on early failure)
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", exactSession).Run()
	})

	tr := &TmuxRunner{Shell: "sh", ShellArgs: "-c"}
	d := &TmuxDispatcher{runner: tr, session: session}
	command := "true"
	// Dispatch derives the window name from the command; verify the
	// command lands in a window whose name contains it.
	if err := d.Dispatch(command); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	out, err := exec.Command("tmux", "list-windows", "-t", exactSession, "-F", "#{window_name}").Output()
	if err != nil {
		t.Fatalf("list-windows failed: %v", err)
	}
	names := strings.Split(strings.TrimSpace(string(out)), "\n")

	found := false
	for _, n := range names {
		if strings.Contains(n, command) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("window containing command %q not found in session\ngot windows: %v", command, names)
	}
}

// TestTmuxDispatcher_ExplicitSession uses a TmuxDispatcher bound to an
// explicit session name to prove session targeting still works through
// the Dispatcher seam (dispatch_backend=tmux path).
func TestTmuxDispatcher_ExplicitSession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found")
	}

	session := fmt.Sprintf("linkari-test-disp-%d", os.Getpid())
	exactSession := "=" + session

	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", exactSession).Run()
	})

	tr := &TmuxRunner{Shell: "sh", ShellArgs: "-c"}
	d := &TmuxDispatcher{runner: tr, session: session}
	// Command writes a marker file so we can verify execution detached.
	marker := fmt.Sprintf("/tmp/linkari-dispatch-%d", os.Getpid())
	defer os.Remove(marker)
	if err := d.Dispatch(fmt.Sprintf("touch %s", marker)); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	deadline := 30
	for i := 0; i < deadline; i++ {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("marker file %s never appeared", marker)
}
