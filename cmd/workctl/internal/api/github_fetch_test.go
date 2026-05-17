package api

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

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// --- Events API tests ---

func TestFetchViaEventsAPI(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inRange := now.Add(-24 * time.Hour)
	outOfRange := now.Add(-72 * time.Hour)

	eventJSON := func(id, typ string, ts time.Time, repo string) string {
		return fmt.Sprintf(`{
			"id": %q,
			"type": %q,
			"actor": {"login": "testuser"},
			"repo": {"name": %q},
			"created_at": %q,
			"public": true,
			"payload": {}
		}`, id, typ, repo, ts.Format(time.RFC3339))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/users/testuser/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		events := fmt.Sprintf("[%s,%s]",
			eventJSON("1", "WatchEvent", inRange, "org/repo1"),
			eventJSON("2", "WatchEvent", outOfRange, "org/repo2"),
		)
		w.Write([]byte(events))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := github.NewClient(nil)
	client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")
	gh := &GitHubClient{
		Client:      client,
		token:       "test-token",
		rateLimiter: time.NewTicker(time.Nanosecond),
	}

	start := now.Add(-48 * time.Hour)
	end := now.Add(time.Hour)

	activities, err := gh.fetchViaEventsAPI(context.Background(), "testuser", start, end)
	if err != nil {
		t.Fatalf("fetchViaEventsAPI error: %v", err)
	}

	// Only the in-range event should be included
	if len(activities) != 1 {
		t.Fatalf("got %d activities, want 1", len(activities))
	}
	if activities[0].EventID != "1" {
		t.Errorf("EventID = %q, want %q", activities[0].EventID, "1")
	}
	if activities[0].EventType != "WatchEvent" {
		t.Errorf("EventType = %q, want %q", activities[0].EventType, "WatchEvent")
	}
}

func TestFetchViaEventsAPI_StopsAtOldEvents(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inRange := now.Add(-12 * time.Hour)
	beforeRange := now.Add(-96 * time.Hour) // clearly before start

	mux := http.NewServeMux()
	mux.HandleFunc("/users/testuser/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return one in-range and one before-range event.
		// The before-range event should trigger early pagination stop.
		w.Write([]byte(fmt.Sprintf(`[
			{
				"id": "1", "type": "WatchEvent",
				"actor": {"login": "testuser"},
				"repo": {"name": "org/repo"},
				"created_at": %q, "public": true, "payload": {}
			},
			{
				"id": "2", "type": "WatchEvent",
				"actor": {"login": "testuser"},
				"repo": {"name": "org/repo2"},
				"created_at": %q, "public": true, "payload": {}
			}
		]`, inRange.Format(time.RFC3339), beforeRange.Format(time.RFC3339))))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := github.NewClient(nil)
	client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")
	gh := &GitHubClient{
		Client:      client,
		token:       "test-token",
		rateLimiter: time.NewTicker(time.Nanosecond),
	}

	start := now.Add(-48 * time.Hour)
	end := now.Add(time.Hour)

	activities, err := gh.fetchViaEventsAPI(context.Background(), "testuser", start, end)
	if err != nil {
		t.Fatalf("fetchViaEventsAPI error: %v", err)
	}

	// Only the in-range event should be returned; the old one filtered out
	if len(activities) != 1 {
		t.Fatalf("got %d activities, want 1 (old event filtered)", len(activities))
	}
}

func TestFetchViaEventsAPI_Empty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/users/testuser/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := github.NewClient(nil)
	client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")
	gh := &GitHubClient{
		Client:      client,
		token:       "test-token",
		rateLimiter: time.NewTicker(time.Nanosecond),
	}

	activities, err := gh.fetchViaEventsAPI(context.Background(), "testuser", time.Now().Add(-24*time.Hour), time.Now())
	if err != nil {
		t.Fatalf("fetchViaEventsAPI error: %v", err)
	}
	if len(activities) != 0 {
		t.Errorf("got %d activities, want 0", len(activities))
	}
}

// --- convertEvent / generateDescriptionAndURL tests ---

func TestConvertEvent_PushEvent_HTTPTest(t *testing.T) {
	gh := &GitHubClient{}
	now := time.Now()
	ts := &github.Timestamp{Time: now}

	rawPayload := json.RawMessage(`{
		"ref": "refs/heads/main",
		"commits": [{"sha": "abc123", "message": "fix: stuff"}]
	}`)

	event := &github.Event{
		ID:         github.Ptr("evt-1"),
		Type:       github.Ptr("PushEvent"),
		Actor:      &github.User{Login: github.Ptr("testuser")},
		Repo:       &github.Repository{Name: github.Ptr("org/repo")},
		CreatedAt:  ts,
		Public:     github.Ptr(true),
		RawPayload: &rawPayload,
	}

	activity, err := gh.convertEvent(event)
	if err != nil {
		t.Fatalf("convertEvent error: %v", err)
	}

	if activity.EventType != "PushEvent" {
		t.Errorf("EventType = %q, want %q", activity.EventType, "PushEvent")
	}
	if activity.ActorLogin != "testuser" {
		t.Errorf("ActorLogin = %q, want %q", activity.ActorLogin, "testuser")
	}
	if activity.Repository != "org/repo" {
		t.Errorf("Repository = %q, want %q", activity.Repository, "org/repo")
	}
}

func TestConvertEvent_PullRequestEvent(t *testing.T) {
	gh := &GitHubClient{}
	now := time.Now()
	ts := &github.Timestamp{Time: now}

	rawPayload := json.RawMessage(`{
		"action": "opened",
		"number": 42,
		"pull_request": {
			"number": 42,
			"title": "Add feature X",
			"html_url": "https://github.com/org/repo/pull/42"
		}
	}`)

	event := &github.Event{
		ID:         github.Ptr("evt-2"),
		Type:       github.Ptr("PullRequestEvent"),
		Actor:      &github.User{Login: github.Ptr("testuser")},
		Repo:       &github.Repository{Name: github.Ptr("org/repo")},
		CreatedAt:  ts,
		Public:     github.Ptr(true),
		RawPayload: &rawPayload,
	}

	activity, err := gh.convertEvent(event)
	if err != nil {
		t.Fatalf("convertEvent error: %v", err)
	}

	if activity.EventType != "PullRequestEvent" {
		t.Errorf("EventType = %q, want %q", activity.EventType, "PullRequestEvent")
	}
	if activity.URL != "https://github.com/org/repo/pull/42" {
		t.Errorf("URL = %q, want PR URL", activity.URL)
	}
}

func TestConvertEvent_IssuesEvent(t *testing.T) {
	gh := &GitHubClient{}
	now := time.Now()
	ts := &github.Timestamp{Time: now}

	rawPayload := json.RawMessage(`{
		"action": "opened",
		"issue": {
			"number": 7,
			"title": "Bug report",
			"html_url": "https://github.com/org/repo/issues/7"
		}
	}`)

	event := &github.Event{
		ID:         github.Ptr("evt-3"),
		Type:       github.Ptr("IssuesEvent"),
		Actor:      &github.User{Login: github.Ptr("testuser")},
		Repo:       &github.Repository{Name: github.Ptr("org/repo")},
		CreatedAt:  ts,
		Public:     github.Ptr(true),
		RawPayload: &rawPayload,
	}

	activity, err := gh.convertEvent(event)
	if err != nil {
		t.Fatalf("convertEvent error: %v", err)
	}

	if activity.EventType != "IssuesEvent" {
		t.Errorf("EventType = %q, want %q", activity.EventType, "IssuesEvent")
	}
}

func TestConvertEvent_CreateEvent(t *testing.T) {
	gh := &GitHubClient{}
	now := time.Now()
	ts := &github.Timestamp{Time: now}

	rawPayload := json.RawMessage(`{
		"ref_type": "branch",
		"ref": "feature/awesome"
	}`)

	event := &github.Event{
		ID:         github.Ptr("evt-4"),
		Type:       github.Ptr("CreateEvent"),
		Actor:      &github.User{Login: github.Ptr("testuser")},
		Repo:       &github.Repository{Name: github.Ptr("org/repo")},
		CreatedAt:  ts,
		Public:     github.Ptr(true),
		RawPayload: &rawPayload,
	}

	activity, err := gh.convertEvent(event)
	if err != nil {
		t.Fatalf("convertEvent error: %v", err)
	}

	if activity.EventType != "CreateEvent" {
		t.Errorf("EventType = %q, want %q", activity.EventType, "CreateEvent")
	}
	if activity.URL != "https://github.com/org/repo/tree/feature/awesome" {
		t.Errorf("URL = %q, want branch URL", activity.URL)
	}
}

// --- Search API tests ---

func TestFetchViaSearchAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(q, "type:pr") {
			w.Write([]byte(`{
				"total_count": 1,
				"items": [{
					"id": 100,
					"number": 5,
					"title": "Add auth",
					"state": "closed",
					"html_url": "https://github.com/org/repo/pull/5",
					"user": {"login": "testuser"},
					"created_at": "2025-06-15T10:00:00Z",
					"repository_url": "https://api.github.com/repos/org/repo",
					"pull_request": {"url": "https://api.github.com/repos/org/repo/pulls/5"}
				}]
			}`))
		} else {
			w.Write([]byte(`{
				"total_count": 1,
				"items": [{
					"id": 200,
					"number": 10,
					"title": "Bug: crash on login",
					"state": "open",
					"html_url": "https://github.com/org/repo/issues/10",
					"user": {"login": "testuser"},
					"created_at": "2025-06-20T10:00:00Z",
					"repository_url": "https://api.github.com/repos/org/repo"
				}]
			}`))
		}
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := github.NewClient(nil)
	client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")
	gh := &GitHubClient{
		Client:      client,
		token:       "test-token",
		rateLimiter: time.NewTicker(time.Nanosecond),
	}

	start := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC)

	activities, err := gh.fetchViaSearchAPI(context.Background(), "testuser", start, end)
	if err != nil {
		t.Fatalf("fetchViaSearchAPI error: %v", err)
	}

	if len(activities) != 2 {
		t.Fatalf("got %d activities, want 2", len(activities))
	}

	// Check PR activity
	var prFound, issueFound bool
	for _, a := range activities {
		if a.EventType == "PullRequestEvent" {
			prFound = true
			if a.Repository != "org/repo" {
				t.Errorf("PR Repository = %q, want %q", a.Repository, "org/repo")
			}
		}
		if a.EventType == "IssuesEvent" {
			issueFound = true
		}
	}
	if !prFound {
		t.Error("expected PullRequestEvent activity")
	}
	if !issueFound {
		t.Error("expected IssuesEvent activity")
	}
}

func TestSearchPullRequests(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(q, "type:pr") {
			t.Errorf("query missing type:pr: %q", q)
		}
		if !strings.Contains(q, "author:testuser") {
			t.Errorf("query missing author: %q", q)
		}
		w.Write([]byte(`{"total_count": 0, "items": []}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := github.NewClient(nil)
	client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")
	gh := &GitHubClient{
		Client:      client,
		token:       "test-token",
		rateLimiter: time.NewTicker(time.Nanosecond),
	}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC)

	results, err := gh.searchPullRequests(context.Background(), "testuser", start, end)
	if err != nil {
		t.Fatalf("searchPullRequests error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestSearchIssues(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(q, "type:issue") {
			t.Errorf("query missing type:issue: %q", q)
		}
		w.Write([]byte(`{"total_count": 0, "items": []}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := github.NewClient(nil)
	client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")
	gh := &GitHubClient{
		Client:      client,
		token:       "test-token",
		rateLimiter: time.NewTicker(time.Nanosecond),
	}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC)

	results, err := gh.searchIssues(context.Background(), "testuser", start, end)
	if err != nil {
		t.Fatalf("searchIssues error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

// --- GraphQL API tests ---

// Note: fetchViaGraphQLAPI and queryContributionStats cannot be tested with httptest
// because queryContributionStats hardcodes "https://api.github.com/graphql".
// The downstream logic (createSyntheticActivities) is tested thoroughly below.
// A future refactor could inject the GraphQL URL to enable full integration tests.

func TestCreateSyntheticActivities_AllTypes(t *testing.T) {
	gh := &GitHubClient{}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	stats := &ContributionStats{
		TotalCommits:            42,
		TotalIssues:             5,
		TotalPRs:                12,
		TotalReviews:            8,
		RestrictedContributions: 3,
		Username:                "testuser",
	}

	activities := gh.createSyntheticActivities(stats, "testuser", start, end)

	if len(activities) != 5 {
		t.Fatalf("got %d activities, want 5", len(activities))
	}

	typeMap := make(map[string]string)
	for _, a := range activities {
		typeMap[a.EventType] = a.Description
	}

	if _, ok := typeMap["AggregateCommits"]; !ok {
		t.Error("missing AggregateCommits activity")
	}
	if _, ok := typeMap["AggregatePullRequests"]; !ok {
		t.Error("missing AggregatePullRequests activity")
	}
	if _, ok := typeMap["AggregatePullRequestReviews"]; !ok {
		t.Error("missing AggregatePullRequestReviews activity")
	}
	if _, ok := typeMap["AggregateIssues"]; !ok {
		t.Error("missing AggregateIssues activity")
	}
	if _, ok := typeMap["AggregateRestrictedContributions"]; !ok {
		t.Error("missing AggregateRestrictedContributions activity")
	}

	// Verify descriptions contain counts
	if !strings.Contains(typeMap["AggregateCommits"], "42 commits") {
		t.Errorf("commits description = %q, want '42 commits'", typeMap["AggregateCommits"])
	}
}

// --- Commits API tests ---

func TestFetchViaCommitsAPI(t *testing.T) {
	commitTime := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/myrepo/commits", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`[{
			"sha": "abc123def456",
			"commit": {
				"message": "Fix bug in auth flow",
				"author": {"date": %q}
			},
			"author": {"login": "testuser"},
			"html_url": "https://github.com/org/myrepo/commit/abc123def456"
		}]`, commitTime.Format(time.RFC3339))))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := github.NewClient(nil)
	client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")
	gh := &GitHubClient{
		Client:      client,
		token:       "test-token",
		rateLimiter: time.NewTicker(time.Nanosecond),
	}

	start := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC)

	activities, err := gh.fetchViaCommitsAPI(context.Background(), "testuser", []string{"org/myrepo"}, start, end, false)
	if err != nil {
		t.Fatalf("fetchViaCommitsAPI error: %v", err)
	}

	if len(activities) != 1 {
		t.Fatalf("got %d activities, want 1", len(activities))
	}

	a := activities[0]
	if a.EventType != "CommitEvent" {
		t.Errorf("EventType = %q, want %q", a.EventType, "CommitEvent")
	}
	if a.CommitSHA != "abc123def456" {
		t.Errorf("CommitSHA = %q, want %q", a.CommitSHA, "abc123def456")
	}
	if a.CommitMessage != "Fix bug in auth flow" {
		t.Errorf("CommitMessage = %q, want %q", a.CommitMessage, "Fix bug in auth flow")
	}
	if a.ActorLogin != "testuser" {
		t.Errorf("ActorLogin = %q, want %q", a.ActorLogin, "testuser")
	}
}

func TestFetchViaCommitsAPI_InvalidRepo(t *testing.T) {
	server := httptest.NewServer(http.NewServeMux())
	defer server.Close()

	client := github.NewClient(nil)
	client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")
	gh := &GitHubClient{
		Client:      client,
		token:       "test-token",
		rateLimiter: time.NewTicker(time.Nanosecond),
	}

	start := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC)

	// Invalid repo slug (no slash) — should be skipped, not error
	activities, err := gh.fetchViaCommitsAPI(context.Background(), "testuser", []string{"noslash"}, start, end, false)
	if err != nil {
		t.Fatalf("expected no error for invalid repo, got: %v", err)
	}
	if len(activities) != 0 {
		t.Errorf("got %d activities, want 0 for invalid repo", len(activities))
	}
}

func TestListCommitsForRepo(t *testing.T) {
	commitTime := time.Date(2025, 6, 10, 14, 30, 0, 0, time.UTC)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/repo/commits", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`[
			{
				"sha": "aaa111",
				"commit": {
					"message": "First commit\n\nDetailed description",
					"author": {"date": %q}
				},
				"author": {"login": "dev1"}
			},
			{
				"sha": "bbb222",
				"commit": {
					"message": "Second commit",
					"author": {"date": %q}
				},
				"author": {"login": "dev1"}
			}
		]`, commitTime.Format(time.RFC3339), commitTime.Add(time.Hour).Format(time.RFC3339))))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := github.NewClient(nil)
	client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")
	gh := &GitHubClient{
		Client:      client,
		token:       "test-token",
		rateLimiter: time.NewTicker(time.Nanosecond),
	}

	start := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC)

	activities, err := gh.listCommitsForRepo(context.Background(), "dev1", "org", "repo", start, end, false)
	if err != nil {
		t.Fatalf("listCommitsForRepo error: %v", err)
	}

	if len(activities) != 2 {
		t.Fatalf("got %d activities, want 2", len(activities))
	}

	// First commit should have truncated message (only first line)
	if activities[0].CommitMessage != "First commit" {
		t.Errorf("CommitMessage = %q, want first line only", activities[0].CommitMessage)
	}
	if activities[0].Repository != "org/repo" {
		t.Errorf("Repository = %q, want %q", activities[0].Repository, "org/repo")
	}
}

func TestEnrichCommit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/repo/commits/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"sha": "abc123",
			"stats": {"additions": 42, "deletions": 7},
			"files": [
				{"filename": "main.go"},
				{"filename": "main_test.go"}
			]
		}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := github.NewClient(nil)
	client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")
	gh := &GitHubClient{
		Client:      client,
		token:       "test-token",
		rateLimiter: time.NewTicker(time.Nanosecond),
	}

	activity := models.GitHubActivity{
		CommitSHA: "abc123",
	}

	gh.enrichCommit(context.Background(), &activity, "org", "repo")

	if activity.LinesAdded != 42 {
		t.Errorf("LinesAdded = %d, want 42", activity.LinesAdded)
	}
	if activity.LinesRemoved != 7 {
		t.Errorf("LinesRemoved = %d, want 7", activity.LinesRemoved)
	}
	if len(activity.FilesChanged) != 2 {
		t.Errorf("FilesChanged = %d, want 2", len(activity.FilesChanged))
	}
	if !activity.Enriched {
		t.Error("expected Enriched = true")
	}
}

func TestEnrichCommit_NoStats(t *testing.T) {
	// Test enrichment when commit exists but has no stats/files
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/repo/commits/nostat12", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"sha": "nostat12",
			"files": []
		}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := github.NewClient(nil)
	client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")
	gh := &GitHubClient{
		Client:      client,
		token:       "test-token",
		rateLimiter: time.NewTicker(time.Nanosecond),
	}

	activity := models.GitHubActivity{
		CommitSHA: "nostat12",
	}

	gh.enrichCommit(context.Background(), &activity, "org", "repo")

	// Should succeed but with zero stats
	if activity.LinesAdded != 0 {
		t.Errorf("LinesAdded = %d, want 0", activity.LinesAdded)
	}
	if len(activity.FilesChanged) != 0 {
		t.Errorf("FilesChanged = %d, want 0", len(activity.FilesChanged))
	}
	if !activity.Enriched {
		t.Error("expected Enriched = true even with no stats")
	}
}

// --- convertCommitToActivity tests ---

func TestConvertCommitToActivity(t *testing.T) {
	gh := &GitHubClient{}
	commitTime := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	ts := &github.Timestamp{Time: commitTime}

	commit := &github.RepositoryCommit{
		SHA: github.Ptr("deadbeef123456"),
		Commit: &github.Commit{
			Message: github.Ptr("Add validation\n\nFull description of changes"),
			Author: &github.CommitAuthor{
				Date: ts,
			},
		},
		Author: &github.User{Login: github.Ptr("myuser")},
	}

	activity := gh.convertCommitToActivity(commit, "org", "repo", "fallback")

	if activity.EventType != "CommitEvent" {
		t.Errorf("EventType = %q, want %q", activity.EventType, "CommitEvent")
	}
	if activity.CommitSHA != "deadbeef123456" {
		t.Errorf("CommitSHA = %q, want %q", activity.CommitSHA, "deadbeef123456")
	}
	if activity.CommitMessage != "Add validation" {
		t.Errorf("CommitMessage = %q, want first line", activity.CommitMessage)
	}
	if activity.ActorLogin != "myuser" {
		t.Errorf("ActorLogin = %q, want %q (from commit author, not fallback)", activity.ActorLogin, "myuser")
	}
	if activity.Repository != "org/repo" {
		t.Errorf("Repository = %q, want %q", activity.Repository, "org/repo")
	}
}

func TestConvertCommitToActivity_FallbackUser(t *testing.T) {
	gh := &GitHubClient{}
	commitTime := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	ts := &github.Timestamp{Time: commitTime}

	commit := &github.RepositoryCommit{
		SHA: github.Ptr("aabbccdd"),
		Commit: &github.Commit{
			Message: github.Ptr("No author login"),
			Author: &github.CommitAuthor{
				Date: ts,
			},
		},
		// Author is nil — should fall back to provided username
	}

	activity := gh.convertCommitToActivity(commit, "org", "repo", "fallbackuser")

	if activity.ActorLogin != "fallbackuser" {
		t.Errorf("ActorLogin = %q, want %q (fallback)", activity.ActorLogin, "fallbackuser")
	}
}

// --- createSyntheticActivities tests ---

func TestCreateSyntheticActivities(t *testing.T) {
	gh := &GitHubClient{}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	stats := &ContributionStats{
		TotalCommits:            50,
		TotalIssues:             0, // zero — should not create activity
		TotalPRs:                10,
		TotalReviews:            0, // zero
		RestrictedContributions: 3,
		Username:                "testuser",
	}

	activities := gh.createSyntheticActivities(stats, "testuser", start, end)

	// Only non-zero stats create activities: commits, PRs, restricted = 3
	if len(activities) != 3 {
		t.Fatalf("got %d activities, want 3 (non-zero stats only)", len(activities))
	}

	types := make(map[string]bool)
	for _, a := range activities {
		types[a.EventType] = true
		if a.ActorLogin != "testuser" {
			t.Errorf("ActorLogin = %q, want %q", a.ActorLogin, "testuser")
		}
	}

	if !types["AggregateCommits"] {
		t.Error("missing AggregateCommits")
	}
	if !types["AggregatePullRequests"] {
		t.Error("missing AggregatePullRequests")
	}
	if !types["AggregateRestrictedContributions"] {
		t.Error("missing AggregateRestrictedContributions")
	}
}

func TestCreateSyntheticActivities_AllZero(t *testing.T) {
	gh := &GitHubClient{}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	stats := &ContributionStats{}

	activities := gh.createSyntheticActivities(stats, "user", start, end)
	if len(activities) != 0 {
		t.Errorf("got %d activities, want 0 for all-zero stats", len(activities))
	}
}

// --- convertSearchResultToActivity tests ---

func TestConvertSearchResultToActivity_PR(t *testing.T) {
	gh := &GitHubClient{}
	created := time.Date(2025, 3, 15, 10, 0, 0, 0, time.UTC)

	issue := &github.Issue{
		ID:            github.Ptr(int64(100)),
		Number:        github.Ptr(42),
		Title:         github.Ptr("Add feature"),
		State:         github.Ptr("closed"),
		HTMLURL:       github.Ptr("https://github.com/org/repo/pull/42"),
		User:          &github.User{Login: github.Ptr("dev")},
		CreatedAt:     &github.Timestamp{Time: created},
		RepositoryURL: github.Ptr("https://api.github.com/repos/org/repo"),
		PullRequestLinks: &github.PullRequestLinks{
			URL: github.Ptr("https://api.github.com/repos/org/repo/pulls/42"),
		},
	}

	activity := gh.convertSearchResultToActivity(issue, "PullRequestEvent")

	if activity.EventType != "PullRequestEvent" {
		t.Errorf("EventType = %q, want %q", activity.EventType, "PullRequestEvent")
	}
	if activity.Repository != "org/repo" {
		t.Errorf("Repository = %q, want %q", activity.Repository, "org/repo")
	}
	if activity.ActorLogin != "dev" {
		t.Errorf("ActorLogin = %q, want %q", activity.ActorLogin, "dev")
	}
}

func TestConvertSearchResult_IssueType(t *testing.T) {
	gh := &GitHubClient{}
	created := time.Date(2025, 3, 15, 10, 0, 0, 0, time.UTC)

	issue := &github.Issue{
		ID:            github.Ptr(int64(200)),
		Number:        github.Ptr(10),
		Title:         github.Ptr("Bug report"),
		State:         github.Ptr("open"),
		HTMLURL:       github.Ptr("https://github.com/org/repo/issues/10"),
		User:          &github.User{Login: github.Ptr("reporter")},
		CreatedAt:     &github.Timestamp{Time: created},
		RepositoryURL: github.Ptr("https://api.github.com/repos/org/repo"),
	}

	activity := gh.convertSearchResultToActivity(issue, "IssuesEvent")

	if activity.EventType != "IssuesEvent" {
		t.Errorf("EventType = %q, want %q", activity.EventType, "IssuesEvent")
	}
}

// --- GetUserActivity integration test ---

// TestGetUserActivity_ViaEventsStrategy tests GetUserActivity routing to the Events API.
func TestGetUserActivity_ViaEventsStrategy(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inRange := now.Add(-12 * time.Hour)

	mux := http.NewServeMux()
	mux.HandleFunc("/users/testuser/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`[{
			"id": "1", "type": "WatchEvent",
			"actor": {"login": "testuser"},
			"repo": {"name": "org/repo"},
			"created_at": %q, "public": true, "payload": {}
		}]`, inRange.Format(time.RFC3339))))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := github.NewClient(nil)
	client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")
	gh := &GitHubClient{
		Client:      client,
		token:       "test-token",
		rateLimiter: time.NewTicker(time.Nanosecond),
	}

	// Use a recent date range so StrategyAuto selects events
	cfg := &models.QueryConfig{
		StartDate:         now.Add(-48 * time.Hour).Format("2006-01-02"),
		EndDate:           now.Format("2006-01-02"),
		GitHubAPIStrategy: "events",
	}

	activities, err := gh.GetUserActivity(context.Background(), "testuser", cfg)
	if err != nil {
		t.Fatalf("GetUserActivity error: %v", err)
	}

	if len(activities) != 1 {
		t.Fatalf("got %d activities, want 1", len(activities))
	}
}
