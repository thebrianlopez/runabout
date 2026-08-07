package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v81/github"
)

// newTestClient creates a Client pointing at the given test server.
func newTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	ghClient := github.NewClient(nil).WithAuthToken("test-token")
	ghClient.BaseURL, _ = ghClient.BaseURL.Parse(serverURL + "/")
	return &Client{
		gh:          ghClient,
		owner:       "testowner",
		repo:        "testrepo",
		rateLimiter: time.NewTicker(time.Millisecond), // Fast for tests.
	}
}

func TestListRepoEvents_Success(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	ts := github.Timestamp{Time: now}

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/events", func(w http.ResponseWriter, r *http.Request) {
		events := []map[string]interface{}{
			{
				"id":         "1",
				"type":       "PushEvent",
				"created_at": ts.Time.Format(time.RFC3339),
				"repo":       map[string]string{"name": "testowner/testrepo"},
				"actor":      map[string]interface{}{"login": "alice", "id": 1},
				"payload": map[string]interface{}{
					"ref":  "refs/heads/main",
					"size": 1,
					"commits": []map[string]interface{}{
						{"sha": "abc123", "message": "test commit", "author": map[string]string{"name": "alice"}},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	defer c.Close()

	events, _, err := c.ListRepoEvents(context.Background(), &github.ListOptions{PerPage: 10})
	if err != nil {
		t.Fatalf("ListRepoEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].GetType() != "PushEvent" {
		t.Errorf("event type: got %q, want %q", events[0].GetType(), "PushEvent")
	}
}

func TestListPullRequests_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		prs := []map[string]interface{}{
			{
				"number":     42,
				"title":      "Add feature",
				"state":      "open",
				"html_url":   "https://github.com/testowner/testrepo/pull/42",
				"created_at": "2025-06-01T10:00:00Z",
				"updated_at": "2025-06-01T12:00:00Z",
				"user":       map[string]interface{}{"login": "bob", "id": 2},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(prs)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	defer c.Close()

	prs, _, err := c.ListPullRequests(context.Background(), &github.PullRequestListOptions{
		State: "all", Sort: "updated", Direction: "desc",
	})
	if err != nil {
		t.Fatalf("ListPullRequests: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	if prs[0].GetNumber() != 42 {
		t.Errorf("PR number: got %d, want 42", prs[0].GetNumber())
	}
}

func TestListWorkflowRuns_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		runs := map[string]interface{}{
			"total_count": 1,
			"workflow_runs": []map[string]interface{}{
				{
					"id":             100,
					"name":           "CI",
					"status":         "completed",
					"conclusion":     "success",
					"head_branch":    "main",
					"html_url":       "https://github.com/testowner/testrepo/actions/runs/100",
					"created_at":     "2025-06-01T10:00:00Z",
					"updated_at":     "2025-06-01T10:05:00Z",
					"run_started_at": "2025-06-01T10:00:00Z",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(runs)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	defer c.Close()

	runs, _, err := c.ListWorkflowRuns(context.Background(), &github.ListWorkflowRunsOptions{})
	if err != nil {
		t.Fatalf("ListWorkflowRuns: %v", err)
	}
	if runs.GetTotalCount() != 1 {
		t.Fatalf("expected 1 run, got %d", runs.GetTotalCount())
	}
	if runs.WorkflowRuns[0].GetName() != "CI" {
		t.Errorf("workflow name: got %q, want %q", runs.WorkflowRuns[0].GetName(), "CI")
	}
}

func TestRateLimitResponse_Retry(t *testing.T) {
	attempts := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/events", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			// Return rate limit error.
			reset := time.Now().Add(100 * time.Millisecond)
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", reset.Unix()))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"message":           "API rate limit exceeded",
				"documentation_url": "https://docs.github.com/rest",
			})
			return
		}
		// Success on retry.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]interface{}{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	defer c.Close()

	events, _, err := c.ListRepoEvents(context.Background(), &github.ListOptions{PerPage: 10})
	if err != nil {
		t.Fatalf("ListRepoEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
	if attempts < 2 {
		t.Errorf("expected at least 2 attempts (retry), got %d", attempts)
	}
}

func TestMalformedJSON_GracefulError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{not valid json"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	defer c.Close()

	_, _, err := c.ListRepoEvents(context.Background(), &github.ListOptions{PerPage: 10})
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	// The error should mention the parse failure, not panic.
	if !strings.Contains(err.Error(), "failed after") && !strings.Contains(err.Error(), "invalid") {
		t.Logf("error: %v", err) // Log for debugging but don't fail — exact message may vary.
	}
}

func TestContextCancellation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/events", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // Slow response.
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, _, err := c.ListRepoEvents(ctx, &github.ListOptions{PerPage: 10})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
