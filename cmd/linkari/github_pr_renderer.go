package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidRepoPath is returned by ParseGitHubPRURL when owner or repo segment is empty.
// Note: ErrInvalidRepoPath is already defined in github_client.go — reuse it.

// githubPRResponse is the GitHub REST API v3 pull request response shape.
type githubPRResponse struct {
	Number       int          `json:"number"`
	Title        string       `json:"title"`
	Body         string       `json:"body"`
	State        string       `json:"state"`
	MergedAt     *string      `json:"merged_at"`
	HTMLURL      string       `json:"html_url"`
	User         githubUser   `json:"user"`
	Reviewers    []githubUser `json:"requested_reviewers"`
	ChangedFiles int          `json:"changed_files"`
	Additions    int          `json:"additions"`
	Deletions    int          `json:"deletions"`
}

// githubUser is a minimal GitHub user object embedded in PR responses.
type githubUser struct {
	Login string `json:"login"`
}

// GitHubPRRenderer converts GitHub REST API v3 pull request JSON responses to markdown artifacts.
// It implements CaptureRenderer and is stateless and pure — no network fields.
type GitHubPRRenderer struct{}

// NewGitHubPRRenderer returns a new GitHubPRRenderer.
func NewGitHubPRRenderer() *GitHubPRRenderer { return &GitHubPRRenderer{} }

// ParseGitHubPRURL parses a GitHub pull request URL into (owner, repo, prNumber).
// Accepts: "https://github.com/{owner}/{repo}/pull/{number}"
// Returns ErrUnsupportedURL if the URL is not a GitHub PR URL.
// Returns ErrInvalidRepoPath if owner or repo segment is empty.
// Returns a formatted error if the pull number segment is non-numeric or missing.
func ParseGitHubPRURL(rawURL string) (owner, repo string, prNumber int, err error) {
	u, parseErr := url.Parse(rawURL)
	if parseErr != nil {
		return "", "", 0, ErrUnsupportedURL
	}

	host := strings.TrimPrefix(u.Host, "www.")
	if host != "github.com" {
		return "", "", 0, ErrUnsupportedURL
	}

	// Path segments: ["", "owner", "repo", "pull", "number"]
	segments := strings.Split(u.Path, "/")
	// segments[0] is always empty (leading slash)
	if len(segments) < 5 {
		return "", "", 0, ErrUnsupportedURL
	}

	owner = segments[1]
	repo = segments[2]
	pullSegment := segments[3]
	numberSegment := segments[4]

	if owner == "" || repo == "" {
		return "", "", 0, ErrInvalidRepoPath
	}

	if pullSegment != "pull" {
		return "", "", 0, ErrUnsupportedURL
	}

	if numberSegment == "" {
		return "", "", 0, ErrUnsupportedURL
	}

	n, convErr := strconv.Atoi(numberSegment)
	if convErr != nil {
		return "", "", 0, fmt.Errorf("github_pr_invalid_number: %q is not a valid PR number", numberSegment)
	}

	return owner, repo, n, nil
}

// ArtifactKey implements CaptureRenderer.
// Extracts owner, repo, and PR number from a GitHub PR URL.
// "https://github.com/owner/repo/pull/42" → "owner-repo-42"
// Returns "" on parse error.
func (r *GitHubPRRenderer) ArtifactKey(rawURL string) string {
	owner, repo, prNumber, err := ParseGitHubPRURL(rawURL)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s-%s-%d", owner, repo, prNumber)
}

// Render implements CaptureRenderer.
// content must be valid githubPRResponse JSON; ct must be ContentTypeJSON.
// now is used for the captured_at frontmatter field.
func (r *GitHubPRRenderer) Render(content string, ct ContentType, now time.Time) ([]byte, error) {
	if ct != ContentTypeJSON {
		return nil, fmt.Errorf("render_missing_pr_number: unsupported content type %v", ct)
	}

	var pr githubPRResponse
	if err := json.Unmarshal([]byte(content), &pr); err != nil {
		return nil, fmt.Errorf("github_pr_decode_response_error: %w", err)
	}

	if pr.Title == "" {
		return nil, fmt.Errorf("render_missing_title: title field is empty")
	}

	if pr.Number == 0 {
		return nil, fmt.Errorf("render_missing_pr_number: pr number field is zero")
	}

	// Compute state: "merged" if merged_at is non-nil, otherwise use pr.State.
	state := pr.State
	if pr.MergedAt != nil {
		state = "merged"
	}

	// Extract repo from HTML URL for frontmatter; fallback to html_url parsing.
	repoStr := ""
	owner, repoName, _, parseErr := ParseGitHubPRURL(pr.HTMLURL)
	if parseErr == nil {
		repoStr = owner + "/" + repoName
	}

	capturedAt := now.UTC().Format(time.RFC3339)

	var b strings.Builder

	// YAML frontmatter.
	b.WriteString("---\n")
	b.WriteString("source: github_pr\n")
	fmt.Fprintf(&b, "repo: %s\n", repoStr)
	fmt.Fprintf(&b, "pr_number: %d\n", pr.Number)
	fmt.Fprintf(&b, "title: %q\n", pr.Title)
	fmt.Fprintf(&b, "state: %s\n", state)
	fmt.Fprintf(&b, "author: %s\n", pr.User.Login)

	// reviewers: omit field entirely if empty.
	if len(pr.Reviewers) > 0 {
		logins := make([]string, len(pr.Reviewers))
		for i, rv := range pr.Reviewers {
			logins[i] = rv.Login
		}
		fmt.Fprintf(&b, "reviewers: [%s]\n", strings.Join(logins, ", "))
	}

	// merged_at: RFC3339 string or YAML null (~).
	if pr.MergedAt != nil {
		fmt.Fprintf(&b, "merged_at: %s\n", *pr.MergedAt)
	} else {
		b.WriteString("merged_at: ~\n")
	}

	fmt.Fprintf(&b, "url: %s\n", pr.HTMLURL)
	fmt.Fprintf(&b, "captured_at: %s\n", capturedAt)
	b.WriteString("---\n")

	// Body.
	b.WriteString("\n")
	fmt.Fprintf(&b, "## PR #%d: %s\n\n", pr.Number, pr.Title)
	fmt.Fprintf(&b, "**State:** %s | **Author:** %s | **Repo:** %s\n", state, pr.User.Login, repoStr)

	// Description section — only if body is non-empty.
	if pr.Body != "" {
		b.WriteString("\n### Description\n\n")
		b.WriteString(pr.Body)
		b.WriteString("\n")
	}

	// Diff Summary section — only if any stat is non-zero.
	if pr.ChangedFiles != 0 || pr.Additions != 0 || pr.Deletions != 0 {
		b.WriteString("\n### Diff Summary\n\n")
		fmt.Fprintf(&b, "**Files changed:** %d | **Additions:** +%d | **Deletions:** -%d\n",
			pr.ChangedFiles, pr.Additions, pr.Deletions)
	}

	// Reviewers section — only if reviewers is non-empty.
	if len(pr.Reviewers) > 0 {
		b.WriteString("\n### Reviewers\n\n")
		for _, rv := range pr.Reviewers {
			fmt.Fprintf(&b, "- %s\n", rv.Login)
		}
	}

	return []byte(b.String()), nil
}

// compile-time assertion — GitHubPRRenderer implements CaptureRenderer.
var _ CaptureRenderer = (*GitHubPRRenderer)(nil)
