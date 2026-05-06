package telemetry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestBuildEvent(t *testing.T) {
	event := buildEvent("mdq", "query", 42, 0, map[string]string{"field": "Status"})

	if event.SchemaVersion != "2" {
		t.Errorf("schema_version = %q, want %q", event.SchemaVersion, "2")
	}
	if event.Layer != "go_cli" {
		t.Errorf("layer = %q, want %q", event.Layer, "go_cli")
	}
	if event.EventType != "command" {
		t.Errorf("event_type = %q, want %q", event.EventType, "command")
	}
	if event.EventClass != "user_intent" {
		t.Errorf("event_class = %q, want %q", event.EventClass, "user_intent")
	}
	if event.Command != "mdq query" {
		t.Errorf("command = %q, want %q", event.Command, "mdq query")
	}
	if event.DurationMs != 42 {
		t.Errorf("duration_ms = %d, want %d", event.DurationMs, 42)
	}
	if event.ExitCode != 0 {
		t.Errorf("exit_code = %d, want %d", event.ExitCode, 0)
	}
	if event.Timestamp == "" {
		t.Error("timestamp should not be empty")
	}

	// Verify metadata contains scrubbed flags.
	flags, ok := event.Metadata["flags"].(map[string]string)
	if !ok {
		t.Fatal("metadata.flags missing or wrong type")
	}
	if flags["field"] != "Status" {
		t.Errorf("metadata.flags.field = %q, want %q", flags["field"], "Status")
	}
}

func TestBuildEventErrorExit(t *testing.T) {
	event := buildEvent("perfgate", "run", 100, 1, map[string]string{})
	if event.ExitCode != 1 {
		t.Errorf("exit_code = %d, want %d", event.ExitCode, 1)
	}
}

func TestBuildEventEmptyFlags(t *testing.T) {
	event := buildEvent("shellprof", "profile", 5, 0, map[string]string{})
	flags, ok := event.Metadata["flags"].(map[string]string)
	if !ok {
		t.Fatal("metadata.flags missing or wrong type")
	}
	if len(flags) != 0 {
		t.Errorf("expected empty flags, got %v", flags)
	}
}

func TestWriteEventCreatesValidJSONL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	event := buildEvent("mdq", "query", 42, 0, map[string]string{"field": "Status"})
	if err := writeEvent(event); err != nil {
		t.Fatal(err)
	}

	dateStr := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, "events", dateStr+".jsonl")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading events file: %v", err)
	}

	// Must be valid JSON.
	var parsed Event
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\ndata: %s", err, data)
	}

	if parsed.SchemaVersion != "2" {
		t.Errorf("schema_version = %q, want %q", parsed.SchemaVersion, "2")
	}
	if parsed.Command != "mdq query" {
		t.Errorf("command = %q, want %q", parsed.Command, "mdq query")
	}
	if parsed.EventClass != "user_intent" {
		t.Errorf("event_class = %q, want %q", parsed.EventClass, "user_intent")
	}
}

func TestWriteEventNoFishRequired(t *testing.T) {
	// Ensure telemetry works even when fish is not in PATH.
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)
	t.Setenv("PATH", "/nonexistent")

	event := buildEvent("mdq", "query", 10, 0, map[string]string{})
	if err := writeEvent(event); err != nil {
		t.Fatalf("writeEvent should not require fish: %v", err)
	}

	dateStr := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, "events", dateStr+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("events file should exist: %v", err)
	}
}

func TestWriteEventRotation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	eventsPath := filepath.Join(dir, "events")
	os.MkdirAll(eventsPath, 0o755)

	dateStr := time.Now().Format("2006-01-02")
	path := filepath.Join(eventsPath, dateStr+".jsonl")

	// Create an oversized file to trigger rotation.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(maxEventFileSize + 1)
	f.Close()

	event := buildEvent("mdq", "query", 1, 0, map[string]string{})
	if err := writeEvent(event); err != nil {
		t.Fatal(err)
	}

	// Original should be rotated to .1
	rotated := path + ".1"
	if _, err := os.Stat(rotated); err != nil {
		t.Errorf("rotated file should exist: %v", err)
	}

	// New file should contain the event.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed Event
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("new file should contain valid JSON: %v", err)
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

func TestScrubFlagsRedactsSensitive(t *testing.T) {
	flags := map[string]string{
		"auth-token": "abc123",
		"api-key":    "secret-val",
		"password":   "hunter2",
		"authkey":    "mykey",
		"secret":     "s3cr3t",
		"output":     "json",
		"verbose":    "true",
	}
	scrubbed := scrubFlags(flags)

	for _, name := range []string{"auth-token", "api-key", "password", "authkey", "secret"} {
		if scrubbed[name] != "[REDACTED]" {
			t.Errorf("flag %q should be redacted, got %q", name, scrubbed[name])
		}
	}
	for _, name := range []string{"output", "verbose"} {
		if scrubbed[name] == "[REDACTED]" {
			t.Errorf("flag %q should not be redacted", name)
		}
	}
}

func TestScrubFlagsCaseInsensitive(t *testing.T) {
	flags := map[string]string{
		"AuthKey":   "val1",
		"API_TOKEN": "val2",
		"Password":  "val3",
	}
	scrubbed := scrubFlags(flags)
	for name, val := range scrubbed {
		if val != "[REDACTED]" {
			t.Errorf("flag %q should be redacted, got %q", name, val)
		}
	}
}

func TestScrubFlagsEmpty(t *testing.T) {
	scrubbed := scrubFlags(map[string]string{})
	if len(scrubbed) != 0 {
		t.Error("expected empty map")
	}
}

func TestBuildEventRedactsSensitiveFlags(t *testing.T) {
	event := buildEvent("wasend", "send", 50, 0, map[string]string{
		"auth-token": "real-secret",
		"recipient":  "user@example.com",
	})

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)

	if contains(s, "real-secret") {
		t.Error("sensitive value should not appear in event JSON")
	}
	if !contains(s, "[REDACTED]") {
		t.Error("expected [REDACTED] in event JSON")
	}
	if !contains(s, "user@example.com") {
		t.Error("non-sensitive value should be preserved")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestEmitNilCmd(t *testing.T) {
	tracker := &Tracker{cliName: "mdq"}
	// cmd is nil — Emit should return silently without panic
	tracker.Emit(nil)
	tracker.Emit(errors.New("some error"))
}

func TestEventMarshalNullPointers(t *testing.T) {
	event := buildEvent("mdq", "query", 10, 0, map[string]string{})

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}

	// Agent, epic, milestone should be null when not set.
	s := string(data)
	if !contains(s, `"agent":null`) {
		t.Errorf("agent should be null, got: %s", s)
	}
	if !contains(s, `"epic":null`) {
		t.Errorf("epic should be null, got: %s", s)
	}
	if !contains(s, `"milestone":null`) {
		t.Errorf("milestone should be null, got: %s", s)
	}
}
