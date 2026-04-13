package pipeline

import (
	"context"
	"time"
)

// Source is a pluggable data provider scoped to a date range.
// All concrete fetchers (Jira, Confluence, GitHub, FishHistory, AuditLog,
// ClaudeStats) satisfy this interface via adapter types in internal/api/.
type Source interface {
	// Fetch retrieves raw events for the given inclusive date range.
	// Implementations must be context-aware and idempotent.
	// Partial results with a non-nil error are permitted; callers
	// should consume both.
	Fetch(ctx context.Context, start, end time.Time) ([]Event, error)

	// Name returns a stable, lowercase identifier used in log lines
	// and metrics labels (e.g. "jira", "fish_history").
	Name() string
}

// Event is the normalized unit produced by any Source.
// Sources are responsible for mapping domain types (Issue, GitHubActivity,
// ShellCommand) to Event before returning.
type Event struct {
	Source    string // matches Source.Name()
	Timestamp time.Time
	Kind      string // "issue", "pr", "shell_cmd", "ai_call", "audit_event", …
	Payload   any    // original typed value; callers type-assert as needed
}
