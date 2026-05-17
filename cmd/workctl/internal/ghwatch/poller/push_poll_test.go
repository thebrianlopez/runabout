package poller

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-github/v81/github"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ghwatch/event"
)

func TestPushPoller_Poll_FiltersPushEvents(t *testing.T) {
	now := time.Now()
	since := now.Add(-time.Hour)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/events", func(w http.ResponseWriter, r *http.Request) {
		pushPayload, _ := json.Marshal(&github.PushEvent{
			Ref:  github.Ptr("refs/heads/main"),
			Head: github.Ptr("abc123"),
			Size: github.Ptr(1),
			Commits: []*github.HeadCommit{
				{
					SHA:     github.Ptr("abc123"),
					Message: github.Ptr("fix: resolve issue"),
					Author:  &github.CommitAuthor{Name: github.Ptr("alice")},
				},
			},
		})

		events := []*github.Event{
			{
				ID:         github.Ptr("evt-1"),
				Type:       github.Ptr("PushEvent"),
				CreatedAt:  &github.Timestamp{Time: now},
				RawPayload: (*json.RawMessage)(&pushPayload),
			},
			{
				ID:        github.Ptr("evt-2"),
				Type:      github.Ptr("WatchEvent"), // should be filtered out
				CreatedAt: &github.Timestamp{Time: now},
			},
		}
		json.NewEncoder(w).Encode(events)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	p := NewPushPoller(c, since)
	events, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (only PushEvent)", len(events))
	}
	if events[0].Kind != event.KindPush {
		t.Errorf("kind = %q, want push", events[0].Kind)
	}
	if events[0].Push.Branch != "main" {
		t.Errorf("branch = %q, want main", events[0].Push.Branch)
	}
	if len(events[0].Push.Commits) != 1 {
		t.Fatalf("commits = %d, want 1", len(events[0].Push.Commits))
	}
	if events[0].Push.Commits[0].Author != "alice" {
		t.Errorf("author = %q, want alice", events[0].Push.Commits[0].Author)
	}
}

func TestPushPoller_Poll_SkipsOldEvents(t *testing.T) {
	now := time.Now()
	since := now.Add(time.Hour) // everything is "old"

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/events", func(w http.ResponseWriter, r *http.Request) {
		pushPayload, _ := json.Marshal(&github.PushEvent{
			Ref:  github.Ptr("refs/heads/main"),
			Head: github.Ptr("old123"),
			Size: github.Ptr(1),
		})
		events := []*github.Event{
			{
				ID:         github.Ptr("old-evt"),
				Type:       github.Ptr("PushEvent"),
				CreatedAt:  &github.Timestamp{Time: now},
				RawPayload: (*json.RawMessage)(&pushPayload),
			},
		}
		json.NewEncoder(w).Encode(events)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	p := NewPushPoller(c, since)
	events, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0 (all events before lastSeen)", len(events))
	}
}

func TestPushPoller_Poll_UpdatesLastSeen(t *testing.T) {
	now := time.Now()
	since := now.Add(-2 * time.Hour)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/events", func(w http.ResponseWriter, r *http.Request) {
		pushPayload, _ := json.Marshal(&github.PushEvent{
			Ref:  github.Ptr("refs/heads/main"),
			Head: github.Ptr("new123"),
			Size: github.Ptr(1),
			Commits: []*github.HeadCommit{
				{SHA: github.Ptr("new123"), Message: github.Ptr("latest"), Author: &github.CommitAuthor{Name: github.Ptr("bob")}},
			},
		})
		events := []*github.Event{
			{
				ID:         github.Ptr("evt-new"),
				Type:       github.Ptr("PushEvent"),
				CreatedAt:  &github.Timestamp{Time: now},
				RawPayload: (*json.RawMessage)(&pushPayload),
			},
		}
		json.NewEncoder(w).Encode(events)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	p := NewPushPoller(c, since)
	_, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}

	if p.LastSeen().Before(since) {
		t.Errorf("lastSeen was not updated: got %v, was %v", p.LastSeen(), since)
	}
}

func TestPushPoller_Poll_HydrateCommit(t *testing.T) {
	now := time.Now()
	since := now.Add(-time.Hour)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/events", func(w http.ResponseWriter, r *http.Request) {
		// Push event with NO inline commits — triggers hydration.
		pushPayload, _ := json.Marshal(&github.PushEvent{
			Ref:     github.Ptr("refs/heads/main"),
			Head:    github.Ptr("hydrate123"),
			Size:    github.Ptr(1),
			Commits: []*github.HeadCommit{}, // empty → triggers hydrate
		})
		events := []*github.Event{
			{
				ID:         github.Ptr("evt-hydrate"),
				Type:       github.Ptr("PushEvent"),
				CreatedAt:  &github.Timestamp{Time: now},
				RawPayload: (*json.RawMessage)(&pushPayload),
			},
		}
		json.NewEncoder(w).Encode(events)
	})

	// Hydration endpoint: GET /repos/owner/repo/commits/:sha
	mux.HandleFunc("/repos/testowner/testrepo/commits/hydrate123", func(w http.ResponseWriter, r *http.Request) {
		commit := &github.RepositoryCommit{
			SHA: github.Ptr("hydrate123"),
			Commit: &github.Commit{
				Message: github.Ptr("hydrated commit"),
				Author:  &github.CommitAuthor{Name: github.Ptr("carol")},
			},
			Files: []*github.CommitFile{
				{Filename: github.Ptr("main.go"), Status: github.Ptr("modified")},
				{Filename: github.Ptr("new.go"), Status: github.Ptr("added")},
			},
		}
		json.NewEncoder(w).Encode(commit)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	p := NewPushPoller(c, since)
	events, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	commits := events[0].Push.Commits
	if len(commits) != 1 {
		t.Fatalf("commits = %d, want 1 (hydrated)", len(commits))
	}
	if commits[0].Author != "carol" {
		t.Errorf("author = %q, want carol", commits[0].Author)
	}
	if len(commits[0].Modified) != 1 || commits[0].Modified[0] != "main.go" {
		t.Errorf("modified = %v, want [main.go]", commits[0].Modified)
	}
	if len(commits[0].Added) != 1 || commits[0].Added[0] != "new.go" {
		t.Errorf("added = %v, want [new.go]", commits[0].Added)
	}
}

func TestPushPoller_Poll_HydrateError(t *testing.T) {
	now := time.Now()
	since := now.Add(-time.Hour)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/events", func(w http.ResponseWriter, r *http.Request) {
		pushPayload, _ := json.Marshal(&github.PushEvent{
			Ref:     github.Ptr("refs/heads/main"),
			Head:    github.Ptr("err1234567890"),
			Size:    github.Ptr(1),
			Commits: []*github.HeadCommit{},
		})
		events := []*github.Event{
			{
				ID:         github.Ptr("evt-err"),
				Type:       github.Ptr("PushEvent"),
				CreatedAt:  &github.Timestamp{Time: now},
				RawPayload: (*json.RawMessage)(&pushPayload),
			},
		}
		json.NewEncoder(w).Encode(events)
	})

	// Hydration endpoint returns error.
	mux.HandleFunc("/repos/testowner/testrepo/commits/err1234567890", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	p := NewPushPoller(c, since)
	// Use a short timeout since the client will retry on 404.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	events, err := p.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll should not fail on hydrate error: %v", err)
	}
	// Event should still be emitted, just without commits.
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if len(events[0].Push.Commits) != 0 {
		t.Errorf("commits = %d, want 0 (hydration failed gracefully)", len(events[0].Push.Commits))
	}
}

func TestPushPoller_Poll_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/events", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	p := NewPushPoller(c, time.Now().Add(-time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := p.Poll(ctx)
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
}
