package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestBuildEmitCmd(t *testing.T) {
	cmd, err := buildEmitCmd("wasend", "send", 42, 0, map[string]string{"to": "15551234567"})
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		label, substr string
	}{
		{"layer", "--layer go_cli"},
		{"event-type", "--event-type command"},
		{"command", "--command 'wasend send'"},
		{"duration", "--duration-ms 42"},
		{"exit-code", "--exit-code 0"},
		{"flag value", `"to":"15551234567"`},
	}
	for _, c := range checks {
		if !strings.Contains(cmd, c.substr) {
			t.Errorf("missing %s: want %q in %q", c.label, c.substr, cmd)
		}
	}
}

func TestBuildEmitCmdErrorExit(t *testing.T) {
	cmd, err := buildEmitCmd("wasend", "send", 100, 1, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "--exit-code 1") {
		t.Errorf("expected exit-code 1 in %q", cmd)
	}
}

func TestBuildEmitCmdEmptyFlags(t *testing.T) {
	cmd, err := buildEmitCmd("wasend", "login", 5, 0, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, `"flags":{}`) {
		t.Errorf("expected empty flags object in %q", cmd)
	}
}

func TestBuildEmitCmdSingleQuoteEscaping(t *testing.T) {
	cmd, err := buildEmitCmd("wasend", "send", 10, 0, map[string]string{"msg": "it's"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, `it'\''s`) {
		t.Errorf("single quote not escaped in %q", cmd)
	}
}

func TestInstrument(t *testing.T) {
	rootCmd := &cobra.Command{Use: "test"}
	tr := instrument(rootCmd, "test")

	if tr.cliName != "test" {
		t.Errorf("expected cliName %q, got %q", "test", tr.cliName)
	}
	if rootCmd.PersistentPreRun == nil {
		t.Error("PersistentPreRun should be set")
	}
}

func TestEmitNilCmd(t *testing.T) {
	tr := &tracker{cliName: "wasend"}
	// cmd is nil — emit should return silently without panic
	tr.emit(nil)
	tr.emit(errors.New("some error"))
}
