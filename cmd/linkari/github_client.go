package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrUnsupportedURL    = errors.New("github_unsupported_url")
	ErrInvalidRepoPath   = errors.New("github_invalid_repo_path")
	ErrGitHubSSHParse    = errors.New("github_ssh_parse_error")
	ErrGitHubAuth        = errors.New("github_auth_error")
	ErrGitHubNotFound    = errors.New("github_not_found")
	ErrGitHubRateLimited = errors.New("github_rate_limited")
	ErrGitHubUnexpected  = errors.New("github_unexpected")
)

type GitHubClient struct {
	token      string
	client     *http.Client
	apiBaseURL string
	onEvent    func(eventType string, metadata map[string]interface{})
}

func NewGitHubClient(token string) *GitHubClient {
	return &GitHubClient{
		token:      token,
		client:     &http.Client{},
		apiBaseURL: "https://api.github.com",
	}
}

// ParseGitHubURL parses an HTTPS or SSH GitHub URL into (owner, repo, filepath).
func ParseGitHubURL(rawURL string) (owner, repo, filepath string, err error) {
	if strings.HasPrefix(rawURL, "git@github.com:") {
		remainder := strings.TrimPrefix(rawURL, "git@github.com:")
		idx := strings.Index(remainder, "/")
		if idx < 0 {
			return "", "", "", ErrGitHubSSHParse
		}
		owner = remainder[:idx]
		repo = strings.TrimSuffix(remainder[idx+1:], ".git")
		if owner == "" || repo == "" {
			return "", "", "", ErrGitHubSSHParse
		}
		return owner, repo, "", nil
	}

	u, parseErr := url.Parse(rawURL)
	if parseErr != nil {
		return "", "", "", ErrUnsupportedURL
	}

	host := strings.TrimPrefix(u.Host, "www.")
	if host != "github.com" {
		return "", "", "", ErrUnsupportedURL
	}

	// segments: ["", "owner", "repo", ...]
	segments := strings.Split(u.Path, "/")
	if len(segments) < 2 || segments[1] == "" {
		return "", "", "", ErrUnsupportedURL
	}
	owner = segments[1]
	if len(segments) < 3 || segments[2] == "" {
		return "", "", "", ErrInvalidRepoPath
	}
	repo = segments[2]

	if len(segments) == 3 {
		// Root: /owner/repo
		return owner, repo, "", nil
	}

	// Has additional path segments
	thirdSeg := segments[3]
	if thirdSeg == "" {
		// Trailing slash on repo root
		return owner, repo, "", nil
	}
	if thirdSeg != "blob" {
		return "", "", "", ErrUnsupportedURL
	}

	// /owner/repo/blob/<branch>/<filepath...>
	if len(segments) < 6 {
		// blob with no file path
		return owner, repo, "", nil
	}
	filepath = strings.Join(segments[5:], "/")
	return owner, repo, filepath, nil
}

type githubContentResponse struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// Fetch implements DomainClient for GitHub repositories.
func (c *GitHubClient) Fetch(ctx context.Context, u *url.URL) (string, ContentType, error) {
	start := time.Now()

	owner, repo, filepath, err := ParseGitHubURL(u.String())
	if err != nil {
		return "", ContentTypePlain, err
	}

	var apiURL string
	if filepath == "" {
		apiURL = fmt.Sprintf("%s/repos/%s/%s/readme", c.apiBaseURL, owner, repo)
	} else {
		apiURL = fmt.Sprintf("%s/repos/%s/%s/contents/%s", c.apiBaseURL, owner, repo, filepath)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", ContentTypePlain, fmt.Errorf("%w: %v", ErrGitHubUnexpected, err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		latency := time.Since(start).Milliseconds()
		c.emitEvent("github_fetch_error", map[string]interface{}{
			"url":         redactedHost(u),
			"error_class": "network_error",
			"http_status": 0,
			"latency_ms":  latency,
		})
		return "", ContentTypePlain, err
	}
	defer resp.Body.Close()

	latency := time.Since(start).Milliseconds()

	// Check rate limit header before status code.
	if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining == "0" {
		c.emitEvent("github_fetch_error", map[string]interface{}{
			"url":         redactedHost(u),
			"error_class": "rate_limited",
			"http_status": resp.StatusCode,
			"latency_ms":  latency,
		})
		return "", ContentTypePlain, ErrGitHubRateLimited
	}

	// Emit quota warning when remaining < 100 (but > 0).
	if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining != "" {
		if n, parseErr := strconv.Atoi(remaining); parseErr == nil && n > 0 && n < 100 {
			c.emitEvent("github_quota_warning", map[string]interface{}{
				"url":       redactedHost(u),
				"remaining": n,
			})
		}
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		c.emitEvent("github_fetch_error", map[string]interface{}{
			"url":         redactedHost(u),
			"error_class": "auth_error",
			"http_status": resp.StatusCode,
			"latency_ms":  latency,
		})
		return "", ContentTypePlain, ErrGitHubAuth
	case http.StatusNotFound:
		c.emitEvent("github_fetch_error", map[string]interface{}{
			"url":         redactedHost(u),
			"error_class": "not_found",
			"http_status": resp.StatusCode,
			"latency_ms":  latency,
		})
		return "", ContentTypePlain, ErrGitHubNotFound
	case http.StatusTooManyRequests:
		c.emitEvent("github_fetch_error", map[string]interface{}{
			"url":         redactedHost(u),
			"error_class": "rate_limited",
			"http_status": resp.StatusCode,
			"latency_ms":  latency,
		})
		return "", ContentTypePlain, ErrGitHubRateLimited
	}

	if resp.StatusCode != http.StatusOK {
		c.emitEvent("github_fetch_error", map[string]interface{}{
			"url":         redactedHost(u),
			"error_class": "unexpected",
			"http_status": resp.StatusCode,
			"latency_ms":  latency,
		})
		return "", ContentTypePlain, fmt.Errorf("%w: status %d", ErrGitHubUnexpected, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", ContentTypePlain, fmt.Errorf("%w: read body: %v", ErrGitHubUnexpected, err)
	}

	var ghResp githubContentResponse
	if err := json.Unmarshal(body, &ghResp); err != nil {
		return "", ContentTypePlain, fmt.Errorf("%w: json decode: %v", ErrGitHubUnexpected, err)
	}

	// GitHub base64-encodes content with newline wrapping — strip them before decoding.
	cleaned := strings.ReplaceAll(ghResp.Content, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return "", ContentTypePlain, fmt.Errorf("%w: base64 decode: %v", ErrGitHubUnexpected, err)
	}

	c.emitEvent("github_fetch_complete", map[string]interface{}{
		"url":           redactedHost(u),
		"owner":         owner,
		"repo":          repo,
		"filepath":      filepath,
		"latency_ms":    latency,
		"authenticated": c.token != "",
	})

	return string(decoded), ContentTypeMarkdown, nil
}

func (c *GitHubClient) emitEvent(eventType string, metadata map[string]interface{}) {
	if c.onEvent != nil {
		c.onEvent(eventType, metadata)
	}
}

func redactedHost(u *url.URL) string {
	return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
}
