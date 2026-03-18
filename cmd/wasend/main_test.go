package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRootCommandHelp(t *testing.T) {
	rootCmd := &cobra.Command{
		Use:   "wasend",
		Short: "Send WhatsApp messages from the command line",
	}
	rootCmd.AddCommand(loginCmd())
	rootCmd.AddCommand(sendCmd())
	rootCmd.AddCommand(logoutCmd())

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"--help"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"login", "send", "logout"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %q subcommand", want)
		}
	}
}

func TestSendRequiresTo(t *testing.T) {
	cmd := sendCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"hello"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --to is missing")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("expected 'required' in error, got: %v", err)
	}
}

func TestSendRequiresMessage(t *testing.T) {
	// Use a temp DB so newClient doesn't fail on directory issues
	tmp := t.TempDir()
	dbPath = filepath.Join(tmp, "test.db")

	cmd := sendCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"-t", "15551234567"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no message provided")
	}
	if !strings.Contains(err.Error(), "provide a message") {
		t.Errorf("expected 'provide a message' in error, got: %v", err)
	}
}

func TestSendEmptyMessage(t *testing.T) {
	tmp := t.TempDir()
	dbPath = filepath.Join(tmp, "test.db")

	cmd := sendCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"-t", "15551234567", ""})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for empty message")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected 'empty' in error, got: %v", err)
	}
}

func TestSendNotLoggedIn(t *testing.T) {
	tmp := t.TempDir()
	dbPath = filepath.Join(tmp, "test.db")

	cmd := sendCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"-t", "15551234567", "hello"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when not logged in")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in' in error, got: %v", err)
	}
}

func TestLogoutNoSession(t *testing.T) {
	tmp := t.TempDir()
	dbPath = filepath.Join(tmp, "test.db")

	cmd := logoutCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	// Redirect stdout to capture "No active session." output
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := cmd.Execute()

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out bytes.Buffer
	out.ReadFrom(r)
	if !strings.Contains(out.String(), "No active session") {
		t.Errorf("expected 'No active session' output, got: %s", out.String())
	}
}

func TestNewClientCreatesDir(t *testing.T) {
	tmp := t.TempDir()
	dbPath = filepath.Join(tmp, "subdir", "test.db")

	_, container, err := newClient()
	if err != nil {
		t.Fatalf("newClient failed: %v", err)
	}
	defer container.Close()

	if _, err := os.Stat(filepath.Join(tmp, "subdir")); os.IsNotExist(err) {
		t.Error("expected db directory to be created")
	}
}

func TestVersionFormat(t *testing.T) {
	v := version
	c := commit
	d := date
	formatted := v + " (commit: " + c + ", built: " + d + ")"
	if !strings.Contains(formatted, "0.1.0") {
		t.Errorf("version format unexpected: %s", formatted)
	}
}

func TestResolveMessageFromArgs(t *testing.T) {
	msg, err := resolveMessage(false, []string{"hello", "world"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", msg)
	}
}

func TestResolveMessageFromStdin(t *testing.T) {
	reader := strings.NewReader("  piped message  \n")
	msg, err := resolveMessage(true, nil, reader)
	if err != nil {
		t.Fatal(err)
	}
	if msg != "piped message" {
		t.Errorf("expected %q, got %q", "piped message", msg)
	}
}

func TestResolveMessageNoInput(t *testing.T) {
	_, err := resolveMessage(false, nil, nil)
	if err == nil {
		t.Fatal("expected error when no args and no stdin")
	}
	if !strings.Contains(err.Error(), "provide a message") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseRecipientValid(t *testing.T) {
	jid, err := parseRecipient("15551234567")
	if err != nil {
		t.Fatal(err)
	}
	if jid.User != "15551234567" {
		t.Errorf("expected user %q, got %q", "15551234567", jid.User)
	}
	if jid.Server != "s.whatsapp.net" {
		t.Errorf("expected server %q, got %q", "s.whatsapp.net", jid.Server)
	}
}
