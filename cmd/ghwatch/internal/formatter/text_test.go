package formatter

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/thebrianlopez/runabout/cmd/ghwatch/internal/event"
)

func TestText_PushEvent(t *testing.T) {
	var buf bytes.Buffer
	f := NewText(&buf)

	ts := time.Date(2025, 6, 1, 14, 32, 5, 0, time.UTC)
	ev := event.Event{
		ID:        "push-1",
		Kind:      event.KindPush,
		Repo:      "owner/repo",
		Timestamp: ts,
		Push: &event.PushDetail{
			Branch: "main",
			Commits: []event.CommitInfo{
				{SHA: "abc1234567890", Author: "alice", Message: "Fix login bug"},
			},
		},
	}

	if err := f.Format(ev); err != nil {
		t.Fatalf("Format: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "PUSH") {
		t.Errorf("expected PUSH label, got: %s", out)
	}
	if !strings.Contains(out, "main") {
		t.Errorf("expected branch 'main', got: %s", out)
	}
	if !strings.Contains(out, "abc1234") {
		t.Errorf("expected short SHA 'abc1234', got: %s", out)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("expected author 'alice', got: %s", out)
	}
	if !strings.Contains(out, "Fix login bug") {
		t.Errorf("expected commit message, got: %s", out)
	}
}

func TestText_PREvent(t *testing.T) {
	var buf bytes.Buffer
	f := NewText(&buf)

	ts := time.Date(2025, 6, 1, 14, 33, 12, 0, time.UTC)
	ev := event.Event{
		ID:        "pr-42-opened",
		Kind:      event.KindPR,
		Repo:      "owner/repo",
		Timestamp: ts,
		PR: &event.PRDetail{
			Number: 42,
			Title:  "Add metrics endpoint",
			Author: "bob",
			Action: "opened",
			URL:    "https://github.com/owner/repo/pull/42",
		},
	}

	if err := f.Format(ev); err != nil {
		t.Fatalf("Format: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "PR") {
		t.Errorf("expected PR label, got: %s", out)
	}
	if !strings.Contains(out, "#42") {
		t.Errorf("expected PR number, got: %s", out)
	}
	if !strings.Contains(out, "opened") {
		t.Errorf("expected action 'opened', got: %s", out)
	}
	if !strings.Contains(out, "bob") {
		t.Errorf("expected author 'bob', got: %s", out)
	}
}

func TestText_WorkflowEvent(t *testing.T) {
	var buf bytes.Buffer
	f := NewText(&buf)

	ts := time.Date(2025, 6, 1, 14, 35, 0, 0, time.UTC)
	ev := event.Event{
		ID:        "wf-100-completed-success",
		Kind:      event.KindWorkflow,
		Repo:      "owner/repo",
		Timestamp: ts,
		Workflow: &event.WorkflowDetail{
			Name:       "deploy",
			Status:     "completed",
			Conclusion: "success",
			Branch:     "main",
			Duration:   2*time.Minute + 34*time.Second,
			URL:        "https://github.com/owner/repo/actions/runs/100",
			RunID:      100,
		},
	}

	if err := f.Format(ev); err != nil {
		t.Fatalf("Format: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "CI") {
		t.Errorf("expected CI label, got: %s", out)
	}
	if !strings.Contains(out, "deploy") {
		t.Errorf("expected workflow name, got: %s", out)
	}
	if !strings.Contains(out, "completed/success") {
		t.Errorf("expected status/conclusion, got: %s", out)
	}
	if !strings.Contains(out, "2m34s") {
		t.Errorf("expected duration '2m34s', got: %s", out)
	}
}

func TestText_MultipleCommits(t *testing.T) {
	var buf bytes.Buffer
	f := NewText(&buf)

	ts := time.Date(2025, 6, 1, 14, 32, 5, 0, time.UTC)
	ev := event.Event{
		ID:        "push-2",
		Kind:      event.KindPush,
		Repo:      "owner/repo",
		Timestamp: ts,
		Push: &event.PushDetail{
			Branch: "main",
			Commits: []event.CommitInfo{
				{SHA: "abc1234567890", Author: "alice", Message: "First commit"},
				{SHA: "def5678901234", Author: "alice", Message: "Second commit"},
			},
		},
	}

	if err := f.Format(ev); err != nil {
		t.Fatalf("Format: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines for 2 commits, got %d: %v", len(lines), lines)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		max   int
		want  string
	}{
		{"short", 10, "short"},
		{"this is a long string", 10, "this is a…"},
		{"exact", 5, "exact"},
	}
	for _, tt := range tests {
		got := truncate(tt.input, tt.max)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"single line", "single line"},
		{"first\nsecond\nthird", "first"},
		{"", ""},
	}
	for _, tt := range tests {
		got := firstLine(tt.input)
		if got != tt.want {
			t.Errorf("firstLine(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		dur  time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{2*time.Minute + 34*time.Second, "2m34s"},
		{10 * time.Minute, "10m00s"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.dur)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.dur, got, tt.want)
		}
	}
}
