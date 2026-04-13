package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestSpinner_NonTTY_PlainLines(t *testing.T) {
	var buf bytes.Buffer
	s := &Spinner{w: &buf, tty: false}

	s.Start("Fetching Jira...")
	s.Update("Fetching Confluence...")
	s.Stop("Done.")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(lines), buf.String())
	}
	if lines[0] != "Fetching Jira..." {
		t.Errorf("line[0] = %q, want %q", lines[0], "Fetching Jira...")
	}
	if lines[1] != "Fetching Confluence..." {
		t.Errorf("line[1] = %q, want %q", lines[1], "Fetching Confluence...")
	}
	if lines[2] != "Done." {
		t.Errorf("line[2] = %q, want %q", lines[2], "Done.")
	}
}

func TestSpinner_TTY_CarriageReturn(t *testing.T) {
	var buf bytes.Buffer
	s := &Spinner{w: &buf, tty: true}

	s.Start("step 1")
	s.Update("step 2")
	s.Stop("finished")

	out := buf.String()

	// TTY mode uses \r for overwrite
	if !strings.Contains(out, "\r") {
		t.Errorf("TTY output should contain \\r, got %q", out)
	}
	// Final message should end with newline
	if !strings.HasSuffix(out, "finished\n") {
		t.Errorf("TTY output should end with final message + newline, got %q", out)
	}
}

func TestSpinner_StopEmpty(t *testing.T) {
	var buf bytes.Buffer
	s := &Spinner{w: &buf, tty: false}

	s.Start("working...")
	s.Stop("")

	// Stop with empty message should not print an extra line
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %q", len(lines), buf.String())
	}
}

func TestSpinner_UpdateBeforeStart(t *testing.T) {
	var buf bytes.Buffer
	s := &Spinner{w: &buf, tty: false}

	// Update before Start should be a no-op
	s.Update("ignored")
	if buf.Len() != 0 {
		t.Errorf("Update before Start should be no-op, got %q", buf.String())
	}
}

func TestSpinner_TTY_ClearsLine(t *testing.T) {
	var buf bytes.Buffer
	s := &Spinner{w: &buf, tty: true}

	s.Start("short")
	s.Update("longer message")
	s.Stop("")

	out := buf.String()
	// Should have clearing sequences (spaces)
	if !strings.Contains(out, "\r") {
		t.Errorf("expected \\r in TTY output, got %q", out)
	}
}
