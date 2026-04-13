package ai

import (
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

// Source identifies the origin system for a piece of data.
type Source string

const (
	SourceJira       Source = "jira"
	SourceConfluence Source = "confluence"
	SourceGitHub     Source = "github"
)

// UserIdentity maps a person across multiple data sources.
type UserIdentity struct {
	Email              string `json:"email,omitempty"`
	AtlassianAccountID string `json:"atlassian_account_id,omitempty"`
	GitHubLogin        string `json:"github_login,omitempty"`
	DisplayName        string `json:"display_name,omitempty"`
}

// ProcessOptions configures the data processing pipeline.
type ProcessOptions struct {
	JiraPath       string        `json:"jira_path,omitempty"`
	ConfluencePath string        `json:"confluence_path,omitempty"`
	GitHubPath     string        `json:"github_path,omitempty"`
	Identity       *UserIdentity `json:"identity,omitempty"`
}

// TimelineEntry represents a single activity from any source.
type TimelineEntry struct {
	Timestamp   time.Time `json:"timestamp"`
	Source      Source    `json:"source"`
	EventType   string    `json:"event_type"`
	Title       string    `json:"title"`
	URL         string    `json:"url,omitempty"`
	IsSynthetic bool      `json:"is_synthetic,omitempty"`

	// Optional back-references to source records
	JiraIssue         *models.Issue             `json:"jira_issue,omitempty"`
	ConfluenceArticle *models.ConfluenceArticle `json:"confluence_article,omitempty"`
	GitHubActivity    *models.GitHubActivity    `json:"github_activity,omitempty"`
}

// DateRange represents a time window for data.
type DateRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// UnifiedTimeline is a sorted collection of timeline entries with metadata.
type UnifiedTimeline struct {
	Entries   []TimelineEntry `json:"entries"`
	DateRange DateRange       `json:"date_range"`
	Sources   []Source        `json:"sources"`
}

// JiraMetrics contains aggregate statistics for Jira issues.
type JiraMetrics struct {
	TotalIssues        int            `json:"total_issues"`
	ByStatus           map[string]int `json:"by_status"`
	ByType             map[string]int `json:"by_type"`
	ByProject          map[string]int `json:"by_project"`
	ResolvedCount      int            `json:"resolved_count"`
	MeanResolutionDays float64        `json:"mean_resolution_days,omitempty"`
}

// ConfluenceMetrics contains aggregate statistics for Confluence articles.
type ConfluenceMetrics struct {
	TotalArticles int            `json:"total_articles"`
	BySpace       map[string]int `json:"by_space"`
	CreatedCount  int            `json:"created_count"`
	EditedCount   int            `json:"edited_count"`
	UniqueSpaces  int            `json:"unique_spaces"`
}

// GitHubMetrics contains aggregate statistics for GitHub activities.
type GitHubMetrics struct {
	TotalActivities int            `json:"total_activities"`
	ByEventType     map[string]int `json:"by_event_type"`
	ByRepo          map[string]int `json:"by_repo"`
	PRsOpened       int            `json:"prs_opened"`
	PRsMerged       int            `json:"prs_merged"`
	PRsReviewed     int            `json:"prs_reviewed"`
	CommitsCount    int            `json:"commits_count"`
	UniqueRepos     int            `json:"unique_repos"`
}

// DataQuality describes the completeness and reliability of processed data.
type DataQuality struct {
	SourcesPresent []Source `json:"sources_present"`
	SourcesMissing []Source `json:"sources_missing"`
	PartialData    bool     `json:"partial_data,omitempty"`
	SyntheticData  bool     `json:"synthetic_data,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

// PerformanceMetrics wraps all per-source metrics with date range and quality info.
type PerformanceMetrics struct {
	DateRange   DateRange          `json:"date_range"`
	Jira        *JiraMetrics       `json:"jira,omitempty"`
	Confluence  *ConfluenceMetrics `json:"confluence,omitempty"`
	GitHub      *GitHubMetrics     `json:"github,omitempty"`
	DataQuality DataQuality        `json:"data_quality"`
}

// ProcessedData is the top-level output of the data processing pipeline.
// It is the interface boundary between raw data (Phases 1-2) and AI narrative generation (Phase 3B).
type ProcessedData struct {
	Identity UserIdentity       `json:"identity"`
	Timeline UnifiedTimeline    `json:"timeline"`
	Metrics  PerformanceMetrics `json:"metrics"`
}
