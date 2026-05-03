package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// IssueSchemeV2 is the subset of the Jira REST API v2 issue response used by JiraRenderer.
type IssueSchemeV2 struct {
	Key    string         `json:"key"`
	Self   string         `json:"self"`
	Fields *IssueFieldsV2 `json:"fields"`
}

type IssueFieldsV2 struct {
	Summary     string        `json:"summary"`
	Description string        `json:"description"`
	Status      *StatusV2     `json:"status"`
	Assignee    *AssigneeV2   `json:"assignee"`
	Priority    *PriorityV2   `json:"priority"`
	Labels      []string      `json:"labels"`
	Components  []ComponentV2 `json:"components"`
	IssueLinks  []IssueLinkV2 `json:"issuelinks"`
}

type StatusV2 struct {
	Name string `json:"name"`
}

type AssigneeV2 struct {
	DisplayName string `json:"displayName"`
	Name        string `json:"name"`
	AccountID   string `json:"accountId"`
}

type PriorityV2 struct {
	Name string `json:"name"`
}

type ComponentV2 struct {
	Name string `json:"name"`
}

type IssueLinkV2 struct {
	Type         LinkTypeV2   `json:"type"`
	OutwardIssue *LinkedIssue `json:"outwardIssue"`
	InwardIssue  *LinkedIssue `json:"inwardIssue"`
}

type LinkTypeV2 struct {
	Outward string `json:"outward"`
	Inward  string `json:"inward"`
}

type LinkedIssue struct {
	Key string `json:"key"`
}

// JiraRenderer implements CaptureRenderer for Jira issues.
// It is stateless and pure: same input always produces the same bytes.
type JiraRenderer struct{}

func NewJiraRenderer() *JiraRenderer { return &JiraRenderer{} }

// Render implements CaptureRenderer. content must be a valid IssueSchemeV2 JSON string
// with ct == ContentTypeJSON. now is used for the captured_at frontmatter field.
func (r *JiraRenderer) Render(content string, ct ContentType, now time.Time) ([]byte, error) {
	var issue IssueSchemeV2
	if err := json.Unmarshal([]byte(content), &issue); err != nil {
		return nil, fmt.Errorf("render_missing_key: unmarshal: %w", err)
	}
	if issue.Key == "" || issue.Fields == nil {
		return nil, fmt.Errorf("render_missing_key: key or fields missing")
	}

	browseURL := r.buildBrowseURL(issue)
	capturedAt := now.UTC().Format("2006-01-02T15:04Z")

	var b strings.Builder

	// --- frontmatter ---
	b.WriteString("---\n")
	b.WriteString("source: jira\n")
	fmt.Fprintf(&b, "key: %s\n", issue.Key)
	fmt.Fprintf(&b, "summary: %q\n", issue.Fields.Summary)
	b.WriteString("status: ")
	if issue.Fields.Status != nil {
		b.WriteString(issue.Fields.Status.Name)
	}
	b.WriteByte('\n')
	if assignee := r.assigneeName(issue.Fields.Assignee); assignee != "" {
		fmt.Fprintf(&b, "assignee: %s\n", assignee)
	}
	if issue.Fields.Priority != nil && issue.Fields.Priority.Name != "" {
		fmt.Fprintf(&b, "priority: %s\n", issue.Fields.Priority.Name)
	}
	if len(issue.Fields.Labels) > 0 {
		fmt.Fprintf(&b, "labels: [%s]\n", strings.Join(issue.Fields.Labels, ", "))
	}
	if len(issue.Fields.Components) > 0 {
		names := make([]string, len(issue.Fields.Components))
		for i, c := range issue.Fields.Components {
			names[i] = c.Name
		}
		fmt.Fprintf(&b, "components: [%s]\n", strings.Join(names, ", "))
	}
	fmt.Fprintf(&b, "url: %s\n", browseURL)
	fmt.Fprintf(&b, "captured_at: %s\n", capturedAt)
	b.WriteString("---\n\n")

	// --- body ---
	statusStr := ""
	if issue.Fields.Status != nil {
		statusStr = issue.Fields.Status.Name
	}
	assignee := r.assigneeName(issue.Fields.Assignee)
	priorityStr := ""
	if issue.Fields.Priority != nil {
		priorityStr = issue.Fields.Priority.Name
	}

	fmt.Fprintf(&b, "## %s: %s\n\n", issue.Key, issue.Fields.Summary)
	fmt.Fprintf(&b, "**Status:** %s | **Assignee:** %s | **Priority:** %s\n", statusStr, assignee, priorityStr)

	if issue.Fields.Description != "" {
		b.WriteString("\n### Description\n\n")
		b.WriteString(issue.Fields.Description)
		b.WriteByte('\n')
	}

	if len(issue.Fields.IssueLinks) > 0 {
		b.WriteString("\n### Linked Issues\n\n")
		for _, link := range issue.Fields.IssueLinks {
			if link.OutwardIssue != nil {
				fmt.Fprintf(&b, "- %s (%s)\n", link.OutwardIssue.Key, link.Type.Outward)
			} else if link.InwardIssue != nil {
				fmt.Fprintf(&b, "- %s (%s)\n", link.InwardIssue.Key, link.Type.Inward)
			}
		}
	}

	return []byte(b.String()), nil
}

// ArtifactKey implements CaptureRenderer. Extracts the Jira issue key from a browse URL.
func (r *JiraRenderer) ArtifactKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	key, err := ExtractJiraKey(u.Path)
	if err != nil {
		return ""
	}
	return key
}

func (r *JiraRenderer) assigneeName(a *AssigneeV2) string {
	if a == nil {
		return ""
	}
	if a.DisplayName != "" {
		return a.DisplayName
	}
	if a.Name != "" {
		return a.Name
	}
	return a.AccountID
}

func (r *JiraRenderer) buildBrowseURL(issue IssueSchemeV2) string {
	if issue.Self != "" {
		u, err := url.Parse(issue.Self)
		if err == nil {
			return "https://" + u.Host + "/browse/" + issue.Key
		}
	}
	return "/browse/" + issue.Key
}
