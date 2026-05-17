package insights

import (
	"sort"
	"strings"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// Theme categorizes work items by type of activity.
type Theme string

const (
	ThemeBug            Theme = "Bug"
	ThemeFeature        Theme = "Feature"
	ThemeInfrastructure Theme = "Infrastructure"
	ThemeIncident       Theme = "Incident"
	ThemeMaintenance    Theme = "Maintenance"
	ThemeOther          Theme = "Other"
)

// MonthBucket holds counts for a calendar month.
type MonthBucket struct {
	Month   string `json:"month"` // "2026-01"
	Created int    `json:"created"`
	Closed  int    `json:"closed"`
}

// FocusItem represents a project, repo, or space with activity count.
type FocusItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// CollaborationSignals captures cross-team and review activity.
type CollaborationSignals struct {
	PRReviews       int `json:"pr_reviews"`        // PullRequestReviewEvent count
	IssueComments   int `json:"issue_comments"`    // IssueCommentEvent count
	CrossTeamIssues int `json:"cross_team_issues"` // Issues in projects other than the top project
	UniqueRepos     int `json:"unique_repos"`      // Distinct repos contributed to
}

// OwnershipSignals captures ownership-related patterns.
type OwnershipSignals struct {
	TotalClosed        int     `json:"total_closed"`         // Issues in Done/Closed status
	HighPriorityClosed int     `json:"high_priority_closed"` // High/Critical priority closed
	IncidentRatio      float64 `json:"incident_ratio"`       // Fraction of incidents vs total
}

// ShellActivitySignals captures signals from fish shell history and terminal audit log.
type ShellActivitySignals struct {
	TotalCommands    int            `json:"total_commands"`
	DaysActive       int            `json:"days_active"`
	InfraCommands    int            `json:"infra_commands"`    // commands with infrastructure binaries (kubectl, terraform, aws…)
	DeployCommands   int            `json:"deploy_commands"`   // commands containing deploy-indicating verbs
	ToolCounts       map[string]int `json:"tool_counts"`       // binary name → count (top tools used)
	CategoryCounts   map[string]int `json:"category_counts"`   // category → count (kubernetes, terraform, aws, git, docker, general)
	HourDistribution [24]int        `json:"hour_distribution"` // commands per hour of day (local time, index = hour 0–23)
	WeekdayActivity  [7]int         `json:"weekday_activity"`  // commands per weekday (index 0 = Sunday, per time.Weekday)
	// RepoFocus deferred to M5 (requires git-root resolution from shell history paths).
}

// AIActivitySignals captures signals from the Claude Code stats cache and audit log.
type AIActivitySignals struct {
	TotalSessions  int         `json:"total_sessions"`
	TotalMessages  int         `json:"total_messages"`
	TotalToolCalls int         `json:"total_tool_calls"`
	TotalTokens    int         `json:"total_tokens"`
	DaysActive     int         `json:"days_active"`    // days with at least one session
	HumanCommands  int         `json:"human_commands"` // audit log events with source="interactive_shell"
	AgentCommands  int         `json:"agent_commands"` // audit log events with source="claude_code"
	AgentProjects  []FocusItem `json:"agent_projects"` // working directories used during claude_code sessions (sorted by count)

	// EPIC-019 M1+M2: events-native signals from session_summary + inference events
	EventSessions         int            `json:"event_sessions"`           // count of session_summary events
	AvgSessionDurationMin float64        `json:"avg_session_duration_min"` // average session duration in minutes
	TotalCostUSD          float64        `json:"total_cost_usd"`           // accumulated cost from inference events
	ToolDistribution      map[string]int `json:"tool_distribution"`        // aggregated tool name → call count across all sessions
	GraduationCandidates  int            `json:"graduation_candidates"`    // total graduation candidates across all sessions

	// EPIC-019 M3: layer-aware breakdown (counts per source/layer)
	LayerBreakdown map[string]int `json:"layer_breakdown"` // source value → count (e.g. interactive_shell, claude_code, cloud_llm, fish, go_cli)
}

// SignalSet is the complete set of extracted career signals.
type SignalSet struct {
	// Theme distribution
	ThemeCounts map[Theme]int `json:"theme_counts"`

	// Velocity (monthly buckets sorted chronologically)
	Velocity []MonthBucket `json:"velocity"`

	// Focus distribution
	ProjectFocus []FocusItem `json:"project_focus"`
	RepoFocus    []FocusItem `json:"repo_focus"`
	SpaceFocus   []FocusItem `json:"space_focus"`

	// Collaboration
	Collaboration CollaborationSignals `json:"collaboration"`

	// Ownership
	Ownership OwnershipSignals `json:"ownership"`

	// Raw counts for delta comparison
	TotalIssues     int `json:"total_issues"`
	TotalArticles   int `json:"total_articles"`
	TotalActivities int `json:"total_activities"`

	// Local activity (EPIC-015); nil when local sources are disabled or unavailable.
	ShellActivity *ShellActivitySignals `json:"shell_activity,omitempty"`
	AIActivity    *AIActivitySignals    `json:"ai_activity,omitempty"`

	// EPIC-020 M1-M3: session and topology signals; nil when no session data.
	SessionSignals  *SessionSignals  `json:"session_signals,omitempty"`
	TopologySignals *TopologySignals `json:"topology_signals,omitempty"`
}

// ExtractSignals aggregates career signals from fetched data.
func ExtractSignals(issues []models.Issue, articles []models.ConfluenceArticle, activities []models.GitHubActivity) *SignalSet {
	s := &SignalSet{
		ThemeCounts:     make(map[Theme]int),
		TotalIssues:     len(issues),
		TotalArticles:   len(articles),
		TotalActivities: len(activities),
	}

	classifyThemes(s, issues)
	computeVelocity(s, issues)
	computeProjectFocus(s, issues)
	computeSpaceFocus(s, articles)
	computeRepoFocus(s, activities)
	extractCollaboration(s, activities)
	analyzeOwnership(s, issues)

	return s
}

// classifyThemes maps each Jira issue to a theme based on issue type.
func classifyThemes(s *SignalSet, issues []models.Issue) {
	for _, issue := range issues {
		theme := ClassifyTheme(issue.IssueType, issue.Fields.Summary)
		s.ThemeCounts[theme]++
	}
}

// ClassifyTheme determines a theme from Jira issue type and summary.
func ClassifyTheme(issueType, summary string) Theme {
	lower := strings.ToLower(issueType)
	summaryLower := strings.ToLower(summary)

	switch {
	case lower == "bug":
		return ThemeBug
	case lower == "incident" || strings.Contains(summaryLower, "incident") || strings.Contains(summaryLower, "outage"):
		return ThemeIncident
	case lower == "story" || lower == "feature" || lower == "new feature":
		return ThemeFeature
	case lower == "task" || lower == "sub-task":
		// Disambiguate: infra-related tasks vs maintenance
		if isInfraRelated(summaryLower) {
			return ThemeInfrastructure
		}
		return ThemeMaintenance
	case lower == "epic":
		return ThemeFeature
	case isInfraRelated(lower) || isInfraRelated(summaryLower):
		return ThemeInfrastructure
	default:
		return ThemeOther
	}
}

// isInfraRelated checks if text contains infrastructure-related keywords.
func isInfraRelated(text string) bool {
	keywords := []string{
		"terraform", "kubernetes", "k8s", "helm", "deploy", "infra",
		"pipeline", "ci/cd", "cicd", "migration", "database", "cluster",
		"network", "vpc", "iam", "s3", "eks", "rds", "kafka", "aws",
	}
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// computeVelocity buckets issues by created/closed month.
func computeVelocity(s *SignalSet, issues []models.Issue) {
	created := make(map[string]int)
	closed := make(map[string]int)
	months := make(map[string]bool)

	for _, issue := range issues {
		if t, err := parseDate(issue.Fields.Created); err == nil {
			m := t.Format("2006-01")
			created[m]++
			months[m] = true
		}
		if issue.Fields.Resolved != "" {
			if t, err := parseDate(issue.Fields.Resolved); err == nil {
				m := t.Format("2006-01")
				closed[m]++
				months[m] = true
			}
		}
	}

	// Sort months chronologically
	sorted := make([]string, 0, len(months))
	for m := range months {
		sorted = append(sorted, m)
	}
	sort.Strings(sorted)

	s.Velocity = make([]MonthBucket, len(sorted))
	for i, m := range sorted {
		s.Velocity[i] = MonthBucket{
			Month:   m,
			Created: created[m],
			Closed:  closed[m],
		}
	}
}

// computeProjectFocus counts issues per project.
func computeProjectFocus(s *SignalSet, issues []models.Issue) {
	counts := make(map[string]int)
	for _, issue := range issues {
		key := issue.ProjectKey
		if key == "" {
			key = "Unknown"
		}
		counts[key]++
	}
	s.ProjectFocus = sortedFocusItems(counts)
}

// computeSpaceFocus counts articles per Confluence space.
func computeSpaceFocus(s *SignalSet, articles []models.ConfluenceArticle) {
	counts := make(map[string]int)
	for _, article := range articles {
		key := article.SpaceKey
		// Personal spaces have keys like "~accountId"; show display name instead.
		if strings.HasPrefix(key, "~") && article.SpaceName != "" {
			key = article.SpaceName
		}
		if key == "" {
			key = "Unknown"
		}
		counts[key]++
	}
	s.SpaceFocus = sortedFocusItems(counts)
}

// computeRepoFocus counts GitHub activities per repository.
func computeRepoFocus(s *SignalSet, activities []models.GitHubActivity) {
	counts := make(map[string]int)
	for _, a := range activities {
		repo := a.Repository
		if repo == "" {
			repo = "Unknown"
		}
		counts[repo]++
	}
	s.RepoFocus = sortedFocusItems(counts)
}

// extractCollaboration derives collaboration signals from GitHub activity.
func extractCollaboration(s *SignalSet, activities []models.GitHubActivity) {
	repos := make(map[string]bool)
	for _, a := range activities {
		switch a.EventType {
		case "PullRequestReviewEvent":
			s.Collaboration.PRReviews++
		case "IssueCommentEvent":
			s.Collaboration.IssueComments++
		}
		if a.Repository != "" {
			repos[a.Repository] = true
		}
	}
	s.Collaboration.UniqueRepos = len(repos)

	// Cross-team: issues in projects other than top project
	if len(s.ProjectFocus) > 1 {
		topProject := s.ProjectFocus[0].Name
		for _, item := range s.ProjectFocus[1:] {
			if item.Name != topProject {
				s.Collaboration.CrossTeamIssues += item.Count
			}
		}
	}
}

// analyzeOwnership extracts ownership patterns from Jira issues.
func analyzeOwnership(s *SignalSet, issues []models.Issue) {
	var totalClosed, highPriClosed, incidents int
	for _, issue := range issues {
		status := strings.ToLower(issue.Fields.Status.Name)
		if status == "done" || status == "closed" || status == "resolved" {
			totalClosed++
		}
		theme := ClassifyTheme(issue.IssueType, issue.Fields.Summary)
		if theme == ThemeIncident {
			incidents++
		}
		// High-priority closed detection via IssueType field naming pattern
		// Since priority is only in query filters, we infer from status + theme
		if (status == "done" || status == "closed" || status == "resolved") &&
			(theme == ThemeIncident || theme == ThemeBug) {
			highPriClosed++
		}
	}

	s.Ownership.TotalClosed = totalClosed
	s.Ownership.HighPriorityClosed = highPriClosed
	if len(issues) > 0 {
		s.Ownership.IncidentRatio = float64(incidents) / float64(len(issues))
	}
}

// sortedFocusItems converts a map to a descending-sorted slice of FocusItems.
func sortedFocusItems(counts map[string]int) []FocusItem {
	items := make([]FocusItem, 0, len(counts))
	for name, count := range counts {
		items = append(items, FocusItem{Name: name, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Count > items[j].Count
	})
	return items
}

// LocalSignals is the unified result of all local-source signal extraction.
// It replaces the separate ExtractShellSignals + ExtractAISignals + AnalyzeSessions +
// AnalyzeTopology calls that were split across FetchReportData (EPIC-021 E3).
type LocalSignals struct {
	ShellActivity   *ShellActivitySignals
	AIActivity      *AIActivitySignals
	SessionSignals  *SessionSignals
	TopologySignals *TopologySignals
}

// ExtractLocalSignals derives all local-source signals in one call.
// It replaces the four separate conditional calls in FetchReportData.
// All fields are always populated (never nil); callers can omit rendering
// fields whose underlying data was not fetched.
func ExtractLocalSignals(cmds []models.ShellCommand, events []models.AuditEvent, activity []models.AIActivity, summaries []models.SessionSummary) *LocalSignals {
	ls := &LocalSignals{
		ShellActivity: ExtractShellSignals(cmds),
		AIActivity:    ExtractAISignals(activity, events, summaries),
	}
	if len(summaries) > 0 {
		ls.SessionSignals = AnalyzeSessions(summaries)
		ls.TopologySignals = AnalyzeTopology(summaries)
	}
	return ls
}

// ExtractShellSignals derives ShellActivitySignals from a slice of shell commands.
// Returns a non-nil struct even when cmds is empty.
//
// Deprecated: use ExtractLocalSignals for unified local-source signal extraction (EPIC-021 E3).
func ExtractShellSignals(cmds []models.ShellCommand) *ShellActivitySignals {
	s := &ShellActivitySignals{
		ToolCounts:     make(map[string]int),
		CategoryCounts: make(map[string]int),
	}
	if len(cmds) == 0 {
		return s
	}

	days := make(map[string]bool)
	for _, cmd := range cmds {
		s.TotalCommands++

		if !cmd.Timestamp.IsZero() {
			days[cmd.Timestamp.Format("2006-01-02")] = true
			s.HourDistribution[cmd.Timestamp.Hour()]++
			s.WeekdayActivity[int(cmd.Timestamp.Weekday())]++
		}

		if cmd.Binary != "" {
			s.ToolCounts[cmd.Binary]++
		}
		s.CategoryCounts[cmd.Category]++

		if cmd.IsInfra {
			s.InfraCommands++
		}
		if cmd.IsDeploy {
			s.DeployCommands++
		}
	}

	s.DaysActive = len(days)
	return s
}

// ExtractAISignals derives AIActivitySignals from daily AI activity records,
// audit events, and session summaries.
// Returns a non-nil struct even when all slices are empty.
//
// Deprecated: use ExtractLocalSignals for unified local-source signal extraction (EPIC-021 E3).
func ExtractAISignals(activity []models.AIActivity, events []models.AuditEvent, summaries []models.SessionSummary) *AIActivitySignals {
	s := &AIActivitySignals{
		ToolDistribution: make(map[string]int),
		LayerBreakdown:   make(map[string]int),
	}

	for _, a := range activity {
		if a.MessageCount > 0 || a.SessionCount > 0 {
			s.DaysActive++
		}
		s.TotalSessions += a.SessionCount
		s.TotalMessages += a.MessageCount
		s.TotalToolCalls += a.ToolCallCount
		s.TotalTokens += a.TokensUsed
	}

	agentProjects := make(map[string]int)
	for _, event := range events {
		// Binary human/agent classification (backward-compatible)
		switch event.Source {
		case "interactive_shell":
			s.HumanCommands++
		case "claude_code":
			s.AgentCommands++
			if proj := lastPathComponent(event.Cwd); proj != "" {
				agentProjects[proj]++
			}
		}
		// EPIC-019 M3: layer-aware breakdown — count every source value
		if event.Source != "" {
			s.LayerBreakdown[event.Source]++
		}
	}
	s.AgentProjects = sortedFocusItems(agentProjects)

	// EPIC-019 M1+M2: enrich from session summaries
	var totalDurationMin float64
	for _, ss := range summaries {
		s.EventSessions++
		s.GraduationCandidates += ss.GraduationCandidates
		s.TotalCostUSD += ss.CostEstimateUSD
		for tool, count := range ss.ToolDistribution {
			s.ToolDistribution[tool] += count
		}
		if !ss.FirstEvent.IsZero() && !ss.LastEvent.IsZero() {
			dur := ss.LastEvent.Sub(ss.FirstEvent).Minutes()
			if dur > 0 {
				totalDurationMin += dur
			}
		}
	}
	if s.EventSessions > 0 {
		s.AvgSessionDurationMin = totalDurationMin / float64(s.EventSessions)
	}

	return s
}

// lastPathComponent returns the last non-empty segment of a slash-separated path.
func lastPathComponent(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// parseDate attempts multiple date formats common in Jira responses.
func parseDate(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, &time.ParseError{Value: s, Message: "no matching format"}
}
