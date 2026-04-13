package poller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/go-github/v81/github"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/ghwatch/client"
)

// newTestClient creates a client.Client backed by a test server.
func newTestClient(t *testing.T, handler http.Handler) (*client.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	gh := github.NewClient(nil)
	u, _ := url.Parse(srv.URL + "/")
	gh.BaseURL = u
	return client.NewWithGitHubClient(gh, "testowner", "testrepo"), srv
}

func TestPRPoller_Poll_NewPR(t *testing.T) {
	now := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		prs := []*github.PullRequest{
			{
				Number:    github.Ptr(42),
				State:     github.Ptr("open"),
				Title:     github.Ptr("Add feature X"),
				HTMLURL:   github.Ptr("https://github.com/testowner/testrepo/pull/42"),
				Merged:    github.Ptr(false),
				UpdatedAt: &github.Timestamp{Time: now},
				User:      &github.User{Login: github.Ptr("alice")},
			},
		}
		json.NewEncoder(w).Encode(prs)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	p := NewPRPoller(c)
	events, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.PR.Number != 42 {
		t.Errorf("PR number = %d, want 42", ev.PR.Number)
	}
	if ev.PR.Action != "opened" {
		t.Errorf("action = %q, want opened", ev.PR.Action)
	}
	if ev.PR.Author != "alice" {
		t.Errorf("author = %q, want alice", ev.PR.Author)
	}
	if ev.Repo != "testowner/testrepo" {
		t.Errorf("repo = %q, want testowner/testrepo", ev.Repo)
	}
}

func TestPRPoller_Poll_MergedPR(t *testing.T) {
	now := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		prs := []*github.PullRequest{
			{
				Number:    github.Ptr(10),
				State:     github.Ptr("closed"),
				Title:     github.Ptr("Fix bug"),
				HTMLURL:   github.Ptr("https://github.com/testowner/testrepo/pull/10"),
				Merged:    github.Ptr(true),
				UpdatedAt: &github.Timestamp{Time: now},
				User:      &github.User{Login: github.Ptr("bob")},
			},
		}
		json.NewEncoder(w).Encode(prs)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	p := NewPRPoller(c)
	events, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].PR.Action != "merged" {
		t.Errorf("action = %q, want merged", events[0].PR.Action)
	}
}

func TestPRPoller_Poll_StateChange(t *testing.T) {
	now := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		prs := []*github.PullRequest{
			{
				Number:    github.Ptr(5),
				State:     github.Ptr("closed"),
				Title:     github.Ptr("Close me"),
				HTMLURL:   github.Ptr("https://github.com/testowner/testrepo/pull/5"),
				Merged:    github.Ptr(false),
				UpdatedAt: &github.Timestamp{Time: now},
				User:      &github.User{Login: github.Ptr("carol")},
			},
		}
		json.NewEncoder(w).Encode(prs)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	p := NewPRPoller(c)
	// Pre-seed known state as "open".
	p.knownPRs[5] = prState{State: "open", UpdatedAt: now.Add(-time.Hour), Merged: false}

	events, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].PR.Action != "closed" {
		t.Errorf("action = %q, want closed", events[0].PR.Action)
	}
}

func TestPRPoller_Poll_NoChange(t *testing.T) {
	now := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		prs := []*github.PullRequest{
			{
				Number:    github.Ptr(7),
				State:     github.Ptr("open"),
				Title:     github.Ptr("No change"),
				HTMLURL:   github.Ptr("https://github.com/testowner/testrepo/pull/7"),
				Merged:    github.Ptr(false),
				UpdatedAt: &github.Timestamp{Time: now},
				User:      &github.User{Login: github.Ptr("dave")},
			},
		}
		json.NewEncoder(w).Encode(prs)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	p := NewPRPoller(c)
	// Same state already known — should not emit event.
	p.knownPRs[7] = prState{State: "open", UpdatedAt: now, Merged: false}

	events, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0 (no state change)", len(events))
	}
}

func TestPRPoller_Poll_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	p := NewPRPoller(c)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := p.Poll(ctx)
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
}

func TestPRPoller_Poll_StateTracking(t *testing.T) {
	now := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		prs := []*github.PullRequest{
			{
				Number:    github.Ptr(99),
				State:     github.Ptr("open"),
				Title:     github.Ptr("Track me"),
				HTMLURL:   github.Ptr("https://github.com/testowner/testrepo/pull/99"),
				Merged:    github.Ptr(false),
				UpdatedAt: &github.Timestamp{Time: now},
				User:      &github.User{Login: github.Ptr("eve")},
			},
		}
		json.NewEncoder(w).Encode(prs)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	p := NewPRPoller(c)
	_, _ = p.Poll(context.Background())

	known := p.KnownPRs()
	if _, ok := known[99]; !ok {
		t.Error("PR 99 should be in known state after poll")
	}
	if known[99].State != "open" {
		t.Errorf("known state = %q, want open", known[99].State)
	}
}
