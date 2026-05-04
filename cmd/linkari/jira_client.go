package main

// EPIC-072 M10: Jira draft ticket creation client.
// EPIC-013 M1: DomainClient implementation for Jira issues and Confluence pages.
// Uses Jira REST API v3 (create) and v2 (read) with basic auth.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var (
	ErrAtlassianAuth        = errors.New("atlassian_auth_error")
	ErrAtlassianNotFound    = errors.New("atlassian_not_found")
	ErrAtlassianUnsupported = errors.New("atlassian_unsupported_url")
)

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// JiraClient creates draft issues via Jira REST API v3 and reads issues/pages via v2.
type JiraClient struct {
	Domain     string // e.g. "yourorg.atlassian.net"
	Username   string // email
	Password   string // API token
	baseURL    string // override for tests; empty = "https://<Domain>"
	httpClient *http.Client
}

// compile-time assertion
var _ DomainClient = (*JiraClient)(nil)

func (c *JiraClient) base() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return "https://" + c.Domain
}

func (c *JiraClient) client() *http.Client {
	if c.httpClient != nil {
		return c.httpClient
	}
	return http.DefaultClient
}

// ExtractJiraKey extracts the Jira issue key from a browse URL path.
// "/browse/SR-2972" → "SR-2972". Validates the key against jiraKeyRegex
// to reject shell metacharacters before any downstream use.
func ExtractJiraKey(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, "/browse/")
	if !ok {
		return "", fmt.Errorf("jira_url_not_browse: %q", path)
	}
	key := strings.SplitN(rest, "/", 2)[0]
	if !jiraKeyRegex.MatchString(key) {
		return "", fmt.Errorf("jira_key_invalid: %q", key)
	}
	return key, nil
}

// ParseAtlassianURL extracts (issueKey, pageID) from *.atlassian.net URLs.
// Exactly one of issueKey/pageID is non-empty on success.
func ParseAtlassianURL(u *url.URL) (issueKey, pageID string, err error) {
	path := u.Path
	// /browse/{KEY}
	if rest, ok := strings.CutPrefix(path, "/browse/"); ok {
		key := strings.SplitN(rest, "/", 2)[0]
		if key == "" {
			return "", "", ErrAtlassianUnsupported
		}
		return key, "", nil
	}
	// /wiki/spaces/{S}/pages/{ID}[/...]
	if rest, ok := strings.CutPrefix(path, "/wiki/spaces/"); ok {
		parts := strings.SplitN(rest, "/", 5) // [S, "pages", ID, ...]
		if len(parts) >= 3 && parts[1] == "pages" && parts[2] != "" {
			return "", parts[2], nil
		}
	}
	return "", "", ErrAtlassianUnsupported
}

// Fetch implements DomainClient.
func (c *JiraClient) Fetch(ctx context.Context, u *url.URL) (string, ContentType, error) {
	issueKey, pageID, err := ParseAtlassianURL(u)
	if err != nil {
		return "", ContentTypePlain, err
	}
	if issueKey != "" {
		content, err := c.FetchIssue(ctx, issueKey)
		return content, ContentTypeJSON, err
	}
	content, err := c.FetchConfluencePage(ctx, pageID)
	return content, ContentTypePlain, err
}

// FetchIssue returns the raw JSON body of a Jira issue via REST API v2.
func (c *JiraClient) FetchIssue(ctx context.Context, issueKey string) (string, error) {
	endpoint := fmt.Sprintf("%s/rest/api/2/issue/%s", c.base(), issueKey)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("jira_fetch_issue: %w", err)
	}
	req.SetBasicAuth(c.Username, c.Password)

	resp, err := c.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("jira_fetch_issue: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", ErrAtlassianAuth
	case http.StatusNotFound:
		return "", ErrAtlassianNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("jira_fetch_issue: status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("jira_fetch_issue: read: %w", err)
	}
	return string(raw), nil
}

// FetchConfluenceADF fetches Confluence page metadata and ADF body via REST API v1.
// Returns the full JSON response as a string for renderer parsing.
// Errors: ErrAtlassianAuth (401/403), ErrAtlassianNotFound (404), wrapped network errors.
func (c *JiraClient) FetchConfluenceADF(ctx context.Context, pageID string) (string, error) {
	return "", nil // stub — implemented in M2
}

// FetchConfluencePage returns plain text via Confluence REST API.
func (c *JiraClient) FetchConfluencePage(ctx context.Context, pageID string) (string, error) {
	endpoint := fmt.Sprintf("%s/wiki/rest/api/content/%s?expand=body.view", c.base(), pageID)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("confluence_fetch_page: %w", err)
	}
	req.SetBasicAuth(c.Username, c.Password)

	resp, err := c.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("confluence_fetch_page: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", ErrAtlassianAuth
	case http.StatusNotFound:
		return "", ErrAtlassianNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("confluence_fetch_page: status %d", resp.StatusCode)
	}

	var body struct {
		Body struct {
			View struct {
				Value string `json:"value"`
			} `json:"view"`
		} `json:"body"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("confluence_fetch_page: decode: %w", err)
	}

	plain := htmlTagRe.ReplaceAllString(body.Body.View.Value, "")
	plain = strings.Join(strings.Fields(plain), " ")
	return plain, nil
}

// JiraIssue represents the response from creating an issue.
type JiraIssue struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Self string `json:"self"`
}

// CreateDraftTicket creates a Jira issue with the given summary and description.
// Returns the issue key and browse URL.
func (c *JiraClient) CreateDraftTicket(ctx context.Context, projectKey, summary, description string) (string, string, error) {
	if c.Domain == "" || c.Username == "" || c.Password == "" {
		return "", "", fmt.Errorf("jira client not configured (domain=%q, username=%q)", c.Domain, c.Username)
	}

	body := map[string]interface{}{
		"fields": map[string]interface{}{
			"project":     map[string]string{"key": projectKey},
			"summary":     summary,
			"description": descriptionToADF(description),
			"issuetype":   map[string]string{"name": "Task"},
			"labels":      []string{"linkari-auto-draft"},
		},
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", "", fmt.Errorf("marshal issue: %w", err)
	}

	url := fmt.Sprintf("https://%s/rest/api/3/issue", c.Domain)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return "", "", fmt.Errorf("create request: %w", err)
	}
	req.SetBasicAuth(c.Username, c.Password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("jira request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", "", fmt.Errorf("jira API %d: %s", resp.StatusCode, string(respBody))
	}

	var issue JiraIssue
	if err := json.Unmarshal(respBody, &issue); err != nil {
		return "", "", fmt.Errorf("decode issue: %w", err)
	}

	browseURL := fmt.Sprintf("https://%s/browse/%s", c.Domain, issue.Key)
	return issue.Key, browseURL, nil
}

// descriptionToADF converts a plain text description to Atlassian Document Format (ADF).
func descriptionToADF(text string) map[string]interface{} {
	paragraphs := strings.Split(text, "\n\n")
	content := make([]interface{}, 0, len(paragraphs))
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		content = append(content, map[string]interface{}{
			"type": "paragraph",
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": p,
				},
			},
		})
	}
	return map[string]interface{}{
		"type":    "doc",
		"version": 1,
		"content": content,
	}
}

// draftJiraTicket is the M10 handler wired into dispatchActionRoute.
func draftJiraTicket(ctx context.Context, sc *Scorecard, profile, url string, q *Queue, itemID int64, cfg *ServerConfig) {
	if cfg == nil || cfg.JiraDomain == "" || cfg.JiraAPIUsername == "" || cfg.JiraAPIPassword == "" {
		slog.DebugContext(ctx, "action_route: jira not configured, skipping draft")
		return
	}

	client := &JiraClient{
		Domain:   cfg.JiraDomain,
		Username: cfg.JiraAPIUsername,
		Password: cfg.JiraAPIPassword,
	}

	summary := fmt.Sprintf("[Linkari] %s — score %d", truncateString(sc.Verdict, 80), sc.Score)
	description := fmt.Sprintf("URL: %s\n\nProfile: %s\nScore: %d\n\nVerdict:\n%s",
		url, profile, sc.Score, sc.Verdict)

	issueKey, browseURL, err := client.CreateDraftTicket(ctx, "LINK", summary, description)
	if err != nil {
		slog.WarnContext(ctx, "action_route: jira draft failed",
			"id", itemID,
			"error", err,
		)
		return
	}

	// Store draft URL in queue item for client retrieval.
	q.db.Exec("UPDATE queue SET action_route=? WHERE id=?",
		fmt.Sprintf("draft_jira_ticket:%s", browseURL), itemID)

	slog.InfoContext(ctx, "action_route: jira draft created",
		"event_type", "jira_draft_created",
		"id", itemID,
		"issue_key", issueKey,
		"url", browseURL,
	)
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
