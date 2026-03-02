package telemetry

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestBuildEmitCmd(t *testing.T) {
	cmd, err := buildEmitCmd("mdq", "query", 42, 0, map[string]string{"field": "Status"})
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		label, substr string
	}{
		{"layer", "--layer go_cli"},
		{"event-type", "--event-type command"},
		{"command", "--command 'mdq query'"},
		{"duration", "--duration-ms 42"},
		{"exit-code", "--exit-code 0"},
		{"flag value", `"field":"Status"`},
	}
	for _, c := range checks {
		if !strings.Contains(cmd, c.substr) {
			t.Errorf("missing %s: want %q in %q", c.label, c.substr, cmd)
		}
	}
}

func TestBuildEmitCmdErrorExit(t *testing.T) {
	cmd, err := buildEmitCmd("perfgate", "run", 100, 1, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "--exit-code 1") {
		t.Errorf("expected exit-code 1 in %q", cmd)
	}
}

func TestBuildEmitCmdEmptyFlags(t *testing.T) {
	cmd, err := buildEmitCmd("shellprof", "profile", 5, 0, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, `"flags":{}`) {
		t.Errorf("expected empty flags object in %q", cmd)
	}
}

func TestBuildEmitCmdSingleQuoteEscaping(t *testing.T) {
	cmd, err := buildEmitCmd("mdq", "query", 10, 0, map[string]string{"field": "it's"})
	if err != nil {
		t.Fatal(err)
	}
	// Single quotes in metadata should be escaped for fish embedding
	if !strings.Contains(cmd, `it'\''s`) {
		t.Errorf("single quote not escaped in %q", cmd)
	}
}

func TestInstrument(t *testing.T) {
	rootCmd := &cobra.Command{Use: "test"}
	tracker := Instrument(rootCmd, "test")

	if tracker.cliName != "test" {
		t.Errorf("expected cliName %q, got %q", "test", tracker.cliName)
	}
	if rootCmd.PersistentPreRun == nil {
		t.Error("PersistentPreRun should be set")
	}
}

func TestEmitNilCmd(t *testing.T) {
	tracker := &Tracker{cliName: "mdq"}
	// cmd is nil — Emit should return silently without panic
	tracker.Emit(nil)
	tracker.Emit(errors.New("some error"))
}
