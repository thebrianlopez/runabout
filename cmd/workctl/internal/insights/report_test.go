package insights

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderInsights_Empty(t *testing.T) {
	var buf bytes.Buffer
	s := &SignalSet{ThemeCounts: make(map[Theme]int)}
	RenderInsights(&buf, s, "2026-01-01 to 2026-02-01")

	out := buf.String()
	if !strings.Contains(out, "# Career Growth Insights") {
		t.Error("missing title")
	}
	if !strings.Contains(out, "2026-01-01 to 2026-02-01") {
		t.Error("missing period")
	}
	if !strings.Contains(out, "| Jira Issues | 0 |") {
		t.Error("missing overview table")
	}
}

func TestRenderInsights_Full(t *testing.T) {
	s := &SignalSet{
		TotalIssues:     5,
		TotalArticles:   3,
		TotalActivities: 10,
		ThemeCounts: map[Theme]int{
			ThemeBug:     2,
			ThemeFeature: 3,
		},
		Velocity: []MonthBucket{
			{Month: "2026-01", Created: 3, Closed: 2},
			{Month: "2026-02", Created: 2, Closed: 3},
		},
		ProjectFocus: []FocusItem{
			{Name: "SR", Count: 3},
			{Name: "ISRE", Count: 2},
		},
		SpaceFocus: []FocusItem{
			{Name: "ENG", Count: 3},
		},
		RepoFocus: []FocusItem{
			{Name: "org/backend", Count: 7},
			{Name: "org/frontend", Count: 3},
		},
		Collaboration: CollaborationSignals{
			PRReviews:       5,
			IssueComments:   3,
			UniqueRepos:     2,
			CrossTeamIssues: 2,
		},
		Ownership: OwnershipSignals{
			TotalClosed:        4,
			HighPriorityClosed: 2,
			IncidentRatio:      0.1,
		},
	}

	var buf bytes.Buffer
	RenderInsights(&buf, s, "2026-01-01 to 2026-02-28")
	out := buf.String()

	checks := []string{
		"## Work Themes",
		"| Feature | 3 | 60% |",
		"| Bug | 2 | 40% |",
		"## Monthly Velocity",
		"| 2026-01 | 3 | 2 | -1 |",
		"| 2026-02 | 2 | 3 | +1 |",
		"## Focus Distribution",
		"### Projects",
		"| SR | 3 | 60% |",
		"### Repositories",
		"| org/backend | 7 | 70% |",
		"## Collaboration Signals",
		"| PR Reviews | 5 |",
		"| Cross-Team Issues | 2 |",
		"## Ownership Signals",
		"| Issues Closed | 4 |",
		"| Incident Ratio | 10.0% |",
	}

	for _, check := range checks {
		if !strings.Contains(out, check) {
			t.Errorf("missing in output: %q", check)
		}
	}
}

func TestFormatPeriod(t *testing.T) {
	tests := []struct {
		start, end, want string
	}{
		{"2026-01-01", "2026-02-28", "2026-01-01 to 2026-02-28"},
		{"2026-01-01", "", "2026-01-01"},
		{"", "2026-02-28", "2026-02-28"},
		{"", "", ""},
	}
	for _, tt := range tests {
		got := FormatPeriod(tt.start, tt.end)
		if got != tt.want {
			t.Errorf("FormatPeriod(%q, %q) = %q, want %q", tt.start, tt.end, got, tt.want)
		}
	}
}
