// Package types defines shared domain types for the jira-poller service.
package types

import "time"

// TransitionEvent represents a single Jira status-change transition.
// The JSON field names match the epic.md schema and the automation-metrics
// event bus JSONL format.
type TransitionEvent struct {
	IssueKey       string    `json:"issue_key"`
	ProjectKey     string    `json:"project_key"`
	IssueType      string    `json:"issue_type"`
	Summary        string    `json:"summary"`
	FromStatus     string    `json:"from_status"`
	ToStatus       string    `json:"to_status"`
	TransitionedAt time.Time `json:"transitioned_at"`
	TransitionedBy UserRef   `json:"transitioned_by"`
	Assignee       *UserRef  `json:"assignee,omitempty"`
	Labels         []string  `json:"labels"`
	ChangelogID    string    `json:"changelog_id"`
	Self           string    `json:"self"`
}

// UserRef is a minimal Jira user identity, used for TransitionedBy and Assignee.
type UserRef struct {
	AccountID   string `json:"account_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
}
