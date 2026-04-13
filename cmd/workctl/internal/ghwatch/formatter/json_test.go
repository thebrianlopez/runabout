package formatter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/ghwatch/event"
)

func TestJSON_ValidJSONL(t *testing.T) {
	var buf bytes.Buffer
	f := NewJSON(&buf)

	ts := time.Date(2025, 6, 1, 14, 32, 5, 0, time.UTC)
	events := []event.Event{
		{
			ID:        "push-1",
			Kind:      event.KindPush,
			Repo:      "owner/repo",
			Timestamp: ts,
			Push: &event.PushDetail{
				Branch:  "main",
				Commits: []event.CommitInfo{{SHA: "abc123", Author: "alice", Message: "Fix"}},
			},
		},
		{
			ID:        "pr-42",
			Kind:      event.KindPR,
			Repo:      "owner/repo",
			Timestamp: ts,
			PR: &event.PRDetail{
				Number: 42,
				Title:  "Test PR",
				Author: "bob",
				Action: "opened",
				URL:    "https://github.com/owner/repo/pull/42",
			},
		},
	}

	for _, ev := range events {
		if err := f.Format(ev); err != nil {
			t.Fatalf("Format: %v", err)
		}
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSONL lines, got %d", len(lines))
	}

	for i, line := range lines {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("line %d: invalid JSON: %v\nline: %s", i, err, line)
		}
		if _, ok := m["id"]; !ok {
			t.Errorf("line %d: missing 'id' field", i)
		}
		if _, ok := m["kind"]; !ok {
			t.Errorf("line %d: missing 'kind' field", i)
		}
	}
}

func TestJSON_PushFields(t *testing.T) {
	var buf bytes.Buffer
	f := NewJSON(&buf)

	ts := time.Date(2025, 6, 1, 14, 32, 5, 0, time.UTC)
	ev := event.Event{
		ID:        "push-1",
		Kind:      event.KindPush,
		Repo:      "owner/repo",
		Timestamp: ts,
		Push: &event.PushDetail{
			Branch:  "main",
			Commits: []event.CommitInfo{{SHA: "abc123", Author: "alice", Message: "Fix"}},
		},
	}

	if err := f.Format(ev); err != nil {
		t.Fatalf("Format: %v", err)
	}

	var decoded event.Event
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.Kind != event.KindPush {
		t.Errorf("Kind: got %q, want %q", decoded.Kind, event.KindPush)
	}
	if decoded.Push == nil {
		t.Fatal("Push detail is nil")
	}
	if decoded.Push.Branch != "main" {
		t.Errorf("Branch: got %q, want %q", decoded.Push.Branch, "main")
	}
	if decoded.PR != nil {
		t.Error("PR detail should be nil for push event")
	}
}

func TestJSON_NilDetailOmitted(t *testing.T) {
	var buf bytes.Buffer
	f := NewJSON(&buf)

	ev := event.Event{
		ID:        "push-1",
		Kind:      event.KindPush,
		Repo:      "owner/repo",
		Timestamp: time.Now(),
		Push: &event.PushDetail{
			Branch:  "main",
			Commits: []event.CommitInfo{{SHA: "abc", Author: "a", Message: "m"}},
		},
	}

	if err := f.Format(ev); err != nil {
		t.Fatalf("Format: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, `"pr"`) {
		t.Errorf("expected pr field to be omitted, got: %s", out)
	}
	if strings.Contains(out, `"workflow"`) {
		t.Errorf("expected workflow field to be omitted, got: %s", out)
	}
}
