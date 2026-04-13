package insights

import (
	"testing"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

func TestClassifyTheme(t *testing.T) {
	tests := []struct {
		name      string
		issueType string
		summary   string
		want      Theme
	}{
		{"bug type", "Bug", "Fix login crash", ThemeBug},
		{"story type", "Story", "Add user dashboard", ThemeFeature},
		{"feature type", "Feature", "New search API", ThemeFeature},
		{"new feature type", "New Feature", "Add search", ThemeFeature},
		{"epic type", "Epic", "Platform overhaul", ThemeFeature},
		{"incident type", "Incident", "Database down", ThemeIncident},
		{"task with incident keyword", "Task", "Investigate outage in prod", ThemeIncident},
		{"task infra keyword terraform", "Task", "Update terraform modules", ThemeInfrastructure},
		{"task infra keyword kubernetes", "Task", "Upgrade kubernetes to 1.29", ThemeInfrastructure},
		{"task infra keyword deploy", "Task", "Deploy new service to EKS", ThemeInfrastructure},
		{"task infra keyword helm", "Task", "Update helm chart values", ThemeInfrastructure},
		{"task infra keyword pipeline", "Task", "Fix CI/CD pipeline", ThemeInfrastructure},
		{"task infra keyword kafka", "Task", "Kafka topic configuration", ThemeInfrastructure},
		{"task infra keyword aws", "Task", "AWS IAM policy update", ThemeInfrastructure},
		{"task maintenance", "Task", "Clean up old logs", ThemeMaintenance},
		{"sub-task maintenance", "Sub-task", "Write unit tests", ThemeMaintenance},
		{"unknown type", "Custom", "Something else", ThemeOther},
		{"empty type", "", "Random task", ThemeOther},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyTheme(tt.issueType, tt.summary)
			if got != tt.want {
				t.Errorf("ClassifyTheme(%q, %q) = %q, want %q", tt.issueType, tt.summary, got, tt.want)
			}
		})
	}
}

func TestExtractSignals_Empty(t *testing.T) {
	s := ExtractSignals(nil, nil, nil)
	if s.TotalIssues != 0 {
		t.Errorf("TotalIssues = %d, want 0", s.TotalIssues)
	}
	if s.TotalArticles != 0 {
		t.Errorf("TotalArticles = %d, want 0", s.TotalArticles)
	}
	if s.TotalActivities != 0 {
		t.Errorf("TotalActivities = %d, want 0", s.TotalActivities)
	}
	if len(s.ThemeCounts) != 0 {
		t.Errorf("ThemeCounts not empty: %v", s.ThemeCounts)
	}
	if len(s.Velocity) != 0 {
		t.Errorf("Velocity not empty: %v", s.Velocity)
	}
	if s.Ownership.IncidentRatio != 0 {
		t.Errorf("IncidentRatio = %f, want 0", s.Ownership.IncidentRatio)
	}
}

func TestExtractSignals_ThemeCounts(t *testing.T) {
	issues := []models.Issue{
		{IssueType: "Bug", Fields: issueFields("Fix crash", "", "", "")},
		{IssueType: "Bug", Fields: issueFields("Fix OOM", "", "", "")},
		{IssueType: "Story", Fields: issueFields("Add feature", "", "", "")},
		{IssueType: "Task", Fields: issueFields("Update terraform", "", "", "")},
		{IssueType: "Task", Fields: issueFields("Write docs", "", "", "")},
	}

	s := ExtractSignals(issues, nil, nil)

	if s.ThemeCounts[ThemeBug] != 2 {
		t.Errorf("Bug count = %d, want 2", s.ThemeCounts[ThemeBug])
	}
	if s.ThemeCounts[ThemeFeature] != 1 {
		t.Errorf("Feature count = %d, want 1", s.ThemeCounts[ThemeFeature])
	}
	if s.ThemeCounts[ThemeInfrastructure] != 1 {
		t.Errorf("Infrastructure count = %d, want 1", s.ThemeCounts[ThemeInfrastructure])
	}
	if s.ThemeCounts[ThemeMaintenance] != 1 {
		t.Errorf("Maintenance count = %d, want 1", s.ThemeCounts[ThemeMaintenance])
	}
}

func TestExtractSignals_Velocity(t *testing.T) {
	issues := []models.Issue{
		{Fields: issueFields("A", "2026-01-15T10:00:00.000Z", "", "2026-01-20T10:00:00.000Z")},
		{Fields: issueFields("B", "2026-01-20T10:00:00.000Z", "", "")},
		{Fields: issueFields("C", "2026-02-05T10:00:00.000Z", "", "2026-02-10T10:00:00.000Z")},
	}

	s := ExtractSignals(issues, nil, nil)

	if len(s.Velocity) != 2 {
		t.Fatalf("Velocity len = %d, want 2", len(s.Velocity))
	}

	jan := s.Velocity[0]
	if jan.Month != "2026-01" {
		t.Errorf("month[0] = %q, want 2026-01", jan.Month)
	}
	if jan.Created != 2 {
		t.Errorf("jan created = %d, want 2", jan.Created)
	}
	if jan.Closed != 1 {
		t.Errorf("jan closed = %d, want 1", jan.Closed)
	}

	feb := s.Velocity[1]
	if feb.Month != "2026-02" {
		t.Errorf("month[1] = %q, want 2026-02", feb.Month)
	}
	if feb.Created != 1 {
		t.Errorf("feb created = %d, want 1", feb.Created)
	}
	if feb.Closed != 1 {
		t.Errorf("feb closed = %d, want 1", feb.Closed)
	}
}

func TestExtractSignals_ProjectFocus(t *testing.T) {
	issues := []models.Issue{
		{ProjectKey: "SR"},
		{ProjectKey: "SR"},
		{ProjectKey: "SR"},
		{ProjectKey: "ISRE"},
		{ProjectKey: "DATA"},
	}

	s := ExtractSignals(issues, nil, nil)

	if len(s.ProjectFocus) != 3 {
		t.Fatalf("ProjectFocus len = %d, want 3", len(s.ProjectFocus))
	}
	if s.ProjectFocus[0].Name != "SR" || s.ProjectFocus[0].Count != 3 {
		t.Errorf("top project = %+v, want SR:3", s.ProjectFocus[0])
	}
}

func TestExtractSignals_SpaceFocus(t *testing.T) {
	articles := []models.ConfluenceArticle{
		{SpaceKey: "ENG"},
		{SpaceKey: "ENG"},
		{SpaceKey: "INFRA"},
	}

	s := ExtractSignals(nil, articles, nil)

	if len(s.SpaceFocus) != 2 {
		t.Fatalf("SpaceFocus len = %d, want 2", len(s.SpaceFocus))
	}
	if s.SpaceFocus[0].Name != "ENG" || s.SpaceFocus[0].Count != 2 {
		t.Errorf("top space = %+v, want ENG:2", s.SpaceFocus[0])
	}
}

func TestExtractSignals_RepoFocus(t *testing.T) {
	activities := []models.GitHubActivity{
		{Repository: "org/backend", EventType: "PushEvent"},
		{Repository: "org/backend", EventType: "PushEvent"},
		{Repository: "org/frontend", EventType: "PushEvent"},
	}

	s := ExtractSignals(nil, nil, activities)

	if len(s.RepoFocus) != 2 {
		t.Fatalf("RepoFocus len = %d, want 2", len(s.RepoFocus))
	}
	if s.RepoFocus[0].Name != "org/backend" || s.RepoFocus[0].Count != 2 {
		t.Errorf("top repo = %+v, want org/backend:2", s.RepoFocus[0])
	}
}

func TestExtractSignals_Collaboration(t *testing.T) {
	activities := []models.GitHubActivity{
		{EventType: "PullRequestReviewEvent", Repository: "org/repo1"},
		{EventType: "PullRequestReviewEvent", Repository: "org/repo2"},
		{EventType: "IssueCommentEvent", Repository: "org/repo1"},
		{EventType: "PushEvent", Repository: "org/repo3"},
	}

	s := ExtractSignals(nil, nil, activities)

	if s.Collaboration.PRReviews != 2 {
		t.Errorf("PRReviews = %d, want 2", s.Collaboration.PRReviews)
	}
	if s.Collaboration.IssueComments != 1 {
		t.Errorf("IssueComments = %d, want 1", s.Collaboration.IssueComments)
	}
	if s.Collaboration.UniqueRepos != 3 {
		t.Errorf("UniqueRepos = %d, want 3", s.Collaboration.UniqueRepos)
	}
}

func TestExtractSignals_Ownership(t *testing.T) {
	issues := []models.Issue{
		{IssueType: "Bug", Fields: issueFields("Fix crash", "", "Done", "")},
		{IssueType: "Bug", Fields: issueFields("Fix OOM", "", "Closed", "")},
		{IssueType: "Story", Fields: issueFields("Add feature", "", "Done", "")},
		{IssueType: "Task", Fields: issueFields("Investigate outage in prod", "", "Done", "")},
		{IssueType: "Story", Fields: issueFields("In progress work", "", "In Progress", "")},
	}

	s := ExtractSignals(issues, nil, nil)

	if s.Ownership.TotalClosed != 4 {
		t.Errorf("TotalClosed = %d, want 4", s.Ownership.TotalClosed)
	}
	// Bug+Incident closed: 2 bugs + 1 incident = 3
	if s.Ownership.HighPriorityClosed != 3 {
		t.Errorf("HighPriorityClosed = %d, want 3", s.Ownership.HighPriorityClosed)
	}
	// 1 incident out of 5
	expectedRatio := 1.0 / 5.0
	if s.Ownership.IncidentRatio != expectedRatio {
		t.Errorf("IncidentRatio = %f, want %f", s.Ownership.IncidentRatio, expectedRatio)
	}
}

func TestExtractSignals_CrossTeamIssues(t *testing.T) {
	issues := []models.Issue{
		{ProjectKey: "SR"},
		{ProjectKey: "SR"},
		{ProjectKey: "ISRE"},
		{ProjectKey: "DATA"},
	}

	s := ExtractSignals(issues, nil, nil)

	// SR is top (2), ISRE (1) + DATA (1) = 2 cross-team
	if s.Collaboration.CrossTeamIssues != 2 {
		t.Errorf("CrossTeamIssues = %d, want 2", s.Collaboration.CrossTeamIssues)
	}
}

func TestParseDate(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"RFC3339", "2026-01-15T10:00:00Z", false},
		{"Jira format", "2026-01-15T10:00:00.000-0600", false},
		{"Jira UTC", "2026-01-15T10:00:00.000Z", false},
		{"datetime", "2026-01-15 10:00:00", false},
		{"date only", "2026-01-15", false},
		{"invalid", "not-a-date", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseDate(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseDate(%q) err = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// issueFields is a helper to build Issue.Fields with common patterns.
// ---------------------------------------------------------------------------
// Coverage gap tests (EPIC-009 Phase 6)
// ---------------------------------------------------------------------------

// TestClassifyTheme_InfraRelatedNonTask covers the `isInfraRelated(lower)`
// branch that fires when issueType itself is infra-related but is not
// task/sub-task, bug, story, etc. (e.g., a custom "Deployment" type).
func TestClassifyTheme_InfraRelatedNonTask(t *testing.T) {
	got := ClassifyTheme("deployment", "Roll out new service")
	if got != ThemeInfrastructure {
		t.Errorf("ClassifyTheme(deployment) = %q, want ThemeInfrastructure", got)
	}
}

// TestExtractSignals_SpaceFocusEmptyKey covers the empty-SpaceKey → "Unknown"
// fallback inside computeSpaceFocus.
func TestExtractSignals_SpaceFocusEmptyKey(t *testing.T) {
	articles := []models.ConfluenceArticle{
		{SpaceKey: ""}, // empty → "Unknown"
		{SpaceKey: "ENG"},
	}

	s := ExtractSignals(nil, articles, nil)

	found := false
	for _, f := range s.SpaceFocus {
		if f.Name == "Unknown" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("SpaceFocus = %v, expected an 'Unknown' entry for empty SpaceKey", s.SpaceFocus)
	}
}

// TestExtractSignals_RepoFocusEmptyRepo covers the empty-Repository → "Unknown"
// fallback inside computeRepoFocus.
func TestExtractSignals_RepoFocusEmptyRepo(t *testing.T) {
	activities := []models.GitHubActivity{
		{Repository: "", EventType: "PushEvent"}, // empty → "Unknown"
		{Repository: "org/repo", EventType: "PushEvent"},
	}

	s := ExtractSignals(nil, nil, activities)

	found := false
	for _, f := range s.RepoFocus {
		if f.Name == "Unknown" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RepoFocus = %v, expected an 'Unknown' entry for empty Repository", s.RepoFocus)
	}
}

func issueFields(summary, created, status, resolved string) struct {
	Summary  string
	Created  string
	Updated  string
	Resolved string
	Status   struct{ Name string }
} {
	return struct {
		Summary  string
		Created  string
		Updated  string
		Resolved string
		Status   struct{ Name string }
	}{
		Summary:  summary,
		Created:  created,
		Resolved: resolved,
		Status:   struct{ Name string }{Name: status},
	}
}

// ── EPIC-015 M2: ExtractShellSignals ────────────────────────────────────────

func TestExtractShellSignals_Empty(t *testing.T) {
	s := ExtractShellSignals(nil)
	if s == nil {
		t.Fatal("ExtractShellSignals(nil) returned nil")
	}
	if s.TotalCommands != 0 {
		t.Errorf("TotalCommands = %d, want 0", s.TotalCommands)
	}
	if s.DaysActive != 0 {
		t.Errorf("DaysActive = %d, want 0", s.DaysActive)
	}
	if s.ToolCounts == nil {
		t.Error("ToolCounts should be non-nil map")
	}
	if s.CategoryCounts == nil {
		t.Error("CategoryCounts should be non-nil map")
	}
}

func TestExtractShellSignals_Counts(t *testing.T) {
	ts := time.Date(2026, 2, 10, 14, 30, 0, 0, time.UTC) // Tuesday 14:30

	cmds := []models.ShellCommand{
		{Binary: "kubectl", Category: "kubernetes", IsInfra: true, IsDeploy: false, Timestamp: ts},
		{Binary: "kubectl", Category: "kubernetes", IsInfra: true, IsDeploy: false, Timestamp: ts},
		{Binary: "terraform", Category: "terraform", IsInfra: true, IsDeploy: true, Timestamp: ts},
		{Binary: "git", Category: "git", IsInfra: false, IsDeploy: false, Timestamp: ts.Add(24 * time.Hour)}, // Wednesday
		{Binary: "git", Category: "git", IsInfra: false, IsDeploy: true, Timestamp: ts.Add(24 * time.Hour)},
	}

	s := ExtractShellSignals(cmds)

	if s.TotalCommands != 5 {
		t.Errorf("TotalCommands = %d, want 5", s.TotalCommands)
	}
	if s.DaysActive != 2 {
		t.Errorf("DaysActive = %d, want 2 (two distinct dates)", s.DaysActive)
	}
	if s.InfraCommands != 3 {
		t.Errorf("InfraCommands = %d, want 3", s.InfraCommands)
	}
	if s.DeployCommands != 2 {
		t.Errorf("DeployCommands = %d, want 2", s.DeployCommands)
	}
	if s.ToolCounts["kubectl"] != 2 {
		t.Errorf("ToolCounts[kubectl] = %d, want 2", s.ToolCounts["kubectl"])
	}
	if s.ToolCounts["terraform"] != 1 {
		t.Errorf("ToolCounts[terraform] = %d, want 1", s.ToolCounts["terraform"])
	}
	if s.CategoryCounts["kubernetes"] != 2 {
		t.Errorf("CategoryCounts[kubernetes] = %d, want 2", s.CategoryCounts["kubernetes"])
	}
	if s.CategoryCounts["git"] != 2 {
		t.Errorf("CategoryCounts[git] = %d, want 2", s.CategoryCounts["git"])
	}
}

func TestExtractShellSignals_HourDistribution(t *testing.T) {
	cmds := []models.ShellCommand{
		{Binary: "ls", Category: "general", Timestamp: time.Date(2026, 2, 10, 9, 0, 0, 0, time.UTC)},
		{Binary: "ls", Category: "general", Timestamp: time.Date(2026, 2, 10, 9, 30, 0, 0, time.UTC)},
		{Binary: "ls", Category: "general", Timestamp: time.Date(2026, 2, 10, 14, 0, 0, 0, time.UTC)},
	}

	s := ExtractShellSignals(cmds)

	if s.HourDistribution[9] != 2 {
		t.Errorf("HourDistribution[9] = %d, want 2", s.HourDistribution[9])
	}
	if s.HourDistribution[14] != 1 {
		t.Errorf("HourDistribution[14] = %d, want 1", s.HourDistribution[14])
	}
	// All other hours should be zero
	total := 0
	for _, v := range s.HourDistribution {
		total += v
	}
	if total != 3 {
		t.Errorf("HourDistribution total = %d, want 3", total)
	}
}

func TestExtractShellSignals_WeekdayActivity(t *testing.T) {
	// time.Date(2026, 2, 9, ...) is a Monday (weekday 1)
	monday := time.Date(2026, 2, 9, 10, 0, 0, 0, time.UTC)
	// time.Date(2026, 2, 10, ...) is a Tuesday (weekday 2)
	tuesday := time.Date(2026, 2, 10, 10, 0, 0, 0, time.UTC)

	cmds := []models.ShellCommand{
		{Binary: "git", Category: "git", Timestamp: monday},
		{Binary: "git", Category: "git", Timestamp: monday},
		{Binary: "git", Category: "git", Timestamp: tuesday},
	}

	s := ExtractShellSignals(cmds)

	if s.WeekdayActivity[int(time.Monday)] != 2 {
		t.Errorf("WeekdayActivity[Monday] = %d, want 2", s.WeekdayActivity[int(time.Monday)])
	}
	if s.WeekdayActivity[int(time.Tuesday)] != 1 {
		t.Errorf("WeekdayActivity[Tuesday] = %d, want 1", s.WeekdayActivity[int(time.Tuesday)])
	}
}

func TestExtractShellSignals_ZeroTimestamp(t *testing.T) {
	// Commands with zero timestamp should not affect hour/weekday distribution or DaysActive.
	cmds := []models.ShellCommand{
		{Binary: "ls", Category: "general", Timestamp: time.Time{}}, // zero
		{Binary: "ls", Category: "general", Timestamp: time.Date(2026, 2, 10, 9, 0, 0, 0, time.UTC)},
	}

	s := ExtractShellSignals(cmds)

	if s.TotalCommands != 2 {
		t.Errorf("TotalCommands = %d, want 2", s.TotalCommands)
	}
	if s.DaysActive != 1 {
		t.Errorf("DaysActive = %d, want 1 (zero timestamp should not count)", s.DaysActive)
	}
}

// ── EPIC-015 M2: ExtractAISignals ────────────────────────────────────────────

func TestExtractAISignals_Empty(t *testing.T) {
	s := ExtractAISignals(nil, nil, nil)
	if s == nil {
		t.Fatal("ExtractAISignals(nil, nil, nil) returned nil")
	}
	if s.TotalSessions != 0 || s.TotalMessages != 0 || s.TotalTokens != 0 {
		t.Errorf("all totals should be 0 for empty input, got sessions=%d messages=%d tokens=%d",
			s.TotalSessions, s.TotalMessages, s.TotalTokens)
	}
}

func TestExtractAISignals_Aggregates(t *testing.T) {
	activity := []models.AIActivity{
		{Date: "2026-02-09", MessageCount: 100, SessionCount: 5, ToolCallCount: 50, TokensUsed: 10000},
		{Date: "2026-02-10", MessageCount: 200, SessionCount: 8, ToolCallCount: 80, TokensUsed: 20000},
		{Date: "2026-02-11", MessageCount: 0, SessionCount: 0, ToolCallCount: 0, TokensUsed: 0}, // inactive day
	}

	s := ExtractAISignals(activity, nil, nil)

	if s.TotalMessages != 300 {
		t.Errorf("TotalMessages = %d, want 300", s.TotalMessages)
	}
	if s.TotalSessions != 13 {
		t.Errorf("TotalSessions = %d, want 13", s.TotalSessions)
	}
	if s.TotalToolCalls != 130 {
		t.Errorf("TotalToolCalls = %d, want 130", s.TotalToolCalls)
	}
	if s.TotalTokens != 30000 {
		t.Errorf("TotalTokens = %d, want 30000", s.TotalTokens)
	}
	// DaysActive: only days with messages or sessions > 0
	if s.DaysActive != 2 {
		t.Errorf("DaysActive = %d, want 2 (day with 0 activity not counted)", s.DaysActive)
	}
}

func TestExtractAISignals_HumanVsAgent(t *testing.T) {
	events := []models.AuditEvent{
		{Source: "interactive_shell", Cwd: "/Users/brian/code/myproject"},
		{Source: "interactive_shell", Cwd: "/Users/brian/code/myproject"},
		{Source: "claude_code", Cwd: "/Users/brian/code/workctl-tool"},
		{Source: "claude_code", Cwd: "/Users/brian/code/workctl-tool"},
		{Source: "claude_code", Cwd: "/Users/brian/code/other-project"},
	}

	s := ExtractAISignals(nil, events, nil)

	if s.HumanCommands != 2 {
		t.Errorf("HumanCommands = %d, want 2", s.HumanCommands)
	}
	if s.AgentCommands != 3 {
		t.Errorf("AgentCommands = %d, want 3", s.AgentCommands)
	}
	// AgentProjects: workctl-tool=2, other-project=1 (sorted descending)
	if len(s.AgentProjects) != 2 {
		t.Fatalf("len(AgentProjects) = %d, want 2", len(s.AgentProjects))
	}
	if s.AgentProjects[0].Name != "workctl-tool" || s.AgentProjects[0].Count != 2 {
		t.Errorf("AgentProjects[0] = {%q, %d}, want {workctl-tool, 2}",
			s.AgentProjects[0].Name, s.AgentProjects[0].Count)
	}
	if s.AgentProjects[1].Name != "other-project" || s.AgentProjects[1].Count != 1 {
		t.Errorf("AgentProjects[1] = {%q, %d}, want {other-project, 1}",
			s.AgentProjects[1].Name, s.AgentProjects[1].Count)
	}
}

func TestExtractAISignals_EmptyCwd(t *testing.T) {
	// Events with empty Cwd should not appear in AgentProjects
	events := []models.AuditEvent{
		{Source: "claude_code", Cwd: ""},
		{Source: "claude_code", Cwd: "/Users/brian/code/myproject"},
	}

	s := ExtractAISignals(nil, events, nil)

	if len(s.AgentProjects) != 1 {
		t.Errorf("len(AgentProjects) = %d, want 1 (empty cwd excluded)", len(s.AgentProjects))
	}
	if s.AgentProjects[0].Name != "myproject" {
		t.Errorf("AgentProjects[0].Name = %q, want myproject", s.AgentProjects[0].Name)
	}
}

// ── EPIC-019 M1+M2: Session Summary signals ────────────────────────────────

func TestExtractAISignals_SessionSummaries(t *testing.T) {
	summaries := []models.SessionSummary{
		{
			SessionID:            "sess-1",
			TotalEvents:          54,
			ToolEvents:           53,
			UniqueCommands:       27,
			ToolDistribution:     map[string]int{"Bash": 53},
			GraduationCandidates: 10,
			FirstEvent:           time.Date(2026, 3, 2, 14, 17, 10, 0, time.UTC),
			LastEvent:            time.Date(2026, 3, 2, 14, 22, 8, 0, time.UTC), // ~4.97 min
			CostEstimateUSD:      0.15,
		},
		{
			SessionID:            "sess-2",
			TotalEvents:          13,
			ToolEvents:           12,
			UniqueCommands:       6,
			ToolDistribution:     map[string]int{"Bash": 10, "Read": 2},
			GraduationCandidates: 5,
			FirstEvent:           time.Date(2026, 3, 2, 14, 28, 30, 0, time.UTC),
			LastEvent:            time.Date(2026, 3, 2, 14, 36, 1, 0, time.UTC), // ~7.52 min
			CostEstimateUSD:      0.28,
		},
	}

	s := ExtractAISignals(nil, nil, summaries)

	if s.EventSessions != 2 {
		t.Errorf("EventSessions = %d, want 2", s.EventSessions)
	}
	if s.GraduationCandidates != 15 {
		t.Errorf("GraduationCandidates = %d, want 15", s.GraduationCandidates)
	}
	// Total cost
	wantCost := 0.43
	if s.TotalCostUSD < wantCost-0.01 || s.TotalCostUSD > wantCost+0.01 {
		t.Errorf("TotalCostUSD = %.4f, want ~%.2f", s.TotalCostUSD, wantCost)
	}
	// Tool distribution: Bash = 63, Read = 2
	if s.ToolDistribution["Bash"] != 63 {
		t.Errorf("ToolDistribution[Bash] = %d, want 63", s.ToolDistribution["Bash"])
	}
	if s.ToolDistribution["Read"] != 2 {
		t.Errorf("ToolDistribution[Read] = %d, want 2", s.ToolDistribution["Read"])
	}
	// Average duration: (4.97 + 7.52) / 2 ≈ 6.24 min
	if s.AvgSessionDurationMin < 5.0 || s.AvgSessionDurationMin > 8.0 {
		t.Errorf("AvgSessionDurationMin = %.2f, want ~6.2", s.AvgSessionDurationMin)
	}
}

func TestExtractAISignals_LayerBreakdown(t *testing.T) {
	events := []models.AuditEvent{
		{Source: "interactive_shell"},
		{Source: "interactive_shell"},
		{Source: "claude_code"},
		{Source: "claude_code"},
		{Source: "claude_code"},
		{Source: "cloud_llm"},
	}

	s := ExtractAISignals(nil, events, nil)

	if s.LayerBreakdown["interactive_shell"] != 2 {
		t.Errorf("LayerBreakdown[interactive_shell] = %d, want 2", s.LayerBreakdown["interactive_shell"])
	}
	if s.LayerBreakdown["claude_code"] != 3 {
		t.Errorf("LayerBreakdown[claude_code] = %d, want 3", s.LayerBreakdown["claude_code"])
	}
	if s.LayerBreakdown["cloud_llm"] != 1 {
		t.Errorf("LayerBreakdown[cloud_llm] = %d, want 1", s.LayerBreakdown["cloud_llm"])
	}
	// Should still maintain backward-compatible counts
	if s.HumanCommands != 2 {
		t.Errorf("HumanCommands = %d, want 2", s.HumanCommands)
	}
	if s.AgentCommands != 3 {
		t.Errorf("AgentCommands = %d, want 3", s.AgentCommands)
	}
}

func TestExtractAISignals_SessionSummariesEmpty(t *testing.T) {
	s := ExtractAISignals(nil, nil, nil)
	if s.EventSessions != 0 {
		t.Errorf("EventSessions = %d, want 0", s.EventSessions)
	}
	if s.ToolDistribution == nil {
		t.Error("ToolDistribution should be non-nil map")
	}
	if s.AvgSessionDurationMin != 0 {
		t.Errorf("AvgSessionDurationMin = %f, want 0", s.AvgSessionDurationMin)
	}
}

func TestExtractLocalSignals_AllNil(t *testing.T) {
	ls := ExtractLocalSignals(nil, nil, nil, nil)
	if ls == nil {
		t.Fatal("ExtractLocalSignals should never return nil")
	}
	if ls.ShellActivity == nil {
		t.Error("ShellActivity should be non-nil even for empty input")
	}
	if ls.AIActivity == nil {
		t.Error("AIActivity should be non-nil even for empty input")
	}
	// No session summaries → SessionSignals and TopologySignals should be nil.
	if ls.SessionSignals != nil {
		t.Error("SessionSignals should be nil when summaries is empty")
	}
	if ls.TopologySignals != nil {
		t.Error("TopologySignals should be nil when summaries is empty")
	}
}

func TestExtractLocalSignals_WithSummaries(t *testing.T) {
	summaries := []models.SessionSummary{
		{SessionID: "s1", TotalEvents: 5, ToolDistribution: map[string]int{"Bash": 3}},
	}
	ls := ExtractLocalSignals(nil, nil, nil, summaries)
	if ls.SessionSignals == nil {
		t.Error("SessionSignals should be populated when summaries is non-empty")
	}
	if ls.TopologySignals == nil {
		t.Error("TopologySignals should be populated when summaries is non-empty")
	}
}

func TestLastPathComponent(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/Users/brian/code/workctl-tool", "workctl-tool"},
		{"/Users/brian/code/workctl-tool/", "workctl-tool"},
		{"/single", "single"},
		{"", ""},
		{"/", ""},
		{"noSlash", "noSlash"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := lastPathComponent(tt.input); got != tt.want {
				t.Errorf("lastPathComponent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
