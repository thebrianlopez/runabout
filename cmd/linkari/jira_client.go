package main

// EPIC-072 M10: Jira draft ticket creation client.
// Uses Jira REST API v3 with basic auth (email:API token).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// JiraClient creates draft issues via Jira REST API v3.
type JiraClient struct {
	Domain   string // e.g. "yourorg.atlassian.net"
	Username string // email
	Password string // API token
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
