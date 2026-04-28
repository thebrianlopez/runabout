// Package jiraclient provides a typed Client interface wrapping go-atlassian
// for searching Jira issue transitions. Consumers (F4 poller core) depend only
// on this interface — go-atlassian is an implementation detail.
package jiraclient

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors. Callers use errors.Is to classify failures.
var (
	// ErrAuthFailure is returned on 401 or 403. Not retried.
	ErrAuthFailure = errors.New("jiraclient: auth failure")

	// ErrRateLimited is returned on 429 after max retries. Next poll will retry.
	ErrRateLimited = errors.New("jiraclient: rate limited")

	// ErrUpstream is returned on 5xx or unexpected 4xx after max retries.
	ErrUpstream = errors.New("jiraclient: upstream error")

	// ErrNotFound is returned on 404. Not retried.
	ErrNotFound = errors.New("jiraclient: not found")

	// ErrPagination is returned when the nextPageToken is malformed.
	ErrPagination = errors.New("jiraclient: pagination error")

	// ErrTimeout is returned when the HTTP client times out.
	ErrTimeout = errors.New("jiraclient: request timeout")
)

// Client is the only surface F4 depends on.
type Client interface {
	// SearchTransitions returns issues with status-field changelog entries
	// matching the JQL. Caller loops on NextToken until it is empty.
	SearchTransitions(ctx context.Context, req SearchRequest) (*SearchResult, error)
}

// SearchRequest parameterises a single page of the JQL search.
type SearchRequest struct {
	// Projects contains Jira project keys, e.g. ["INFRA", "PLAT"].
	Projects []string

	// LookbackMinutes is used to build the JQL predicate:
	// status CHANGED AFTER "-{N}m"
	LookbackMinutes int

	// MaxResults is the page size. Defaults to 100 if zero.
	MaxResults int

	// NextToken is empty on the first call; taken from the previous SearchResult.
	NextToken string
}

// SearchResult is one page of results.
type SearchResult struct {
	Issues []Issue

	// NextToken is empty when there are no more pages.
	NextToken string
}

// Issue is a minimal view of a Jira issue relevant to transition events.
type Issue struct {
	Key       string
	Summary   string
	IssueType string
	Self      string

	// Assignee is nil if the issue is unassigned or the assignee is privacy-hidden.
	Assignee  *User
	Labels    []string
	Changelog []ChangelogEntry
}

// ChangelogEntry is one history item where field == "status".
// Non-status items are filtered before reaching the caller.
type ChangelogEntry struct {
	HistoryID  string    // go-atlassian history ID; used as dedup key component
	Created    time.Time
	Author     User
	FromStatus string // empty string when the issue was just created (null in Jira API)
	ToStatus   string
}

// User identifies a Jira user.
type User struct {
	AccountID   string
	DisplayName string

	// Email may be empty when Atlassian privacy settings hide it.
	Email string
}
