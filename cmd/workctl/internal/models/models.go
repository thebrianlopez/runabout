package models

import "time"

// QueryMode defines the type of query to execute
type QueryMode int

const (
	UserMode    QueryMode = iota // Query by user email (existing behavior)
	ProjectMode                  // Query by Jira project keys
	SpaceMode                    // Query by Confluence space keys
	MixedMode                    // Query by projects + spaces (no user)
	GitHubMode                   // Query by GitHub username
)

// QueryConfig holds the query configuration
type QueryConfig struct {
	Mode        QueryMode
	Email       string
	ProjectKeys []string
	SpaceKeys   []string
	GitHubUser  string // GitHub username
	StartDate   string
	EndDate     string
	TimeZone    string
	Debug       bool
	// Jira filters
	JiraStatus   []string // Filter by Jira status (e.g., Done, In Progress)
	JiraType     []string // Filter by Jira issue type (e.g., Story, Bug)
	JiraPriority []string // Filter by Jira priority (e.g., High, Critical)
	// Confluence filters
	ConfluenceType    string // Filter by Confluence content type (page, blogpost)
	ConfluenceHydrate bool   // Enable metadata hydration for Creator/LastEditor (slower)
	// GitHub API strategy
	GitHubAPIStrategy string // API strategy: auto, events, search, graphql (default: auto)
	// GitHub commit history (Commits API, repo-targeted)
	GitHubRepos  []string // Repos to fetch commit history from (e.g. org/repo1,org/repo2)
	GitHubEnrich bool     // Hydrate commits with per-file diff stats (slower)
	// Output options
	Summary      bool   // Generate summary statistics
	OutputFormat string // Output format: "json" (CSV deprecated)
}

// Issue represents a Jira issue for CSV export
type Issue struct {
	ID                string // Numeric Jira issue ID
	Key               string
	URL               string // Direct link: https://domain/browse/KEY
	ProjectKey        string // Project key for project-mode queries
	Assignee          string // Assignee display name
	AssigneeEmail     string // Assignee email (if available)
	AssigneeAccountID string // Stable Atlassian account ID
	IssueType         string // Issue type (Story, Bug, Task, etc.)
	Fields            struct {
		Summary  string
		Created  string
		Updated  string
		Resolved string
		Status   struct {
			Name string
		}
	}
}

// ConfluenceArticle represents a Confluence page for CSV export
type ConfluenceArticle struct {
	ID                  string
	Title               string
	URL                 string // Direct link: https://domain/wiki/spaces/KEY/pages/ID
	SpaceKey            string // Space key
	SpaceName           string // Space name
	Creator             string // Creator display name
	CreatorEmail        string // Creator email (if available)
	CreatorAccountID    string // Stable Atlassian account ID for creator
	LastEditor          string // Last editor display name
	LastEditorAccountID string // Last editor account ID (stable identifier)
	Body                struct {
		Storage struct {
			Value string
		}
	}
	CreatedBy struct {
		AccountID string
	}
	CreatedDate      string
	LastModifiedDate string // Last modified date
}

// ShellCommand represents a command from fish shell history.
type ShellCommand struct {
	Cmd       string    // Raw command text (sensitive values redacted)
	Timestamp time.Time // When the command ran
	Paths     []string  // File paths referenced by the command (from fish `paths:` field)
	Binary    string    // Derived: first non-env-var token (executable name)
	Category  string    // Derived: "kubernetes", "terraform", "aws", "git", "docker", "general"
	IsInfra   bool      // True if command is infrastructure-related
	IsDeploy  bool      // True if command contains deploy/apply/release verbs
}

// AuditEvent represents a command from the terminal audit log.
// Supports both 2025 (v1, no session_id/cwd) and 2026 (v2, full) schema versions.
type AuditEvent struct {
	Timestamp time.Time
	Command   string // Raw command text (sensitive values redacted)
	SessionID string // Empty in 2025 (v1) schema
	Cwd       string // Working directory; empty in 2025 (v1) schema
	Source    string // "interactive_shell" or "claude_code"
	ToolName  string // "Bash" for claude_code events; empty otherwise
}

// AIActivity represents daily AI assistant activity from the Claude stats cache.
type AIActivity struct {
	Date          string // "YYYY-MM-DD"
	MessageCount  int
	SessionCount  int
	ToolCallCount int
	TokensUsed    int // Sum of all model tokens for the day (joined from dailyModelTokens)
}

// SessionSummary represents a session_summary event from the automation-metrics
// events store. Each record captures aggregated stats for one Claude Code session.
type SessionSummary struct {
	SessionID            string
	Cwd                  string
	Timestamp            time.Time
	TotalEvents          int
	ToolEvents           int
	PromptCount          int
	UniqueCommands       int
	ToolDistribution     map[string]int // tool name → call count (e.g. {"Bash": 53, "Read": 12})
	GraduationCandidates int
	FirstEvent           time.Time
	LastEvent            time.Time
	CostEstimateUSD      float64 // accumulated from inference events in same session
}

// TypedEventBatch holds all parsed events from the automation-metrics events store
// for a given date range. EventsClient.GetTypedEvents returns this struct, replacing
// the dual-pass approach of AuditLogClient.GetEvents + GetSessionSummaries.
type TypedEventBatch struct {
	ShellEvents      []ShellEvent
	ToolCallEvents   []ToolCallEvent
	InferenceEvents  []InferenceEvent
	SessionSummaries []SessionSummary
}

// ShellEvent represents a command emitted by any layer of the automation topology.
// Covers event_type values that carry a non-empty Command field (e.g. "shell_command",
// legacy v1/v2 events without an explicit event_type).
type ShellEvent struct {
	Timestamp time.Time
	Command   string // Sensitive values redacted
	SessionID string
	Cwd       string
	Layer     string // Preserved as-is from schema: "fish", "cloud_llm", "go_cli", etc.
	ToolName  string // Non-empty for claude_code tool invocations
}

// ToolCallEvent represents a tool_result event from the claude_code layer.
// These events record individual tool invocations inside a Claude Code session.
type ToolCallEvent struct {
	Timestamp           time.Time
	SessionID           string
	Cwd                 string
	ToolName            string
	Command             string
	FirstWord           string
	GraduationCandidate bool
}

// InferenceEvent represents a single LLM inference call (event_type == "inference").
// Cost and token fields are estimates only.
type InferenceEvent struct {
	Timestamp       time.Time
	SessionID       string
	CostEstimateUSD float64
	TokenEstimate   int
}

// RateLimiter wraps a time.Ticker for API rate limiting
type RateLimiter struct {
	Ticker *time.Ticker
}

// NewRateLimiter creates a new rate limiter with 1 request per second
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		Ticker: time.NewTicker(time.Second),
	}
}

// GitHubActivity represents a GitHub event for CSV export
type GitHubActivity struct {
	EventID     string
	EventType   string // PushEvent, PullRequestEvent, CommitEvent, etc.
	ActorLogin  string // GitHub username (stable identifier)
	Repository  string // org/repo
	Timestamp   time.Time
	Description string // Human-readable summary
	URL         string // Link to event
	Public      bool   // Visibility flag
	// Commit-specific fields (populated for CommitEvent records)
	CommitSHA     string   // Full commit SHA (also used as EventID for CommitEvents)
	CommitMessage string   // First line of commit message
	FilesChanged  []string `json:"files_changed,omitempty"`
	LinesAdded    int      `json:"lines_added,omitempty"`
	LinesRemoved  int      `json:"lines_removed,omitempty"`
	Enriched      bool     `json:"enriched,omitempty"`
}
