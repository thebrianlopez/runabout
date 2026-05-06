package main

// POMO event-bus-tsgo-fish-polling-noise: F1 contract tests for cmd/linkari/telemetry.go.
// TDD: PERSONAL_20260506T165257Z_Runabout_EventBusEventClassification_TDD.md

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCT1_ClassifyHookFish(t *testing.T) {
	if got := classifyEventClass("ts-go", "fish"); got != "hook" {
		t.Errorf("CT-1: classifyEventClass(ts-go,fish) = %q, want \"hook\"", got)
	}
}

func TestCT2_ClassifyHookCompletePrefix(t *testing.T) {
	for _, subcmd := range []string{"__complete", "__completeNoDesc"} {
		if got := classifyEventClass("ts-go", subcmd); got != "hook" {
			t.Errorf("CT-2: classifyEventClass(ts-go,%s) = %q, want \"hook\"", subcmd, got)
		}
	}
}

func TestCT3_ClassifyUserIntentOtherCLI(t *testing.T) {
	if got := classifyEventClass("linkari", "search"); got != "user_intent" {
		t.Errorf("CT-3: classifyEventClass(linkari,search) = %q, want \"user_intent\"", got)
	}
}

func TestCT4_ClassifyUserIntentNonHookSubcmd(t *testing.T) {
	if got := classifyEventClass("ts-go", "version"); got != "user_intent" {
		t.Errorf("CT-4: classifyEventClass(ts-go,version) = %q, want \"user_intent\"", got)
	}
}

func TestCT5_ShouldEmitFirstCall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)
	if !shouldEmitHookEvent("ts-go fish", "/some/cwd", 60*time.Second) {
		t.Error("CT-5: first call should return true (not rate-limited)")
	}
}

func TestCT6_ShouldEmitSecondCallWithinTTL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)
	shouldEmitHookEvent("ts-go fish", "/cwd", 60*time.Second)
	if shouldEmitHookEvent("ts-go fish", "/cwd", 60*time.Second) {
		t.Error("CT-6: second call within TTL should return false (rate-limited)")
	}
}

func TestCT7_ShouldEmitTTLZeroAlwaysEmits(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)
	shouldEmitHookEvent("ts-go fish", "/old/cwd", 60*time.Second)
	if !shouldEmitHookEvent("ts-go fish", "/old/cwd", 0) {
		t.Error("CT-7: ttl=0 should treat any sentinel as expired and return true")
	}
}

func TestCT8_BuildEventHookClass(t *testing.T) {
	ev := buildEvent("ts-go", "fish", 0, 0, map[string]string{})
	if ev.EventClass != "hook" {
		t.Errorf("CT-8: buildEvent(ts-go,fish).EventClass = %q, want \"hook\"", ev.EventClass)
	}
}

func TestCT9_BuildEventUserIntentClass(t *testing.T) {
	ev := buildEvent("linkari", "search", 10, 0, map[string]string{})
	if ev.EventClass != "user_intent" {
		t.Errorf("CT-9: buildEvent(linkari,search).EventClass = %q, want \"user_intent\"", ev.EventClass)
	}
}

func TestCT10_EventClassInJSONL(t *testing.T) {
	ev := buildEvent("ts-go", "fish", 0, 0, map[string]string{})
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if got, _ := parsed["event_class"].(string); got != "hook" {
		t.Errorf("CT-10: JSONL event_class = %q, want \"hook\"", got)
	}
}

func TestCT11_HookEventSuppressedWithinTTL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	dateStr := time.Now().Format("2006-01-02")
	eventsPath := filepath.Join(dir, "events", dateStr+".jsonl")

	emit := func() {
		ev := buildEvent("ts-go", "fish", 0, 0, map[string]string{})
		if ev.EventClass == "hook" && !shouldEmitHookEvent(ev.Command, ev.CWD, 60*time.Second) {
			return
		}
		writeEvent(ev) //nolint:errcheck
	}

	emit()
	data1, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("CT-11: events file missing after first emit: %v", err)
	}
	if countLFt(data1) != 1 {
		t.Errorf("CT-11: expected 1 event after first emit, got %d", countLFt(data1))
	}

	emit()
	data2, _ := os.ReadFile(eventsPath)
	if countLFt(data2) != 1 {
		t.Errorf("CT-11: expected still 1 event after suppressed emit, got %d", countLFt(data2))
	}
}

func TestCT12_UserIntentAlwaysWritten(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	dateStr := time.Now().Format("2006-01-02")
	eventsPath := filepath.Join(dir, "events", dateStr+".jsonl")

	emit := func() {
		ev := buildEvent("linkari", "search", 5, 0, map[string]string{})
		if ev.EventClass == "hook" && !shouldEmitHookEvent(ev.Command, ev.CWD, 60*time.Second) {
			return
		}
		writeEvent(ev) //nolint:errcheck
	}

	emit()
	emit()

	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("CT-12: events file missing: %v", err)
	}
	if countLFt(data) != 2 {
		t.Errorf("CT-12: expected 2 user-intent events, got %d", countLFt(data))
	}
}

func countLFt(data []byte) int {
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	return n
}
