package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventLoggerEmit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	logger, err := NewEventLogger(path)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}

	err = logger.Emit("linkari_share", map[string]interface{}{
		"profile":     "travel",
		"url_domain":  "example.com",
		"status":      "success",
		"duration_ms": int64(42),
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	err = logger.Emit("linkari_digest", map[string]interface{}{
		"profile":    "travel",
		"item_count": 5,
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var ev Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("unmarshal line 0: %v", err)
	}
	if ev.EventType != "linkari_share" {
		t.Errorf("event_type = %q, want linkari_share", ev.EventType)
	}
	if ev.Timestamp == "" {
		t.Error("timestamp is empty")
	}
	if ev.Metadata["profile"] != "travel" {
		t.Errorf("metadata.profile = %v, want travel", ev.Metadata["profile"])
	}
	if ev.Metadata["status"] != "success" {
		t.Errorf("metadata.status = %v, want success", ev.Metadata["status"])
	}
}

// CT-1: Emit calls slog.Debug with msg=event_bus_emit, event_type, and metadata fields.
func TestEmitDebugLogging(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	logger, err := NewEventLogger(path)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	defer logger.Close()

	_ = logger.Emit("capture_async_start", map[string]interface{}{"url": "https://example.com"})

	out := buf.String()
	if !strings.Contains(out, "event_bus_emit") {
		t.Errorf("CT-1: slog.Debug not called with msg=event_bus_emit; got: %s", out)
	}
	if !strings.Contains(out, "capture_async_start") {
		t.Errorf("CT-1: event_type not present in debug log; got: %s", out)
	}
}

// CT-2: JSONL file write still occurs after the debug call (no regression).
func TestEmitJSONLWrittenAfterDebug(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	logger, err := NewEventLogger(path)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	defer logger.Close()

	if err := logger.Emit("test_event", map[string]interface{}{"k": "v"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "test_event") {
		t.Errorf("CT-2: JSONL not written after debug call; got: %s", data)
	}
}

// CT-3: slog.Debug fires even when l.f == nil (logger was closed before Emit).
func TestEmitDebugFiresOnNilFile(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	logger, err := NewEventLogger(path)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	logger.Close() // f is now nil

	_ = logger.Emit("nil_file_event", map[string]interface{}{"x": 1})

	out := buf.String()
	if !strings.Contains(out, "event_bus_emit") {
		t.Errorf("CT-3: slog.Debug did not fire when f == nil; got: %s", out)
	}
}

func TestDomainFromURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://example.com/path", "example.com"},
		{"http://sub.domain.org:8080/x", "sub.domain.org"},
		{"not-a-url", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := domainFromURL(tt.input)
		if got != tt.want {
			t.Errorf("domainFromURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
