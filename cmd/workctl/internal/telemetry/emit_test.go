package telemetry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestBuildEvent(t *testing.T) {
	ev := buildEvent("workctl", "review", 42, 0, map[string]string{"track": "staff"})

	if ev.Layer != "go_cli" {
		t.Errorf("expected layer go_cli, got %q", ev.Layer)
	}
	if ev.EventType != "command" {
		t.Errorf("expected event_type command, got %q", ev.EventType)
	}
	if ev.Command != "workctl review" {
		t.Errorf("expected command 'workctl review', got %q", ev.Command)
	}
	if ev.DurationMs != 42 {
		t.Errorf("expected duration_ms 42, got %d", ev.DurationMs)
	}
	if ev.ExitCode != 0 {
		t.Errorf("expected exit_code 0, got %d", ev.ExitCode)
	}
	if ev.SchemaVersion != "2" {
		t.Errorf("expected schema_version 2, got %q", ev.SchemaVersion)
	}
}

func TestBuildEventNestedSubcommand(t *testing.T) {
	ev := buildEvent("workctl", "cache stats", 10, 0, map[string]string{})
	if ev.Command != "workctl cache stats" {
		t.Errorf("expected 'workctl cache stats', got %q", ev.Command)
	}
}

func TestBuildEventErrorExit(t *testing.T) {
	ev := buildEvent("workctl", "career", 100, 1, map[string]string{})
	if ev.ExitCode != 1 {
		t.Errorf("expected exit_code 1, got %d", ev.ExitCode)
	}
}

func TestBuildEventEmptyFlags(t *testing.T) {
	ev := buildEvent("workctl", "version", 5, 0, map[string]string{})
	meta := ev.Metadata["flags"].(map[string]string)
	if len(meta) != 0 {
		t.Errorf("expected empty flags, got %v", meta)
	}
}

func TestWriteEvent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	ev := buildEvent("workctl", "review", 42, 0, map[string]string{"track": "staff"})
	if err := writeEvent(ev); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(filepath.Join(dir, "events"))
	if len(entries) != 1 {
		t.Fatalf("expected 1 event file, got %d", len(entries))
	}

	data, _ := os.ReadFile(filepath.Join(dir, "events", entries[0].Name()))
	var got Event
	if err := json.Unmarshal(data[:len(data)-1], &got); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if got.Command != "workctl review" {
		t.Errorf("expected command 'workctl review', got %q", got.Command)
	}
	if got.Layer != "go_cli" {
		t.Errorf("expected layer go_cli, got %q", got.Layer)
	}
}

func TestScrubFlags(t *testing.T) {
	flags := map[string]string{
		"token":   "abc123",
		"track":   "staff",
		"api-key": "secret",
	}
	scrubbed := scrubFlags(flags)
	if scrubbed["token"] != "[REDACTED]" {
		t.Errorf("expected token redacted, got %q", scrubbed["token"])
	}
	if scrubbed["track"] != "staff" {
		t.Errorf("expected track preserved, got %q", scrubbed["track"])
	}
	if scrubbed["api-key"] != "[REDACTED]" {
		t.Errorf("expected api-key redacted, got %q", scrubbed["api-key"])
	}
}

func TestScrubFlagsQuoteInValue(t *testing.T) {
	// Regression: single quotes in values should not require escaping (no fish subprocess).
	flags := map[string]string{"title": "it's"}
	scrubbed := scrubFlags(flags)
	if !strings.Contains(scrubbed["title"], "'") {
		t.Errorf("expected value preserved verbatim, got %q", scrubbed["title"])
	}
}

func TestInstrumentWrapsPreRunE(t *testing.T) {
	preRunECalled := false
	rootCmd := &cobra.Command{
		Use: "workctl",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			preRunECalled = true
			return nil
		},
	}
	sub := &cobra.Command{Use: "version", RunE: func(cmd *cobra.Command, args []string) error { return nil }}
	rootCmd.AddCommand(sub)

	tracker := Instrument(rootCmd, "workctl")
	if tracker.cliName != "workctl" {
		t.Errorf("expected cliName %q, got %q", "workctl", tracker.cliName)
	}

	rootCmd.SetArgs([]string{"version"})
	_ = rootCmd.Execute()
	if !preRunECalled {
		t.Error("original PersistentPreRunE should still be called")
	}
	if tracker.cmd == nil {
		t.Error("tracker.cmd should be set after Execute")
	}
}

func TestInstrumentFallsBackToPreRun(t *testing.T) {
	preRunCalled := false
	rootCmd := &cobra.Command{
		Use: "test",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			preRunCalled = true
		},
	}
	sub := &cobra.Command{Use: "sub", Run: func(cmd *cobra.Command, args []string) {}}
	rootCmd.AddCommand(sub)

	tracker := Instrument(rootCmd, "test")
	rootCmd.SetArgs([]string{"sub"})
	_ = rootCmd.Execute()

	if !preRunCalled {
		t.Error("original PersistentPreRun should still be called")
	}
	if tracker.cmd == nil {
		t.Error("tracker.cmd should be set after Execute")
	}
}

func TestInstrumentNoExistingHook(t *testing.T) {
	rootCmd := &cobra.Command{Use: "test"}
	tracker := Instrument(rootCmd, "test")

	if tracker.cliName != "test" {
		t.Errorf("expected cliName %q, got %q", "test", tracker.cliName)
	}
	if rootCmd.PersistentPreRun == nil {
		t.Error("PersistentPreRun should be set")
	}
}

func TestInstrumentSubcommandOverridesPreRunE(t *testing.T) {
	rootCmd := &cobra.Command{
		Use: "workctl",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
	// Subcommand overrides PersistentPreRunE (like versionCmd does)
	sub := &cobra.Command{
		Use:               "version",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
		RunE:              func(cmd *cobra.Command, args []string) error { return nil },
	}
	rootCmd.AddCommand(sub)

	tracker := Instrument(rootCmd, "workctl")
	rootCmd.SetArgs([]string{"version"})
	_ = rootCmd.Execute()

	// The root hook was overridden, so t.cmd should be nil. But root.Find
	// should still resolve it in Emit. We can't call Emit in a unit test
	// (no fish), so verify the tracker state directly.
	if tracker.cmd != nil {
		t.Error("expected tracker.cmd to be nil when subcommand overrides hook")
	}
	if tracker.root == nil {
		t.Error("expected tracker.root to be set")
	}
	if tracker.start.IsZero() {
		t.Error("expected tracker.start to be set at Instrument time")
	}
}

func TestEmitNilCmd(t *testing.T) {
	tracker := &Tracker{cliName: "workctl"}
	// cmd and root both nil — Emit should return silently without panic
	tracker.Emit(nil)
	tracker.Emit(errors.New("some error"))
}
