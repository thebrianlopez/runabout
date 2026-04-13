package poller

import (
	"testing"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/ghwatch/event"
)

func TestPushPoller_SetLastSeen(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	p := &PushPoller{lastSeen: ts}
	if !p.LastSeen().Equal(ts) {
		t.Errorf("LastSeen: got %v, want %v", p.LastSeen(), ts)
	}

	ts2 := time.Date(2025, 6, 2, 12, 0, 0, 0, time.UTC)
	p.SetLastSeen(ts2)
	if !p.LastSeen().Equal(ts2) {
		t.Errorf("LastSeen after set: got %v, want %v", p.LastSeen(), ts2)
	}
}

func TestPushPoller_Name(t *testing.T) {
	p := &PushPoller{}
	if p.Name() != "push" {
		t.Errorf("Name: got %q, want %q", p.Name(), "push")
	}
}

func TestPushDetail_CommitInfo(t *testing.T) {
	ev := event.Event{
		ID:   "test-1",
		Kind: event.KindPush,
		Push: &event.PushDetail{
			Branch: "main",
			Commits: []event.CommitInfo{
				{SHA: "abc1234567890", Author: "alice", Message: "Fix bug"},
				{SHA: "def5678901234", Author: "bob", Message: "Update deps"},
			},
		},
	}

	if ev.Push.Branch != "main" {
		t.Errorf("Branch: got %q, want %q", ev.Push.Branch, "main")
	}
	if len(ev.Push.Commits) != 2 {
		t.Fatalf("Commits: got %d, want 2", len(ev.Push.Commits))
	}
	if ev.Push.Commits[0].SHA != "abc1234567890" {
		t.Errorf("Commit SHA: got %q, want %q", ev.Push.Commits[0].SHA, "abc1234567890")
	}
}
