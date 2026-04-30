package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// CT-11: compile-time assertion that GitHubClient satisfies DomainClient.
var _ DomainClient = (*GitHubClient)(nil)

// mockContentResponse returns a JSON body matching the GitHub REST API content schema.
func mockContentResponse(content string) []byte {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	b, _ := json.Marshal(map[string]string{
		"content":  encoded,
		"encoding": "base64",
		"name":     "README.md",
		"path":     "README.md",
	})
	return b
}

// CT-1: ParseGitHubURL with HTTPS repo root URL.
func TestParseGitHubURL_HTTPSRoot(t *testing.T) {
	owner, repo, fp, err := ParseGitHubURL("https://github.com/owner/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "owner" || repo != "repo" || fp != "" {
		t.Errorf("got (%q, %q, %q), want (\"owner\", \"repo\", \"\")", owner, repo, fp)
	}
}

// CT-2: ParseGitHubURL with HTTPS file URL extracts filepath without blob/branch prefix.
func TestParseGitHubURL_HTTPSFilePath(t *testing.T) {
	_, _, fp, err := ParseGitHubURL("https://github.com/owner/repo/blob/main/path/to/file.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fp != "path/to/file.md" {
		t.Errorf("filepath: got %q, want \"path/to/file.md\"", fp)
	}
}

// CT-3: ParseGitHubURL with SSH URL.
func TestParseGitHubURL_SSH(t *testing.T) {
	owner, repo, fp, err := ParseGitHubURL("git@github.com:owner/repo.git")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "owner" || repo != "repo" || fp != "" {
		t.Errorf("got (%q, %q, %q), want (\"owner\", \"repo\", \"\")", owner, repo, fp)
	}
}

// CT-4: ParseGitHubURL with non-GitHub URL returns ErrUnsupportedURL.
func TestParseGitHubURL_NonGitHub(t *testing.T) {
	_, _, _, err := ParseGitHubURL("https://gitlab.com/owner/repo")
	if err != ErrUnsupportedURL {
		t.Errorf("got %v, want ErrUnsupportedURL", err)
	}
}

// CT-5: ParseGitHubURL with owner-only URL (no repo segment) returns ErrInvalidRepoPath.
func TestParseGitHubURL_OwnerOnly(t *testing.T) {
	_, _, _, err := ParseGitHubURL("https://github.com/owner")
	if err != ErrInvalidRepoPath {
		t.Errorf("got %v, want ErrInvalidRepoPath", err)
	}
}

// CT-6: Fetch with mock 200 returns (content, ContentTypeMarkdown, nil).
func TestGitHubClientFetch_200(t *testing.T) {
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

	content, ct, err := c.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != readmeContent {
		t.Errorf("content: got %q, want %q", content, readmeContent)
	}
	if ct != ContentTypeMarkdown {
		t.Errorf("content type: got %v, want ContentTypeMarkdown", ct)
	}
}

// CT-7: Fetch with mock 401 returns ErrGitHubAuth.
func TestGitHubClientFetch_401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	u, _ := url.Parse("https://github.com/owner/repo")
	c := NewGitHubClient("bad-token")
	c.client = srv.Client()
	c.apiBaseURL = srv.URL

	_, _, err := c.Fetch(context.Background(), u)
	if err != ErrGitHubAuth {
		t.Errorf("got %v, want ErrGitHubAuth", err)
	}
}

// CT-8: Fetch with mock 404 returns ErrGitHubNotFound.
func TestGitHubClientFetch_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	u, _ := url.Parse("https://github.com/owner/repo")
	c := NewGitHubClient("test-token")
	c.client = srv.Client()
	c.apiBaseURL = srv.URL

	_, _, err := c.Fetch(context.Background(), u)
	if err != ErrGitHubNotFound {
		t.Errorf("got %v, want ErrGitHubNotFound", err)
	}
}

// CT-9: Fetch with mock 429 returns ErrGitHubRateLimited.
func TestGitHubClientFetch_429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	u, _ := url.Parse("https://github.com/owner/repo")
	c := NewGitHubClient("test-token")
	c.client = srv.Client()
	c.apiBaseURL = srv.URL

	_, _, err := c.Fetch(context.Background(), u)
	if err != ErrGitHubRateLimited {
		t.Errorf("got %v, want ErrGitHubRateLimited", err)
	}
}

// CT-10: X-RateLimit-Remaining: 0 on a 200 response returns ErrGitHubRateLimited.
func TestGitHubClientFetch_RateLimitHeaderZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(mockContentResponse("# Hello"))
	}))
	defer srv.Close()

	u, _ := url.Parse("https://github.com/owner/repo")
	c := NewGitHubClient("test-token")
	c.client = srv.Client()
	c.apiBaseURL = srv.URL

	_, _, err := c.Fetch(context.Background(), u)
	if err != ErrGitHubRateLimited {
		t.Errorf("got %v, want ErrGitHubRateLimited", err)
	}
}

// CT-12: Fetch with empty token (unauthenticated) sends no Authorization header and returns content.
func TestGitHubClientFetch_EmptyToken(t *testing.T) {
	const readmeContent = "# Public Repo"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no Authorization header for empty token, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(mockContentResponse(readmeContent))
	}))
	defer srv.Close()

	u, _ := url.Parse("https://github.com/owner/repo")
	c := NewGitHubClient("")
	c.client = srv.Client()
	c.apiBaseURL = srv.URL

	content, ct, err := c.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != readmeContent {
		t.Errorf("content: got %q, want %q", content, readmeContent)
	}
	if ct != ContentTypeMarkdown {
		t.Errorf("content type: got %v, want ContentTypeMarkdown", ct)
	}
}

// CT-13: ParseGitHubURL with an issue URL returns ErrUnsupportedURL.
func TestParseGitHubURL_IssueURL(t *testing.T) {
	_, _, _, err := ParseGitHubURL("https://github.com/owner/repo/issues/1213")
	if err != ErrUnsupportedURL {
		t.Errorf("got %v, want ErrUnsupportedURL", err)
	}
}

// BT-1: X-RateLimit-Remaining: 50 on a 200 response emits github_quota_warning but returns content successfully.
func TestGitHubClientFetch_QuotaWarning(t *testing.T) {
	const readmeContent = "# Quota Warning Test"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "50")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(mockContentResponse(readmeContent))
	}))
	defer srv.Close()

	var events []string
	u, _ := url.Parse("https://github.com/owner/repo")
	c := NewGitHubClient("test-token")
	c.client = srv.Client()
	c.apiBaseURL = srv.URL
	c.onEvent = func(eventType string, _ map[string]interface{}) {
		events = append(events, eventType)
	}

	content, _, err := c.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != readmeContent {
		t.Errorf("content: got %q, want %q", content, readmeContent)
	}

	found := false
	for _, e := range events {
		if e == "github_quota_warning" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected github_quota_warning event, got: %v", events)
	}
}

// BT-2: URL with /blob/main/docs/api.md routes to /contents/docs/api.md not /readme.
func TestGitHubClientFetch_FilePathRouting(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(mockContentResponse("# File Content"))
	}))
	defer srv.Close()

	u, _ := url.Parse("https://github.com/owner/repo/blob/main/docs/api.md")
	c := NewGitHubClient("test-token")
	c.client = srv.Client()
	c.apiBaseURL = srv.URL

	_, _, err := c.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/repos/owner/repo/contents/docs/api.md"
	if capturedPath != want {
		t.Errorf("API path: got %q, want %q", capturedPath, want)
	}
}

// BT-3: Token value never appears in any emitted event field.
func TestGitHubClientFetch_TokenRedacted(t *testing.T) {
	const secret = "super-secret-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(mockContentResponse("# Content"))
	}))
	defer srv.Close()

	var allFields []string
	u, _ := url.Parse("https://github.com/owner/repo")
	c := NewGitHubClient(secret)
	c.client = srv.Client()
	c.apiBaseURL = srv.URL
	c.onEvent = func(_ string, metadata map[string]interface{}) {
		for _, v := range metadata {
			allFields = append(allFields, fmt.Sprintf("%v", v))
		}
	}

	if _, _, err := c.Fetch(context.Background(), u); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, f := range allFields {
		if strings.Contains(f, secret) {
			t.Errorf("token leaked in event field: %q", f)
		}
	}
}

// RG-1: ParseGitHubURL with SSH URL without .git suffix returns (owner, repo, "", nil) and never panics.
func TestParseGitHubURL_SSH_NoGitSuffix(t *testing.T) {
	owner, repo, fp, err := ParseGitHubURL("git@github.com:owner/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "owner" || repo != "repo" || fp != "" {
		t.Errorf("got (%q, %q, %q), want (\"owner\", \"repo\", \"\")", owner, repo, fp)
	}
}

// RG-2: DomainRouter with a stubbed GitHubClient returning ErrGitHubRateLimited falls back to jinaFetch.
func TestDomainRouter_RateLimitFallsBackToJina(t *testing.T) {
	stub := &mockDomainClient{
		fetchFn: func(_ context.Context, _ *url.URL) (string, ContentType, error) {
			return "", ContentTypePlain, ErrGitHubRateLimited
		},
	}
	jinaStub := func(_ context.Context, _ string) (string, error) {
		return "jina content", nil
	}

	dr := NewDomainRouter(map[string]DomainClient{"github.com": stub}, jinaStub)
	content, _, err := dr.FetchWithFallback(context.Background(), "https://github.com/owner/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "jina content" {
		t.Errorf("got %q, want \"jina content\"", content)
	}
}
