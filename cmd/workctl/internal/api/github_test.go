package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-github/v81/github"
)

func TestNewGitHubClient(t *testing.T) {
	tests := []struct {
		name      string
		token     string
		wantError bool
	}{
		{
			name:      "valid token",
			token:     "ghp_test_token_1234567890",
			wantError: false,
		},
		{
			name:      "empty token",
			token:     "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewGitHubClient(tt.token)

			if tt.wantError {
				if err == nil {
					t.Errorf("NewGitHubClient() expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("NewGitHubClient() unexpected error: %v", err)
				return
			}

			if client == nil {
				t.Errorf("NewGitHubClient() returned nil client")
			}

			if client.Client == nil {
				t.Errorf("NewGitHubClient() returned client with nil GitHub client")
			}

			if client.rateLimiter == nil {
				t.Errorf("NewGitHubClient() returned client with nil rate limiter")
			}
		})
	}
}

// newTestGitHubClient creates a GitHubClient pointed at an httptest server.
func newTestGitHubClient(t *testing.T, routes map[string]testRoute) (*GitHubClient, func()) {
	t.Helper()

	mux := http.NewServeMux()
	for path, route := range routes {
		route := route
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(route.statusCode)
			w.Write([]byte(route.body))
		})
	}

	server := httptest.NewServer(mux)

	client := github.NewClient(nil)
	client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")

	gh := &GitHubClient{
		Client:      client,
		token:       "test-token",
		rateLimiter: time.NewTicker(time.Nanosecond),
	}

	return gh, server.Close
}

func TestGetGitHubIssue_Issue(t *testing.T) {
	gh, cleanup := newTestGitHubClient(t, map[string]testRoute{
		"/repos/myorg/myrepo/issues/42": {
			method:     "GET",
			statusCode: 200,
			body:       `{"number": 42, "title": "Add metrics endpoint", "body": "We need /metrics for Prometheus.", "state": "open", "html_url": "https://github.com/myorg/myrepo/issues/42"}`,
		},
	})
	defer cleanup()

	info, err := gh.GetGitHubIssue(context.Background(), "myorg", "myrepo", 42)
	if err != nil {
		t.Fatalf("GetGitHubIssue error: %v", err)
	}

	if info.Key != "myorg/myrepo#42" {
		t.Errorf("Key = %q, want %q", info.Key, "myorg/myrepo#42")
	}
	if info.Summary != "Add metrics endpoint" {
		t.Errorf("Summary = %q, want %q", info.Summary, "Add metrics endpoint")
	}
	if info.Status != "open" {
		t.Errorf("Status = %q, want %q", info.Status, "open")
	}
	if info.Type != "Issue" {
		t.Errorf("Type = %q, want %q", info.Type, "Issue")
	}
	if info.URL != "https://github.com/myorg/myrepo/issues/42" {
		t.Errorf("URL = %q, want %q", info.URL, "https://github.com/myorg/myrepo/issues/42")
	}
	if info.Description == "" {
		t.Error("Description should not be empty")
	}
}

func TestGetGitHubIssue_PullRequest(t *testing.T) {
	gh, cleanup := newTestGitHubClient(t, map[string]testRoute{
		"/repos/myorg/myrepo/issues/10": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"number": 10,
				"title": "Fix auth bug",
				"body": "Fixes login timeout.",
				"state": "closed",
				"html_url": "https://github.com/myorg/myrepo/pull/10",
				"pull_request": {"url": "https://api.github.com/repos/myorg/myrepo/pulls/10"}
			}`,
		},
	})
	defer cleanup()

	info, err := gh.GetGitHubIssue(context.Background(), "myorg", "myrepo", 10)
	if err != nil {
		t.Fatalf("GetGitHubIssue error: %v", err)
	}

	if info.Type != "Pull Request" {
		t.Errorf("Type = %q, want %q", info.Type, "Pull Request")
	}
	if info.Status != "closed" {
		t.Errorf("Status = %q, want %q", info.Status, "closed")
	}
}

func TestGetGitHubIssue_NotFound(t *testing.T) {
	gh, cleanup := newTestGitHubClient(t, map[string]testRoute{
		"/repos/myorg/myrepo/issues/999": {
			method:     "GET",
			statusCode: 404,
			body:       `{"message": "Not Found"}`,
		},
	})
	defer cleanup()

	_, err := gh.GetGitHubIssue(context.Background(), "myorg", "myrepo", 999)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}
