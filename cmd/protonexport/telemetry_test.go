package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestBuildEmitCmd(t *testing.T) {
	cmd, err := buildEmitCmd("protonexport", "export", 42, 0, map[string]string{"contact": "user@example.com"})
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		label, substr string
	}{
		{"layer", "--layer go_cli"},
		{"event-type", "--event-type command"},
		{"command", "--command 'protonexport export'"},
		{"duration", "--duration-ms 42"},
		{"exit-code", "--exit-code 0"},
		{"flag value", `"contact":"user@example.com"`},
	}
	for _, c := range checks {
		if !strings.Contains(cmd, c.substr) {
			t.Errorf("missing %s: want %q in %q", c.label, c.substr, cmd)
		}
	}
}

func TestBuildEmitCmdErrorExit(t *testing.T) {
	cmd, err := buildEmitCmd("protonexport", "export", 100, 1, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "--exit-code 1") {
		t.Errorf("expected exit-code 1 in %q", cmd)
	}
}

func TestBuildEmitCmdEmptyFlags(t *testing.T) {
	cmd, err := buildEmitCmd("protonexport", "export", 5, 0, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, `"flags":{}`) {
		t.Errorf("expected empty flags object in %q", cmd)
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
	tr := &tracker{cliName: "protonexport"}
	tr.emit(nil)
	tr.emit(errors.New("some error"))
}
