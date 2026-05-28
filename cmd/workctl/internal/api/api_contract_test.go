package api

// api_contract_test.go — EPIC-009 Phase 5: pure-logic API method tests.
// These test data-transformation methods that do not make network calls.
// httptest-based contract tests are deferred (see EPIC-009 non-goals).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v81/github"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/config"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
	"golang.org/x/time/rate"
)

// testClient returns a GitHubClient suitable for pure-logic tests.
// Uses a fake token; no network calls are made by any method under test.
func testClient(t *testing.T) *GitHubClient {
	t.Helper()
	c, err := NewGitHubClient("ghp_fake_token_for_unit_tests")
	if err != nil {
		t.Fatalf("testClient: NewGitHubClient failed: %v", err)
	}
	// Stop rate limiter immediately so tests aren't gated on ticks.
	c.rateLimiter.Stop()
	return c
}

// ptr helpers for go-github pointer fields
func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
func int64Ptr(i int64) *int64 { return &i }

// ---------------------------------------------------------------------------
// convertCommitToActivity
// ---------------------------------------------------------------------------

func TestConvertCommitToActivity_BasicFields(t *testing.T) {
	g := testClient(t)

	sha := "abc1234def5678"
	msg := "fix: correct off-by-one error\n\nLonger description here."
	login := "alice"
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	stamp := github.Timestamp{Time: ts}
	commit := &github.RepositoryCommit{
		SHA: strPtr(sha),
		Commit: &github.Commit{
			Message: strPtr(msg),
			Author:  &github.CommitAuthor{Date: &stamp},
		},
		Author: &github.User{Login: strPtr(login)},
	}

	act := g.convertCommitToActivity(commit, "org", "repo", "fallback-user")

	if act.EventID != sha {
		t.Errorf("EventID = %q, want %q", act.EventID, sha)
	}
	if act.EventType != "CommitEvent" {
		t.Errorf("EventType = %q, want CommitEvent", act.EventType)
	}
	if act.ActorLogin != login {
		t.Errorf("ActorLogin = %q, want %q", act.ActorLogin, login)
	}
	if act.Repository != "org/repo" {
		t.Errorf("Repository = %q, want org/repo", act.Repository)
	}
	// Only first line of commit message
	wantMsg := "fix: correct off-by-one error"
	if act.Description != wantMsg {
		t.Errorf("Description = %q, want %q", act.Description, wantMsg)
	}
	if !act.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", act.Timestamp, ts)
	}
	if !strings.Contains(act.URL, sha) {
		t.Errorf("URL %q does not contain SHA", act.URL)
	}
}

func TestConvertCommitToActivity_FallbackToCommitter(t *testing.T) {
	g := testClient(t)

	ts := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)
	stamp := github.Timestamp{Time: ts}
	commit := &github.RepositoryCommit{
		SHA: strPtr("deadbeef"),
		Commit: &github.Commit{
			Message:   strPtr("chore: update deps"),
			Author:    &github.CommitAuthor{}, // no Date set
			Committer: &github.CommitAuthor{Date: &stamp},
		},
	}

	act := g.convertCommitToActivity(commit, "org", "repo", "fallback")

	if !act.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want committer timestamp %v", act.Timestamp, ts)
	}
	if act.ActorLogin != "fallback" {
		t.Errorf("ActorLogin = %q, want fallback (no Author.Login)", act.ActorLogin)
	}
}

func TestConvertCommitToActivity_NilCommit(t *testing.T) {
	g := testClient(t)

	commit := &github.RepositoryCommit{SHA: strPtr("niltest")}
	act := g.convertCommitToActivity(commit, "org", "repo", "user")

	if act.EventType != "CommitEvent" {
		t.Errorf("EventType = %q, want CommitEvent", act.EventType)
	}
	if act.Description != "" {
		t.Errorf("Description = %q, want empty for nil Commit", act.Description)
	}
}

func TestConvertCommitToActivity_SingleLineMessage(t *testing.T) {
	g := testClient(t)

	commit := &github.RepositoryCommit{
		SHA: strPtr("abc"),
		Commit: &github.Commit{
			Message: strPtr("single line message"),
		},
	}

	act := g.convertCommitToActivity(commit, "org", "repo", "user")
	if act.Description != "single line message" {
		t.Errorf("Description = %q, want full single-line message", act.Description)
	}
}

// ---------------------------------------------------------------------------
// generateDescriptionAndURL
// ---------------------------------------------------------------------------

func makeEvent(repoName, eventType string) *github.Event {
	return &github.Event{
		Type: strPtr(eventType),
		Repo: &github.Repository{Name: strPtr(repoName)},
	}
}

func TestGenerateDescriptionAndURL_PushEvent(t *testing.T) {
	g := testClient(t)
	event := makeEvent("org/repo", "PushEvent")
	ref := "refs/heads/main"
	payload := &github.PushEvent{
		Ref:     strPtr(ref),
		Commits: []*github.HeadCommit{{}, {}}, // 2 commits
	}

	desc, url := g.generateDescriptionAndURL(event, payload)

	if !strings.Contains(desc, "2") {
		t.Errorf("desc %q should mention 2 commits", desc)
	}
	if !strings.Contains(desc, "main") {
		t.Errorf("desc %q should mention branch name", desc)
	}
	if !strings.Contains(url, "main") {
		t.Errorf("url %q should contain branch", url)
	}
}

func TestGenerateDescriptionAndURL_PullRequestEvent(t *testing.T) {
	g := testClient(t)
	event := makeEvent("org/repo", "PullRequestEvent")
	prURL := "https://github.com/org/repo/pull/42"
	payload := &github.PullRequestEvent{
		Action: strPtr("opened"),
		Number: intPtr(42),
		PullRequest: &github.PullRequest{
			Title:   strPtr("Add feature X"),
			HTMLURL: strPtr(prURL),
		},
	}

	desc, url := g.generateDescriptionAndURL(event, payload)

	if !strings.Contains(desc, "42") {
		t.Errorf("desc %q should mention PR number", desc)
	}
	if url != prURL {
		t.Errorf("url = %q, want %q", url, prURL)
	}
}

func TestGenerateDescriptionAndURL_PullRequestReviewEvent(t *testing.T) {
	g := testClient(t)
	event := makeEvent("org/repo", "PullRequestReviewEvent")
	reviewURL := "https://github.com/org/repo/pull/10#pullrequestreview-1"
	payload := &github.PullRequestReviewEvent{
		Action:      strPtr("submitted"),
		PullRequest: &github.PullRequest{Number: intPtr(10)},
		Review:      &github.PullRequestReview{HTMLURL: strPtr(reviewURL)},
	}

	desc, url := g.generateDescriptionAndURL(event, payload)

	if !strings.Contains(desc, "10") {
		t.Errorf("desc %q should mention PR number", desc)
	}
	if url != reviewURL {
		t.Errorf("url = %q, want %q", url, reviewURL)
	}
}

func TestGenerateDescriptionAndURL_IssuesEvent(t *testing.T) {
	g := testClient(t)
	event := makeEvent("org/repo", "IssuesEvent")
	issueURL := "https://github.com/org/repo/issues/7"
	payload := &github.IssuesEvent{
		Action: strPtr("closed"),
		Issue: &github.Issue{
			Number:  intPtr(7),
			Title:   strPtr("Bug report"),
			HTMLURL: strPtr(issueURL),
		},
	}

	desc, url := g.generateDescriptionAndURL(event, payload)

	if !strings.Contains(desc, "7") {
		t.Errorf("desc %q should mention issue number", desc)
	}
	if url != issueURL {
		t.Errorf("url = %q, want %q", url, issueURL)
	}
}

func TestGenerateDescriptionAndURL_IssueCommentEvent(t *testing.T) {
	g := testClient(t)
	event := makeEvent("org/repo", "IssueCommentEvent")
	commentURL := "https://github.com/org/repo/issues/3#issuecomment-1"
	payload := &github.IssueCommentEvent{
		Action:  strPtr("created"),
		Issue:   &github.Issue{Number: intPtr(3)},
		Comment: &github.IssueComment{HTMLURL: strPtr(commentURL)},
	}

	desc, url := g.generateDescriptionAndURL(event, payload)

	if !strings.Contains(desc, "3") {
		t.Errorf("desc %q should mention issue number", desc)
	}
	if url != commentURL {
		t.Errorf("url = %q, want %q", url, commentURL)
	}
}

func TestGenerateDescriptionAndURL_CreateEvent(t *testing.T) {
	g := testClient(t)
	event := makeEvent("org/repo", "CreateEvent")

	// branch variant
	payload := &github.CreateEvent{RefType: strPtr("branch"), Ref: strPtr("feature/foo")}
	desc, url := g.generateDescriptionAndURL(event, payload)
	if !strings.Contains(desc, "branch") {
		t.Errorf("desc %q should mention branch", desc)
	}
	if !strings.Contains(url, "feature/foo") {
		t.Errorf("url %q should contain branch name", url)
	}

	// tag variant
	payload2 := &github.CreateEvent{RefType: strPtr("tag"), Ref: strPtr("v1.0.0")}
	_, url2 := g.generateDescriptionAndURL(event, payload2)
	if !strings.Contains(url2, "releases/tag") {
		t.Errorf("url %q should contain releases/tag for tag create", url2)
	}
}

func TestGenerateDescriptionAndURL_DeleteEvent(t *testing.T) {
	g := testClient(t)
	event := makeEvent("org/repo", "DeleteEvent")
	payload := &github.DeleteEvent{RefType: strPtr("branch"), Ref: strPtr("old-branch")}

	desc, _ := g.generateDescriptionAndURL(event, payload)
	if !strings.Contains(desc, "Deleted") {
		t.Errorf("desc %q should contain Deleted", desc)
	}
}

func TestGenerateDescriptionAndURL_CommitCommentEvent(t *testing.T) {
	g := testClient(t)
	event := makeEvent("org/repo", "CommitCommentEvent")
	commentURL := "https://github.com/org/repo/commit/abc#comment-1"
	payload := &github.CommitCommentEvent{
		Comment: &github.RepositoryComment{HTMLURL: strPtr(commentURL)},
	}

	_, url := g.generateDescriptionAndURL(event, payload)
	if url != commentURL {
		t.Errorf("url = %q, want %q", url, commentURL)
	}
}

func TestGenerateDescriptionAndURL_WatchEvent(t *testing.T) {
	g := testClient(t)
	event := makeEvent("org/repo", "WatchEvent")
	payload := &github.WatchEvent{Action: strPtr("started")}

	desc, _ := g.generateDescriptionAndURL(event, payload)
	if !strings.Contains(strings.ToLower(desc), "started") {
		t.Errorf("desc %q should mention watch action", desc)
	}
}

func TestGenerateDescriptionAndURL_UnknownEventType(t *testing.T) {
	g := testClient(t)
	event := makeEvent("org/repo", "ForkEvent")
	payload := struct{}{} // unknown type → default branch

	desc, _ := g.generateDescriptionAndURL(event, payload)
	if !strings.Contains(desc, "ForkEvent") {
		t.Errorf("desc %q should contain event type for unknown payload", desc)
	}
}

// ---------------------------------------------------------------------------
// createSyntheticActivities
// ---------------------------------------------------------------------------

func TestCreateSyntheticActivities_AllFields(t *testing.T) {
	g := testClient(t)

	stats := &ContributionStats{
		TotalCommits:            10,
		TotalPRs:                3,
		TotalReviews:            5,
		TotalIssues:             2,
		RestrictedContributions: 7,
		Username:                "alice",
	}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)

	activities := g.createSyntheticActivities(stats, "alice", start, end)

	if len(activities) != 5 {
		t.Fatalf("expected 5 synthetic activities, got %d", len(activities))
	}

	types := make(map[string]bool)
	for _, a := range activities {
		types[a.EventType] = true
		if a.ActorLogin != "alice" {
			t.Errorf("ActorLogin = %q, want alice", a.ActorLogin)
		}
		if !a.Timestamp.Equal(start) {
			t.Errorf("Timestamp = %v, want start %v", a.Timestamp, start)
		}
	}

	for _, want := range []string{"AggregateCommits", "AggregatePullRequests", "AggregatePullRequestReviews", "AggregateIssues", "AggregateRestrictedContributions"} {
		if !types[want] {
			t.Errorf("missing synthetic event type %q", want)
		}
	}
}

func TestCreateSyntheticActivities_ZeroStats(t *testing.T) {
	g := testClient(t)

	stats := &ContributionStats{}
	start := time.Now()
	end := start.AddDate(0, 0, 30)

	activities := g.createSyntheticActivities(stats, "alice", start, end)

	if len(activities) != 0 {
		t.Errorf("expected 0 activities for zero stats, got %d", len(activities))
	}
}

func TestCreateSyntheticActivities_PartialStats(t *testing.T) {
	g := testClient(t)

	stats := &ContributionStats{TotalCommits: 5} // only commits
	start := time.Now()
	end := start.AddDate(0, 0, 30)

	activities := g.createSyntheticActivities(stats, "bob", start, end)

	if len(activities) != 1 {
		t.Fatalf("expected 1 activity for commits-only, got %d", len(activities))
	}
	if activities[0].EventType != "AggregateCommits" {
		t.Errorf("EventType = %q, want AggregateCommits", activities[0].EventType)
	}
}

// ---------------------------------------------------------------------------
// convertSearchResultToActivity
// ---------------------------------------------------------------------------

func TestConvertSearchResultToActivity_PullRequest(t *testing.T) {
	g := testClient(t)

	repoURL := "https://api.github.com/repos/org/myrepo"
	prURL := "https://github.com/org/myrepo/pull/99"
	issue := &github.Issue{
		ID:            int64Ptr(1001),
		Number:        intPtr(99),
		Title:         strPtr("Add feature Y"),
		State:         strPtr("closed"),
		HTMLURL:       strPtr(prURL),
		RepositoryURL: strPtr(repoURL),
		User:          &github.User{Login: strPtr("bob")},
		PullRequestLinks: &github.PullRequestLinks{
			URL: strPtr("https://api.github.com/repos/org/myrepo/pulls/99"),
		},
	}

	act := g.convertSearchResultToActivity(issue, "PullRequestEvent")

	if act.EventType != "PullRequestEvent" {
		t.Errorf("EventType = %q, want PullRequestEvent", act.EventType)
	}
	if act.Repository != "org/myrepo" {
		t.Errorf("Repository = %q, want org/myrepo", act.Repository)
	}
	if act.ActorLogin != "bob" {
		t.Errorf("ActorLogin = %q, want bob", act.ActorLogin)
	}
	if !strings.Contains(act.Description, "Merged") {
		t.Errorf("Description %q should say Merged for closed PR with PullRequestLinks", act.Description)
	}
}

func TestConvertSearchResultToActivity_Issue(t *testing.T) {
	g := testClient(t)

	repoURL := "https://api.github.com/repos/org/myrepo"
	issue := &github.Issue{
		ID:            int64Ptr(2002),
		Number:        intPtr(55),
		Title:         strPtr("Bug: nil pointer"),
		State:         strPtr("open"),
		HTMLURL:       strPtr("https://github.com/org/myrepo/issues/55"),
		RepositoryURL: strPtr(repoURL),
		User:          &github.User{Login: strPtr("carol")},
	}

	act := g.convertSearchResultToActivity(issue, "IssuesEvent")

	if act.EventType != "IssuesEvent" {
		t.Errorf("EventType = %q, want IssuesEvent", act.EventType)
	}
	if !strings.Contains(act.Description, "55") {
		t.Errorf("Description %q should mention issue number", act.Description)
	}
}

// ---------------------------------------------------------------------------
// WarnAboutLimitations (github_strategy.go)
// ---------------------------------------------------------------------------

func TestWarnAboutLimitations_Quiet(t *testing.T) {
	// quiet=true should not panic and should return immediately
	WarnAboutLimitations(StrategyEvents, 120, true)
	WarnAboutLimitations(StrategySearch, 400, true)
	WarnAboutLimitations(StrategyGraphQL, 800, true)
}

func TestWarnAboutLimitations_EventsOnOldData(t *testing.T) {
	// events strategy on old data should print warning; just verify no panic
	WarnAboutLimitations(StrategyEvents, 120, false)
}

func TestWarnAboutLimitations_SearchOnRecentData(t *testing.T) {
	WarnAboutLimitations(StrategySearch, 30, false)
}

func TestWarnAboutLimitations_GraphQLOnRecentData(t *testing.T) {
	WarnAboutLimitations(StrategyGraphQL, 30, false)
}

// Missing WarnAboutLimitations branches: Search-historical, GraphQL-old, debug path.
func TestWarnAboutLimitations_SearchHistoricalData(t *testing.T) {
	// Search + CategoryHistorical (91-365 days) → hits the else branch.
	WarnAboutLimitations(StrategySearch, 120, false)
}

func TestWarnAboutLimitations_GraphQLOnOldData(t *testing.T) {
	// GraphQL + CategoryOld (>365 days) → hits the "category == CategoryOld" branch.
	WarnAboutLimitations(StrategyGraphQL, 400, false)
}

func TestWarnAboutLimitations_EventsDebugBranch(t *testing.T) {
	// Events + daysAgo > 90 + Debug=true → hits the debug LogDebug branch.
	old := config.Debug
	config.Debug = true
	defer func() { config.Debug = old }()
	WarnAboutLimitations(StrategyEvents, 120, false)
}

// ---------------------------------------------------------------------------
// convertEvent (github.go:373)
// ---------------------------------------------------------------------------

func TestConvertEvent_PushEvent(t *testing.T) {
	g := testClient(t)

	rawPayload := json.RawMessage(`{"ref":"refs/heads/main","commits":[{"id":"abc","message":"fix bug","added":[],"removed":[],"modified":[]}]}`)
	eventType := "PushEvent"
	repoName := "org/repo"
	ts := github.Timestamp{Time: time.Now()}
	event := &github.Event{
		Type:       &eventType,
		Repo:       &github.Repository{Name: &repoName},
		CreatedAt:  &ts,
		RawPayload: &rawPayload,
	}

	act, err := g.convertEvent(event)
	if err != nil {
		t.Fatalf("convertEvent PushEvent error: %v", err)
	}
	if act.Repository != "org/repo" {
		t.Errorf("Repository = %q, want org/repo", act.Repository)
	}
	if !strings.Contains(act.Description, "Pushed") {
		t.Errorf("Description = %q, expected 'Pushed'", act.Description)
	}
}

func TestConvertEvent_BadPayload(t *testing.T) {
	g := testClient(t)

	rawPayload := json.RawMessage(`not-valid-json`)
	eventType := "PushEvent"
	event := &github.Event{
		Type:       &eventType,
		RawPayload: &rawPayload,
	}

	_, err := g.convertEvent(event)
	if err == nil {
		t.Error("convertEvent with invalid JSON payload should return error")
	}
}

// ---------------------------------------------------------------------------
// convertSearchResultToActivity — open (non-merged) PR branch
// ---------------------------------------------------------------------------

func TestConvertSearchResultToActivity_OpenPR(t *testing.T) {
	g := testClient(t)

	repoURL := "https://api.github.com/repos/org/repo"
	issue := &github.Issue{
		ID:            int64Ptr(3003),
		Number:        intPtr(77),
		Title:         strPtr("Work in progress"),
		State:         strPtr("open"),
		HTMLURL:       strPtr("https://github.com/org/repo/pull/77"),
		RepositoryURL: strPtr(repoURL),
		User:          &github.User{Login: strPtr("alice")},
		// No PullRequestLinks → not merged → hits the else branch
	}

	act := g.convertSearchResultToActivity(issue, "PullRequestEvent")

	if strings.Contains(act.Description, "Merged") {
		t.Errorf("Description %q should NOT say Merged for open PR", act.Description)
	}
	if !strings.Contains(act.Description, "77") {
		t.Errorf("Description %q should mention PR number", act.Description)
	}
}

// ---------------------------------------------------------------------------
// withRetry — success path (no sleep, fn returns nil immediately)
// ---------------------------------------------------------------------------

func TestWithRetry_SuccessOnFirstAttempt(t *testing.T) {
	g := testClient(t)

	called := false
	err := g.withRetry(func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Errorf("withRetry success should return nil, got %v", err)
	}
	if !called {
		t.Error("fn should have been called")
	}
}

func TestWithRetry_SuccessAfterTransientError(t *testing.T) {
	// fn fails once then succeeds — exercises the retry loop without sleeping
	// by using a non-standard error that bypasses exponential backoff... except
	// it doesn't: withRetry always sleeps for non-rate-limit errors. We only
	// test the success-on-second-attempt where we verify the retry counter.
	// NOTE: This test will actually sleep 2s (attempt 0 backoff). Skipping to
	// keep the test suite fast; pure success path is covered above.
	t.Skip("withRetry failure path sleeps; covered only by integration tests")
}

// ---------------------------------------------------------------------------
// GetUserActivity — early-return error paths (no network calls)
// ---------------------------------------------------------------------------

func minimalCfg(startDate, endDate, strategy string) *models.QueryConfig {
	return &models.QueryConfig{
		StartDate:         startDate,
		EndDate:           endDate,
		GitHubAPIStrategy: strategy,
	}
}

func TestGetUserActivity_EmptyUsername(t *testing.T) {
	g := testClient(t)

	_, err := g.GetUserActivity(context.Background(), "", minimalCfg("2025-01-01", "2025-12-31", "auto"))
	if err == nil {
		t.Error("empty username should return error")
	}
}

func TestGetUserActivity_InvalidStartDate(t *testing.T) {
	g := testClient(t)

	_, err := g.GetUserActivity(context.Background(), "alice", minimalCfg("not-a-date", "2025-12-31", "auto"))
	if err == nil {
		t.Error("invalid start date should return error")
	}
}

func TestGetUserActivity_InvalidEndDate(t *testing.T) {
	g := testClient(t)

	_, err := g.GetUserActivity(context.Background(), "alice", minimalCfg("2025-01-01", "bad-end", "auto"))
	if err == nil {
		t.Error("invalid end date should return error")
	}
}

func TestGetUserActivity_FutureStartDate(t *testing.T) {
	g := testClient(t)

	// Future dates cause SelectStrategy to return an error (no network calls).
	_, err := g.GetUserActivity(context.Background(), "alice", minimalCfg("2030-01-01", "2030-12-31", "auto"))
	if err == nil {
		t.Error("future start date should return error from SelectStrategy")
	}
}

func TestGetUserActivity_EmptyStrategyFutureDate(t *testing.T) {
	g := testClient(t)

	// Empty GitHubAPIStrategy → strategyOverride = "auto" (hits the "" fallback branch).
	// Future dates ensure no network call.
	_, err := g.GetUserActivity(context.Background(), "alice", minimalCfg("2030-01-01", "2030-12-31", ""))
	if err == nil {
		t.Error("future start date should return error")
	}
}

func TestGetUserActivity_DebugMode(t *testing.T) {
	old := config.Debug
	config.Debug = true
	defer func() { config.Debug = old }()

	g := testClient(t)

	// Future date ensures error before network call; with Debug=true the first
	// two LogDebug calls (lines 53-54) are executed.
	_, err := g.GetUserActivity(context.Background(), "alice", minimalCfg("2030-01-01", "2030-12-31", "auto"))
	if err == nil {
		t.Error("future start date should return error")
	}
}

// ---------------------------------------------------------------------------
// NewAtlassianClients — constructor (no network calls during creation)
// ---------------------------------------------------------------------------

func TestNewAtlassianClients_Success(t *testing.T) {
	c, err := NewAtlassianClients("example.atlassian.net", "user@example.com", "fake-token")
	if err != nil {
		t.Fatalf("NewAtlassianClients unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil AtlassianClients")
	}
}

// ---------------------------------------------------------------------------
// withRetry — failure then success path (covers retry loop body, sleeps 2s)
// ---------------------------------------------------------------------------

func TestWithRetry_SuccessAfterOneFailure(t *testing.T) {
	g := testClient(t)

	attempts := 0
	err := g.withRetry(func() error {
		attempts++
		if attempts == 1 {
			// First attempt fails with a generic error (not rate-limit).
			// withRetry will sleep 2s then try again.
			return errors.New("transient error")
		}
		return nil // second attempt succeeds
	})
	if err != nil {
		t.Errorf("withRetry should succeed after transient error, got: %v", err)
	}
	if attempts != 2 {
		t.Errorf("fn should be called twice, called %d times", attempts)
	}
}

// ---------------------------------------------------------------------------
// withRetry — rate limit error path (no sleep: reset time is in the past)
// ---------------------------------------------------------------------------

func makeHTTPResp(statusCode int) *http.Response {
	req, _ := http.NewRequest("GET", "https://api.github.com/test", nil)
	return &http.Response{StatusCode: statusCode, Request: req}
}

func TestWithRetry_RateLimitError_PastReset(t *testing.T) {
	g := testClient(t)

	// Rate limit error with reset time in the past → waitDuration ≤ 0 → no sleep.
	rateLimitErr := &github.RateLimitError{
		Response: makeHTTPResp(429),
		Rate:     github.Rate{Reset: github.Timestamp{Time: time.Now().Add(-time.Second)}},
	}

	attempts := 0
	err := g.withRetry(func() error {
		attempts++
		if attempts == 1 {
			return rateLimitErr
		}
		return nil
	})
	if err != nil {
		t.Errorf("expected success after rate-limit retry, got: %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

// ---------------------------------------------------------------------------
// withRetry — abuse rate limit error path (RetryAfter=0 → no sleep)
// ---------------------------------------------------------------------------

func TestWithRetry_AbuseRateLimitError_NoSleep(t *testing.T) {
	g := testClient(t)

	zero := time.Duration(0)
	abuseErr := &github.AbuseRateLimitError{
		Response:   makeHTTPResp(429),
		RetryAfter: &zero, // RetryAfter != nil → time.Sleep(0) = no-op
	}

	attempts := 0
	err := g.withRetry(func() error {
		attempts++
		if attempts == 1 {
			return abuseErr
		}
		return nil
	})
	if err != nil {
		t.Errorf("expected success after abuse-rate-limit retry, got: %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

// ---------------------------------------------------------------------------
// withRetry — debug-mode coverage for rate-limit and retry-loop log branches
// ---------------------------------------------------------------------------

func TestWithRetry_DebugMode_RateLimitThenSuccess(t *testing.T) {
	// Debug=true + rate limit with past reset → covers debug LogDebug branch
	// inside rate limit handler AND the "attempt > 0 && Debug" retry-loop branch.
	// No sleep because reset time is in the past.
	old := config.Debug
	config.Debug = true
	defer func() { config.Debug = old }()

	g := testClient(t)

	rateLimitErr := &github.RateLimitError{
		Response: makeHTTPResp(429),
		Rate:     github.Rate{Reset: github.Timestamp{Time: time.Now().Add(-time.Second)}},
	}

	attempts := 0
	err := g.withRetry(func() error {
		attempts++
		if attempts == 1 {
			return rateLimitErr
		}
		return nil
	})
	if err != nil {
		t.Errorf("expected success after rate-limit retry (debug mode), got: %v", err)
	}
}

func TestWithRetry_DebugMode_AbuseRateLimitThenSuccess(t *testing.T) {
	// Debug=true + abuse rate limit with RetryAfter=0 → covers debug LogDebug
	// inside abuse handler. Sleep = 0.
	old := config.Debug
	config.Debug = true
	defer func() { config.Debug = old }()

	g := testClient(t)

	zero := time.Duration(0)
	abuseErr := &github.AbuseRateLimitError{
		Response:   makeHTTPResp(429),
		RetryAfter: &zero,
	}

	attempts := 0
	err := g.withRetry(func() error {
		attempts++
		if attempts == 1 {
			return abuseErr
		}
		return nil
	})
	if err != nil {
		t.Errorf("expected success after abuse-limit retry (debug mode), got: %v", err)
	}
}

func TestWithRetry_DebugMode_GenericErrorBackoff(t *testing.T) {
	// Debug=true + generic error on attempt 0 → covers the debug LogDebug in
	// exponential-backoff block. Sleeps 2s (attempt 0 backoff). Then succeeds.
	old := config.Debug
	config.Debug = true
	defer func() { config.Debug = old }()

	g := testClient(t)

	attempts := 0
	err := g.withRetry(func() error {
		attempts++
		if attempts == 1 {
			return errors.New("generic transient error")
		}
		return nil
	})
	if err != nil {
		t.Errorf("expected success after transient error (debug mode), got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// NewAtlassianClients + withRateLimitAndRetry (no network calls)
// ---------------------------------------------------------------------------

// testAtlassianClient creates an AtlassianClients with a near-instant rate limiter.
func testAtlassianClient(t *testing.T) *AtlassianClients {
	t.Helper()
	c, err := NewAtlassianClients("example.atlassian.net", "user@example.com", "fake-token")
	if err != nil {
		t.Fatalf("testAtlassianClient: NewAtlassianClients failed: %v", err)
	}
	// Replace 1-second limiter with unbounded limiter so tests don't block.
	c.rateLimiter = rate.NewLimiter(rate.Inf, 1)
	return c
}

func TestWithRateLimitAndRetry_Success(t *testing.T) {
	a := testAtlassianClient(t)

	called := false
	err := a.withRateLimitAndRetry(func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Errorf("withRateLimitAndRetry success should return nil, got %v", err)
	}
	if !called {
		t.Error("fn should have been called")
	}
}

func TestWithRateLimitAndRetry_OneFailureThenSuccess(t *testing.T) {
	// First failure (retries=0): retryDelay = 2*0*Second = 0s → no sleep.
	// Second attempt (retries=1) succeeds.
	a := testAtlassianClient(t)

	attempts := 0
	err := a.withRateLimitAndRetry(func() error {
		attempts++
		if attempts == 1 {
			return errors.New("transient atlassian error")
		}
		return nil
	})
	if err != nil {
		t.Errorf("expected success after transient error, got %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestWithRateLimitAndRetry_DebugMode(t *testing.T) {
	// Debug=true + 1 failure: covers "config.Debug && retries > 0" log branch.
	old := config.Debug
	config.Debug = true
	defer func() { config.Debug = old }()

	a := testAtlassianClient(t)

	attempts := 0
	err := a.withRateLimitAndRetry(func() error {
		attempts++
		if attempts == 1 {
			return errors.New("debug transient error")
		}
		return nil
	})
	if err != nil {
		t.Errorf("expected success in debug mode, got %v", err)
	}
}

// Sentinel to ensure errors import is used.
var _ = errors.Is
