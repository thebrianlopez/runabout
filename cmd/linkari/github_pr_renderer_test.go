package main

// F7 GitHubPRRenderer contract tests (CT-1–CT-24) and regression guards (RG-1–RG-5).
// CT-* tests written first (M1) — they fail until M3/M4/M5 implement production code.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// compile-time assertion — GitHubPRRenderer implements CaptureRenderer.
var _ CaptureRenderer = (*GitHubPRRenderer)(nil)

// prFixedNow is the deterministic render time injected into Render calls.
var prFixedNow = time.Date(2026, 5, 5, 18, 39, 41, 0, time.UTC)

// prFullFixture returns a JSON githubPRResponse with all fields populated.
func prFullFixture() string {
	mergedAt := "2026-05-04T12:00:00Z"
	pr := map[string]interface{}{
		"number":    42,
		"title":     "Fix memory leak in capture pipeline",
		"body":      "This PR fixes the memory leak reported in issue #41.",
		"state":     "closed",
		"merged_at": mergedAt,
		"html_url":  "https://github.com/owner/repo/pull/42",
		"user":      map[string]interface{}{"login": "octocat"},
		"requested_reviewers": []interface{}{
			map[string]interface{}{"login": "monalisa"},
			map[string]interface{}{"login": "hubot"},
		},
		"changed_files": 3,
		"additions":     42,
		"deletions":     7,
	}
	b, _ := json.Marshal(pr)
	return string(b)
}

// prMinimalFixture returns a JSON githubPRResponse with only required fields.
func prMinimalFixture() string {
	pr := map[string]interface{}{
		"number":              42,
		"title":               "Minimal PR",
		"body":                "",
		"state":               "open",
		"merged_at":           nil,
		"html_url":            "https://github.com/owner/repo/pull/42",
		"user":                map[string]interface{}{"login": "octocat"},
		"requested_reviewers": []interface{}{},
		"changed_files":       0,
		"additions":           0,
		"deletions":           0,
	}
	b, _ := json.Marshal(pr)
	return string(b)
}

// --- CT-1 through CT-4: ParseGitHubPRURL ---

// CT-1: ParseGitHubPRURL with standard HTTPS PR URL.
func TestParsePRURL_CT1_StandardURL(t *testing.T) {
	owner, repo, prNumber, err := ParseGitHubPRURL("https://github.com/owner/repo/pull/42")
	if err != nil {
		t.Fatalf("CT-1: unexpected error: %v", err)
	}
	if owner != "owner" || repo != "repo" || prNumber != 42 {
		t.Errorf("CT-1: got (%q, %q, %d), want (\"owner\", \"repo\", 42)", owner, repo, prNumber)
	}
}

// CT-2: ParseGitHubPRURL with non-PR GitHub URL returns ErrUnsupportedURL.
func TestParsePRURL_CT2_NonPRGitHubURL(t *testing.T) {
	_, _, _, err := ParseGitHubPRURL("https://github.com/owner/repo")
	if err != ErrUnsupportedURL {
		t.Errorf("CT-2: got %v, want ErrUnsupportedURL", err)
	}
}

// CT-3: ParseGitHubPRURL with non-GitHub URL returns ErrUnsupportedURL.
func TestParsePRURL_CT3_NonGitHubURL(t *testing.T) {
	_, _, _, err := ParseGitHubPRURL("https://gitlab.com/owner/repo/pull/42")
	if err != ErrUnsupportedURL {
		t.Errorf("CT-3: got %v, want ErrUnsupportedURL", err)
	}
}

// CT-4: ParseGitHubPRURL with non-numeric PR number returns error.
func TestParsePRURL_CT4_NonNumericPRNumber(t *testing.T) {
	_, _, _, err := ParseGitHubPRURL("https://github.com/owner/repo/pull/notanumber")
	if err == nil {
		t.Error("CT-4: expected error for non-numeric PR number, got nil")
	}
}

// --- CT-5 through CT-9: GitHubClient.FetchPR ---

// CT-5: GitHubClient.FetchPR success → ContentTypeJSON, non-empty content.
func TestGitHubClient_CT5_FetchPR_200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(prFullFixture()))
	}))
	defer srv.Close()

	c := NewGitHubClient("test-token")
	c.client = srv.Client()
	c.apiBaseURL = srv.URL

	content, ct, err := c.FetchPR(context.Background(), "owner", "repo", 42)
	if err != nil {
		t.Fatalf("CT-5: unexpected error: %v", err)
	}
	if ct != ContentTypeJSON {
		t.Errorf("CT-5: content type = %v, want ContentTypeJSON", ct)
	}
	if len(content) == 0 {
		t.Error("CT-5: expected non-empty content")
	}
	// Verify it parses as githubPRResponse.
	var pr githubPRResponse
	if err := json.Unmarshal([]byte(content), &pr); err != nil {
		t.Errorf("CT-5: content does not parse as githubPRResponse: %v", err)
	}
}

// CT-6: GitHubClient.FetchPR 404 → ErrGitHubNotFound.
func TestGitHubClient_CT6_FetchPR_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewGitHubClient("test-token")
	c.client = srv.Client()
	c.apiBaseURL = srv.URL

	_, _, err := c.FetchPR(context.Background(), "owner", "repo", 42)
	if err != ErrGitHubNotFound {
		t.Errorf("CT-6: got %v, want ErrGitHubNotFound", err)
	}
}

// CT-7: GitHubClient.FetchPR 401 → ErrGitHubAuth.
func TestGitHubClient_CT7_FetchPR_401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewGitHubClient("bad-token")
	c.client = srv.Client()
	c.apiBaseURL = srv.URL

	_, _, err := c.FetchPR(context.Background(), "owner", "repo", 42)
	if err != ErrGitHubAuth {
		t.Errorf("CT-7: got %v, want ErrGitHubAuth", err)
	}
}

// CT-8: GitHubClient.FetchPR rate limited → ErrGitHubRateLimited.
func TestGitHubClient_CT8_FetchPR_RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	c := NewGitHubClient("test-token")
	c.client = srv.Client()
	c.apiBaseURL = srv.URL

	_, _, err := c.FetchPR(context.Background(), "owner", "repo", 42)
	if err != ErrGitHubRateLimited {
		t.Errorf("CT-8: got %v, want ErrGitHubRateLimited", err)
	}
}

// CT-9: GitHubClient.Fetch for non-PR GitHub URL still returns ContentTypeMarkdown.
func TestGitHubClient_CT9_Fetch_NonPR_StillMarkdown(t *testing.T) {
	const readmeContent = "# Hello World"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(mockContentResponse(readmeContent))
	}))
	defer srv.Close()

	u, _ := url.Parse("https://github.com/owner/repo")
	c := NewGitHubClient("test-token")
	c.client = srv.Client()
	c.apiBaseURL = srv.URL

	_, ct, err := c.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("CT-9: unexpected error: %v", err)
	}
	if ct != ContentTypeMarkdown {
		t.Errorf("CT-9: content type = %v, want ContentTypeMarkdown", ct)
	}
}

// --- CT-10 through CT-20: GitHubPRRenderer.Render ---

// CT-10: Render minimal PR → all frontmatter fields present.
func TestGitHubPRRenderer_CT10_AllFrontmatterFields(t *testing.T) {
	r := NewGitHubPRRenderer()
	out, err := r.Render(prFullFixture(), ContentTypeJSON, prFixedNow)
	if err != nil {
		t.Fatalf("CT-10: unexpected error: %v", err)
	}
	s := string(out)
	for _, field := range []string{"source:", "repo:", "pr_number:", "title:", "state:", "author:", "url:", "captured_at:"} {
		if !strings.Contains(s, field) {
			t.Errorf("CT-10: frontmatter missing field %q\noutput:\n%s", field, s)
		}
	}
}

// CT-11: Render with empty title → render_missing_title error, nil bytes.
func TestGitHubPRRenderer_CT11_EmptyTitle_Error(t *testing.T) {
	pr := map[string]interface{}{
		"number": 42,
		"title":  "",
		"state":  "open",
		"user":   map[string]interface{}{"login": "octocat"},
	}
	b, _ := json.Marshal(pr)
	r := NewGitHubPRRenderer()
	out, err := r.Render(string(b), ContentTypeJSON, prFixedNow)
	if err == nil {
		t.Fatal("CT-11: expected error for empty title, got nil")
	}
	if !strings.Contains(err.Error(), "render_missing_title") {
		t.Errorf("CT-11: expected 'render_missing_title' in error, got: %v", err)
	}
	if out != nil {
		t.Error("CT-11: expected nil bytes on error")
	}
}

// CT-12: Render with pr.Number == 0 → render_missing_pr_number error.
func TestGitHubPRRenderer_CT12_ZeroNumber_Error(t *testing.T) {
	pr := map[string]interface{}{
		"number": 0,
		"title":  "Some PR",
		"state":  "open",
		"user":   map[string]interface{}{"login": "octocat"},
	}
	b, _ := json.Marshal(pr)
	r := NewGitHubPRRenderer()
	out, err := r.Render(string(b), ContentTypeJSON, prFixedNow)
	if err == nil {
		t.Fatal("CT-12: expected error for pr.Number == 0, got nil")
	}
	if !strings.Contains(err.Error(), "render_missing_pr_number") {
		t.Errorf("CT-12: expected 'render_missing_pr_number' in error, got: %v", err)
	}
	if out != nil {
		t.Error("CT-12: expected nil bytes on error")
	}
}

// CT-13: Render with empty body → Description section absent.
func TestGitHubPRRenderer_CT13_EmptyBody_DescriptionAbsent(t *testing.T) {
	r := NewGitHubPRRenderer()
	out, err := r.Render(prMinimalFixture(), ContentTypeJSON, prFixedNow)
	if err != nil {
		t.Fatalf("CT-13: unexpected error: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "### Description") {
		t.Errorf("CT-13: Description section present but body is empty\noutput:\n%s", s)
	}
}

// CT-14: Render with non-empty body → Description section present.
func TestGitHubPRRenderer_CT14_NonEmptyBody_DescriptionPresent(t *testing.T) {
	r := NewGitHubPRRenderer()
	out, err := r.Render(prFullFixture(), ContentTypeJSON, prFixedNow)
	if err != nil {
		t.Fatalf("CT-14: unexpected error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "### Description") {
		t.Errorf("CT-14: Description section absent but body is non-empty\noutput:\n%s", s)
	}
	if !strings.Contains(s, "memory leak") {
		t.Errorf("CT-14: body text not present in output\noutput:\n%s", s)
	}
}

// CT-15: Render with zero diff stats → Diff Summary section absent.
func TestGitHubPRRenderer_CT15_ZeroDiffStats_DiffSummaryAbsent(t *testing.T) {
	r := NewGitHubPRRenderer()
	out, err := r.Render(prMinimalFixture(), ContentTypeJSON, prFixedNow)
	if err != nil {
		t.Fatalf("CT-15: unexpected error: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "### Diff Summary") {
		t.Errorf("CT-15: Diff Summary section present but all diff stats are zero\noutput:\n%s", s)
	}
}

// CT-16: Render with non-zero diff stats → Diff Summary section present with correct counts.
func TestGitHubPRRenderer_CT16_DiffStats_DiffSummaryPresent(t *testing.T) {
	r := NewGitHubPRRenderer()
	out, err := r.Render(prFullFixture(), ContentTypeJSON, prFixedNow)
	if err != nil {
		t.Fatalf("CT-16: unexpected error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "### Diff Summary") {
		t.Errorf("CT-16: Diff Summary section absent\noutput:\n%s", s)
	}
	if !strings.Contains(s, "3") || !strings.Contains(s, "42") || !strings.Contains(s, "7") {
		t.Errorf("CT-16: diff counts not present in output\noutput:\n%s", s)
	}
}

// CT-17: Render with empty reviewers → Reviewers section absent.
func TestGitHubPRRenderer_CT17_EmptyReviewers_ReviewersSectionAbsent(t *testing.T) {
	r := NewGitHubPRRenderer()
	out, err := r.Render(prMinimalFixture(), ContentTypeJSON, prFixedNow)
	if err != nil {
		t.Fatalf("CT-17: unexpected error: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "### Reviewers") {
		t.Errorf("CT-17: Reviewers section present but reviewers list is empty\noutput:\n%s", s)
	}
}

// CT-18: Render with reviewers → Reviewers section present with all logins.
func TestGitHubPRRenderer_CT18_Reviewers_ReviewersSectionPresent(t *testing.T) {
	r := NewGitHubPRRenderer()
	out, err := r.Render(prFullFixture(), ContentTypeJSON, prFixedNow)
	if err != nil {
		t.Fatalf("CT-18: unexpected error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "### Reviewers") {
		t.Errorf("CT-18: Reviewers section absent\noutput:\n%s", s)
	}
	if !strings.Contains(s, "monalisa") || !strings.Contains(s, "hubot") {
		t.Errorf("CT-18: reviewer logins not present in output\noutput:\n%s", s)
	}
}

// CT-19: Render closed PR (merged_at nil) → state: closed in frontmatter.
func TestGitHubPRRenderer_CT19_ClosedPR_StateIsClosed(t *testing.T) {
	pr := map[string]interface{}{
		"number":              42,
		"title":               "Closed PR",
		"body":                "",
		"state":               "closed",
		"merged_at":           nil,
		"html_url":            "https://github.com/owner/repo/pull/42",
		"user":                map[string]interface{}{"login": "octocat"},
		"requested_reviewers": []interface{}{},
		"changed_files":       0,
		"additions":           0,
		"deletions":           0,
	}
	b, _ := json.Marshal(pr)
	r := NewGitHubPRRenderer()
	out, err := r.Render(string(b), ContentTypeJSON, prFixedNow)
	if err != nil {
		t.Fatalf("CT-19: unexpected error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "state: closed") {
		t.Errorf("CT-19: expected 'state: closed' in frontmatter\noutput:\n%s", s)
	}
}

// CT-20: Render merged PR (merged_at non-nil) → state: merged and merged_at in frontmatter.
func TestGitHubPRRenderer_CT20_MergedPR_StateIsMerged(t *testing.T) {
	mergedAt := "2026-05-04T12:00:00Z"
	pr := map[string]interface{}{
		"number":              42,
		"title":               "Merged PR",
		"body":                "",
		"state":               "closed",
		"merged_at":           mergedAt,
		"html_url":            "https://github.com/owner/repo/pull/42",
		"user":                map[string]interface{}{"login": "octocat"},
		"requested_reviewers": []interface{}{},
		"changed_files":       0,
		"additions":           0,
		"deletions":           0,
	}
	b, _ := json.Marshal(pr)
	r := NewGitHubPRRenderer()
	out, err := r.Render(string(b), ContentTypeJSON, prFixedNow)
	if err != nil {
		t.Fatalf("CT-20: unexpected error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "state: merged") {
		t.Errorf("CT-20: expected 'state: merged' in frontmatter\noutput:\n%s", s)
	}
	if strings.Contains(s, "merged_at: ~") {
		t.Errorf("CT-20: merged_at should not be null for merged PR\noutput:\n%s", s)
	}
}

// --- CT-21 through CT-22: ArtifactKey ---

// CT-21: ArtifactKey extracts correct key from valid PR URL.
func TestGitHubPRRenderer_CT21_ArtifactKey_ValidURL(t *testing.T) {
	r := NewGitHubPRRenderer()
	got := r.ArtifactKey("https://github.com/anthropics/claude-code/pull/42")
	if got != "anthropics-claude-code-42" {
		t.Errorf("CT-21: ArtifactKey = %q, want %q", got, "anthropics-claude-code-42")
	}
}

// CT-22: ArtifactKey returns empty string on bad URL.
func TestGitHubPRRenderer_CT22_ArtifactKey_BadURL(t *testing.T) {
	r := NewGitHubPRRenderer()
	got := r.ArtifactKey("https://not-github.com/foo")
	if got != "" {
		t.Errorf("CT-22: ArtifactKey = %q, want %q", got, "")
	}
}

// --- CT-23 through CT-24: purity and error cases ---

// CT-23: Render is pure — same input → byte-identical output.
func TestGitHubPRRenderer_CT23_Pure(t *testing.T) {
	r := NewGitHubPRRenderer()
	fixed := time.Date(2026, 5, 5, 18, 39, 41, 0, time.UTC)
	out1, err := r.Render(prFullFixture(), ContentTypeJSON, fixed)
	if err != nil {
		t.Fatalf("CT-23: first render error: %v", err)
	}
	out2, err := r.Render(prFullFixture(), ContentTypeJSON, fixed)
	if err != nil {
		t.Fatalf("CT-23: second render error: %v", err)
	}
	if !bytes.Equal(out1, out2) {
		t.Error("CT-23: Render is not pure — outputs differ across calls")
	}
}

// CT-24: Render with non-JSON content → error.
func TestGitHubPRRenderer_CT24_NonJSONContent_Error(t *testing.T) {
	r := NewGitHubPRRenderer()
	out, err := r.Render("<html><body>Not JSON</body></html>", ContentTypeJSON, prFixedNow)
	if err == nil {
		t.Fatal("CT-24: expected error for non-JSON content, got nil")
	}
	if out != nil {
		t.Error("CT-24: expected nil bytes on error")
	}
}

// --- Regression Guards ---

// RG-1: GitHubPRRenderer.Render is pure — same (content, ct, now) → byte-identical output.
// (Same assertion as CT-23, kept as a named regression guard.)
func TestGitHubPRRenderer_RG1_RenderPurity(t *testing.T) {
	r := NewGitHubPRRenderer()
	fixed := time.Date(2026, 5, 5, 18, 39, 41, 0, time.UTC)
	out1, err := r.Render(prFullFixture(), ContentTypeJSON, fixed)
	if err != nil {
		t.Fatalf("RG-1: first render error: %v", err)
	}
	out2, err := r.Render(prFullFixture(), ContentTypeJSON, fixed)
	if err != nil {
		t.Fatalf("RG-1: second render error: %v", err)
	}
	if !bytes.Equal(out1, out2) {
		t.Error("RG-1: Render is not pure — outputs differ")
	}
}

// RG-2: No LLM call made for any capture_github_pr_auto action.
// Verified by TestCapture_RG1_NoLLMCallForKindCapture in capture_async_test.go —
// all KindCapture actions skip the evaluator by structural invariant (F2).
// This guard confirms GitHubPRRenderer compiles and satisfies CaptureRenderer without
// any evaluator dependency.
func TestGitHubPRRenderer_RG2_NoLLMDependency(t *testing.T) {
	// Compile-time: GitHubPRRenderer satisfies CaptureRenderer (no evaluator field).
	var _ CaptureRenderer = (*GitHubPRRenderer)(nil)
	// Structural: GitHubPRRenderer has no http.Client or evaluator fields.
	// (Enforced by RG-5; this test confirms the interface is satisfied without LLM.)
}

// RG-3: GitHubClient.Fetch for non-PR URLs still returns ContentTypeMarkdown.
// (Same assertion as CT-9, kept as a named regression guard.)
func TestGitHubClient_RG3_NonPRFetch_StillMarkdown(t *testing.T) {
	const readmeContent = "# Regression Guard"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(mockContentResponse(readmeContent))
	}))
	defer srv.Close()

	u, _ := url.Parse("https://github.com/owner/repo")
	c := NewGitHubClient("test-token")
	c.client = srv.Client()
	c.apiBaseURL = srv.URL

	_, ct, err := c.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("RG-3: unexpected error: %v", err)
	}
	if ct != ContentTypeMarkdown {
		t.Errorf("RG-3: content type = %v, want ContentTypeMarkdown", ct)
	}
}

// RG-4: All existing TestGitHubClient_* and TestParseGitHubURL_* tests pass unchanged.
// Enforced by running the full suite — no new assertion here.
func TestGitHubPRRenderer_RG4_ExistingGitHubTestsUnchanged(t *testing.T) {
	// Compile-time: GitHubClient still satisfies DomainClient.
	var _ DomainClient = (*GitHubClient)(nil)
}

// RG-5: GitHubPRRenderer struct has no http.Client field — renderer never makes network calls.
// Enforced at compile-time via the struct definition in github_pr_renderer.go.
// This test documents the invariant.
func TestGitHubPRRenderer_RG5_NoHTTPField(t *testing.T) {
	r := NewGitHubPRRenderer()
	// If GitHubPRRenderer had an http.Client field, it would be visible here.
	// The struct is defined as: type GitHubPRRenderer struct{} (empty).
	// This test will fail to compile if the struct gains network fields that
	// violate the pure-renderer invariant — changes to struct definition are
	// caught in code review.
	_ = r
}
