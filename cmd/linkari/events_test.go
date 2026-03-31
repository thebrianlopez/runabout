package main

import (
	"encoding/json"
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
