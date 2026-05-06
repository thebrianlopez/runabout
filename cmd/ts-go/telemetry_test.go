package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClassifySubcmd(t *testing.T) {
	hooks := []string{"fish", "__complete", "__completeNoDesc"}
	for _, sub := range hooks {
		if got := classifySubcmd(sub); got != "hook" {
			t.Errorf("classifySubcmd(%q) = %q, want %q", sub, got, "hook")
		}
	}

	userIntents := []string{"funcs", "types", "extract", "search", "rewrite", ""}
	for _, sub := range userIntents {
		if got := classifySubcmd(sub); got != "user_intent" {
			t.Errorf("classifySubcmd(%q) = %q, want %q", sub, got, "user_intent")
		}
	}
}

func TestBuildEventHookClass(t *testing.T) {
	ev := buildEvent("ts-go", "fish", 0, 0, map[string]string{})
	if ev.EventClass != "hook" {
		t.Errorf("event_class = %q, want %q", ev.EventClass, "hook")
	}
}

func TestBuildEventUserIntentClass(t *testing.T) {
	ev := buildEvent("ts-go", "funcs", 10, 0, map[string]string{})
	if ev.EventClass != "user_intent" {
		t.Errorf("event_class = %q, want %q", ev.EventClass, "user_intent")
	}
}

func TestEventClassInJSON(t *testing.T) {
	ev := buildEvent("ts-go", "fish", 0, 0, map[string]string{})
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !containsStr(s, `"event_class":"hook"`) {
		t.Errorf("expected event_class:hook in JSON, got: %s", s)
	}
}

func TestIsHookRateLimitedFirstCall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	// First call: not rate-limited, sentinel is created.
	if isHookRateLimited("ts-go fish", "/some/cwd") {
		t.Error("first call should not be rate-limited")
	}
}

func TestIsHookRateLimitedSecondCall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	// First call touches sentinel.
	isHookRateLimited("ts-go fish", "/some/cwd")
	// Second call within TTL should be rate-limited.
	if !isHookRateLimited("ts-go fish", "/some/cwd") {
		t.Error("second call within TTL should be rate-limited")
	}
}

func TestIsHookRateLimitedExpired(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	// Create sentinel with old mtime.
	sentinel := hookRateLimitSentinel("ts-go fish", "/old/cwd")
	os.MkdirAll(filepath.Dir(sentinel), 0o755)
	os.WriteFile(sentinel, nil, 0o644)
	old := time.Now().Add(-2 * hookRateLimitTTL)
	os.Chtimes(sentinel, old, old)

	// Should not be rate-limited — TTL has expired.
	if isHookRateLimited("ts-go fish", "/old/cwd") {
		t.Error("expired sentinel should not rate-limit")
	}
}

func TestIsHookRateLimitedDifferentCWD(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	// Rate-limit one (command, cwd) pair.
	isHookRateLimited("ts-go fish", "/cwd/a")
	if !isHookRateLimited("ts-go fish", "/cwd/a") {
		t.Error("same cwd should be rate-limited")
	}

	// Different cwd should have its own bucket — not rate-limited on first call.
	if isHookRateLimited("ts-go fish", "/cwd/b") {
		t.Error("different cwd should not be rate-limited on first call")
	}
}

func TestEmitHookRateLimiting(t *testing.T) {
	metricsDir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", metricsDir)
	t.Setenv("TMPDIR", t.TempDir())

	dateStr := time.Now().Format("2006-01-02")
	eventsPath := filepath.Join(metricsDir, "events", dateStr+".jsonl")

	emitHook := func() {
		ev := buildEvent("ts-go", "fish", 0, 0, map[string]string{})
		if !(ev.EventClass == "hook" && isHookRateLimited(ev.Command, ev.CWD)) {
			writeEvent(ev) //nolint:errcheck
		}
	}

	// First emit: not rate-limited, event should be written.
	emitHook()

	data1, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("expected events file after first emit: %v", err)
	}
	if countLines(data1) != 1 {
		t.Errorf("expected 1 event after first emit, got %d", countLines(data1))
	}

	// Second emit: rate-limited, event should NOT be written.
	emitHook()

	data2, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	if countLines(data2) != 1 {
		t.Errorf("expected 1 event after rate-limited emit, got %d", countLines(data2))
	}
}

func countLines(data []byte) int {
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	return n
}

func containsStr(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
