package insights

import (
	"strings"
	"testing"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// --- ActivityLevel ---

func TestActivityLevel(t *testing.T) {
	tests := []struct {
		total int
		want  string
	}{
		{0, "Low"},
		{49, "Low"},
		{50, "Medium"},
		{150, "Medium"},
		{151, "High"},
		{500, "High"},
	}
	for _, tc := range tests {
		if got := ActivityLevel(tc.total); got != tc.want {
			t.Errorf("ActivityLevel(%d) = %q, want %q", tc.total, got, tc.want)
		}
	}
}

// --- weekNumFromPeriod ---

func TestWeekNumFromPeriod(t *testing.T) {
	tests := []struct {
		period string
		want   int
	}{
		{"2026-01-05 to 2026-01-11", 2}, // ISO week 2
		{"2026-02-02 to 2026-02-08", 6}, // ISO week 6
		{"invalid", 0},
		{"", 0},
	}
	for _, tc := range tests {
		if got := weekNumFromPeriod(tc.period); got != tc.want {
			t.Errorf("weekNumFromPeriod(%q) = %d, want %d", tc.period, got, tc.want)
		}
	}
}

// --- FormatStandupTitle ---

func TestFormatStandupTitle(t *testing.T) {
	tests := []struct {
		start time.Time
		end   time.Time
		want  string
	}{
		// Same month
		{
			time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 2, 24, 0, 0, 0, 0, time.UTC),
			"Week 08 | February 17 - 24, 2026",
		},
		// Cross-month
		{
			time.Date(2026, 1, 29, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 2, 5, 0, 0, 0, 0, time.UTC),
			"Week 05 | January 29 - February 5, 2026",
		},
		// Zero-padded week (week 7)
		{
			time.Date(2026, 2, 9, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC),
			"Week 07 | February 9 - 15, 2026",
		},
		// Two-digit week
		{
			time.Date(2025, 11, 17, 0, 0, 0, 0, time.UTC),
			time.Date(2025, 11, 24, 0, 0, 0, 0, time.UTC),
			"Week 47 | November 17 - 24, 2025",
		},
	}
	for _, tc := range tests {
		got := FormatStandupTitle(tc.start, tc.end)
		if got != tc.want {
			t.Errorf("FormatStandupTitle(%s, %s) = %q, want %q",
				tc.start.Format("2006-01-02"), tc.end.Format("2006-01-02"), got, tc.want)
		}
	}
}

// --- partitionIssues ---

func TestPartitionIssues(t *testing.T) {
	issues := []models.Issue{
		{Key: "SR-1", Fields: struct {
			Summary  string
			Created  string
			Updated  string
			Resolved string
			Status   struct{ Name string }
		}{Summary: "Done ticket", Status: struct{ Name string }{"Done"}}},
		{Key: "SR-2", Fields: struct {
			Summary  string
			Created  string
			Updated  string
			Resolved string
			Status   struct{ Name string }
		}{Summary: "Closed ticket", Status: struct{ Name string }{"Closed"}}},
		{Key: "SR-3", Fields: struct {
			Summary  string
			Created  string
			Updated  string
			Resolved string
			Status   struct{ Name string }
		}{Summary: "In Progress ticket", Status: struct{ Name string }{"In Progress"}}},
		{Key: "SR-4", Fields: struct {
			Summary  string
			Created  string
			Updated  string
			Resolved string
			Status   struct{ Name string }
		}{Summary: "In Review ticket", Status: struct{ Name string }{"In Review"}}},
		{Key: "SR-5", Fields: struct {
			Summary  string
			Created  string
			Updated  string
			Resolved string
			Status   struct{ Name string }
		}{Summary: "Open ticket", Status: struct{ Name string }{"Open"}}},
	}
	done, wip := partitionIssues(issues)
	if len(done) != 2 {
		t.Errorf("done count = %d, want 2", len(done))
	}
	if len(wip) != 2 {
		t.Errorf("wip count = %d, want 2", len(wip))
	}
}

func TestPartitionIssues_Empty(t *testing.T) {
	done, wip := partitionIssues(nil)
	if len(done) != 0 || len(wip) != 0 {
		t.Error("expected empty slices for nil input")
	}
}

// --- countEvents / filterEvents ---

func TestCountEvents(t *testing.T) {
	acts := []models.GitHubActivity{
		{EventType: "PullRequestEvent"},
		{EventType: "PullRequestEvent"},
		{EventType: "PushEvent"},
	}
	if got := countEvents(acts, "PullRequestEvent"); got != 2 {
		t.Errorf("countEvents = %d, want 2", got)
	}
	if got := countEvents(acts, "PushEvent"); got != 1 {
		t.Errorf("countEvents PushEvent = %d, want 1", got)
	}
	if got := countEvents(nil, "PullRequestEvent"); got != 0 {
		t.Errorf("countEvents nil = %d, want 0", got)
	}
}

func TestFilterEvents(t *testing.T) {
	acts := []models.GitHubActivity{
		{EventType: "PullRequestEvent", Description: "PR 1"},
		{EventType: "PushEvent"},
		{EventType: "PullRequestEvent", Description: "PR 2"},
	}
	prs := filterEvents(acts, "PullRequestEvent")
	if len(prs) != 2 {
		t.Errorf("filterEvents count = %d, want 2", len(prs))
	}
}

// --- buildPrimaryFocus ---

func TestBuildPrimaryFocus(t *testing.T) {
	tests := []struct {
		theme, repo, want string
	}{
		{"Infrastructure", "infra-terraform", "Infrastructure / infra-terraform"},
		{"", "infra-terraform", "infra-terraform"},
		{"Bug", "", "Bug"},
		{"", "", ""},
	}
	for _, tc := range tests {
		if got := buildPrimaryFocus(tc.theme, tc.repo); got != tc.want {
			t.Errorf("buildPrimaryFocus(%q,%q) = %q, want %q", tc.theme, tc.repo, got, tc.want)
		}
	}
}

// --- RenderStandupHTML (integration-style output checks) ---

func makeTestOpts() StandupOpts {
	return StandupOpts{
		AuthorName: "Brian Lopez",
		Period:     "2026-01-29 to 2026-02-05",
		Start:      time.Date(2026, 1, 29, 0, 0, 0, 0, time.UTC),
		End:        time.Date(2026, 2, 5, 0, 0, 0, 0, time.UTC),
		Generated:  time.Date(2026, 2, 5, 10, 0, 0, 0, time.UTC),
		Version:    "v4.7.0",
	}
}

func renderToString(t *testing.T, opts StandupOpts) string {
	t.Helper()
	var sb strings.Builder
	if err := RenderStandupHTML(&sb, opts); err != nil {
		t.Fatalf("RenderStandupHTML error: %v", err)
	}
	return sb.String()
}

func TestRenderStandupHTML_EmptyData(t *testing.T) {
	out := renderToString(t, makeTestOpts())

	// Must contain all required section headers
	sections := []string{
		"Weekly Summary", "Completed Work", "Code Impact",
		"Key Activities", "Learnings", "Work In Progress", "Next Week Plans",
	}
	for _, s := range sections {
		if !strings.Contains(out, s) {
			t.Errorf("output missing section %q", s)
		}
	}

	// Author name appears
	if !strings.Contains(out, "Brian Lopez") {
		t.Error("output missing author name")
	}

	// Title: 2026-01-29 to 2026-02-05 is cross-month, ISO week 05
	if !strings.Contains(out, "Week 05 | January 29 - February 5, 2026") {
		t.Error("output missing formatted title")
	}

	// Footer
	if !strings.Contains(out, "v4.7.0") {
		t.Error("output missing version in footer")
	}

	// Placeholder text for empty sections
	if !strings.Contains(out, "No completed tickets") {
		t.Error("output missing empty-tickets placeholder")
	}
	if !strings.Contains(out, "standup-notes") {
		t.Error("output missing standup-notes placeholder")
	}
}

func TestRenderStandupHTML_WithIssues(t *testing.T) {
	opts := makeTestOpts()
	opts.Issues = []models.Issue{
		{
			Key: "SR-1001",
			URL: "https://example.atlassian.net/browse/SR-1001",
			Fields: struct {
				Summary  string
				Created  string
				Updated  string
				Resolved string
				Status   struct{ Name string }
			}{Summary: "Deploy new service", Status: struct{ Name string }{"Done"}},
		},
		{
			Key: "SR-1002",
			URL: "https://example.atlassian.net/browse/SR-1002",
			Fields: struct {
				Summary  string
				Created  string
				Updated  string
				Resolved string
				Status   struct{ Name string }
			}{Summary: "Investigate issue", Status: struct{ Name string }{"In Progress"}},
		},
	}
	out := renderToString(t, opts)

	if !strings.Contains(out, "SR-1001") {
		t.Error("output missing completed ticket SR-1001")
	}
	if !strings.Contains(out, "Deploy new service") {
		t.Error("output missing ticket summary")
	}
	if !strings.Contains(out, "SR-1002") {
		t.Error("output missing WIP ticket SR-1002")
	}
	if !strings.Contains(out, "Investigate issue") {
		t.Error("output missing WIP summary")
	}
	// Completed ticket should be in ✅ section; WIP in 🔧 section
	completedIdx := strings.Index(out, "SR-1001")
	wipIdx := strings.Index(out, "SR-1002")
	wipSectionIdx := strings.Index(out, "Work In Progress")
	if completedIdx > wipSectionIdx {
		t.Error("SR-1001 should appear before Work In Progress section")
	}
	if wipIdx < wipSectionIdx {
		t.Error("SR-1002 should appear in Work In Progress section")
	}
}

func TestRenderStandupHTML_WithPRs(t *testing.T) {
	opts := makeTestOpts()
	opts.Activities = []models.GitHubActivity{
		{
			EventType:   "PullRequestEvent",
			Description: "PR #42: Add VPC peering",
			URL:         "https://github.com/org/repo/pull/42",
		},
		{
			EventType:    "PullRequestEvent",
			Description:  "PR #43: Fix bug",
			URL:          "https://github.com/org/repo/pull/43",
			Enriched:     true,
			LinesAdded:   100,
			LinesRemoved: 20,
			FilesChanged: []string{"a.go", "b.go"},
		},
		{EventType: "PushEvent", Description: "push"},
	}
	out := renderToString(t, opts)

	if !strings.Contains(out, "PR #42") {
		t.Error("missing PR #42")
	}
	if !strings.Contains(out, "PR #43") {
		t.Error("missing PR #43")
	}
	// Enriched PR shows line stats
	if !strings.Contains(out, "+100/-20") {
		t.Error("missing enriched line stats for PR #43")
	}
	// PushEvent should not appear in PR section
	if strings.Contains(out, "push</li>") {
		t.Error("PushEvent should not appear in PRs list")
	}
}

func TestRenderStandupHTML_WithSignals(t *testing.T) {
	opts := makeTestOpts()
	opts.Signals = &SignalSet{
		ThemeCounts:     map[Theme]int{ThemeInfrastructure: 5, ThemeBug: 2},
		TotalActivities: 80,
		TotalIssues:     10,
		Ownership:       OwnershipSignals{TotalClosed: 7},
		RepoFocus:       []FocusItem{{Name: "infra-terraform", Count: 30}},
	}
	out := renderToString(t, opts)

	if !strings.Contains(out, "Medium") {
		t.Error("expected Medium activity level for 80 events")
	}
	if !strings.Contains(out, "7 completed, 10 total") {
		t.Error("expected ticket counts")
	}
	if !strings.Contains(out, "infra-terraform") {
		t.Error("expected repo focus in Code Impact")
	}
	if !strings.Contains(out, "Infrastructure") {
		t.Error("expected Infrastructure theme")
	}
}

func TestRenderStandupHTML_WithNotes(t *testing.T) {
	opts := makeTestOpts()
	opts.Notes = &StandupNotes{
		Learnings:    []string{"Learned about VPC peering", "Improved TF state management"},
		NextWeekPlan: []string{"Complete SR-2801", "Start EPIC-014"},
	}
	out := renderToString(t, opts)

	if !strings.Contains(out, "Learned about VPC peering") {
		t.Error("missing learning 1")
	}
	if !strings.Contains(out, "Improved TF state management") {
		t.Error("missing learning 2")
	}
	if !strings.Contains(out, "Complete SR-2801") {
		t.Error("missing next-week plan 1")
	}
	if !strings.Contains(out, "Start EPIC-014") {
		t.Error("missing next-week plan 2")
	}
	// Placeholder should NOT appear when notes are provided
	if strings.Contains(out, "standup-notes notes.yaml") {
		t.Error("placeholder text should not appear when notes are provided")
	}
}

func TestRenderStandupHTML_HTMLEscaping(t *testing.T) {
	opts := makeTestOpts()
	opts.Issues = []models.Issue{
		{
			Key: "XSS-1",
			URL: "https://example.com",
			Fields: struct {
				Summary  string
				Created  string
				Updated  string
				Resolved string
				Status   struct{ Name string }
			}{Summary: `Fix <script>alert('xss')</script> bug`, Status: struct{ Name string }{"Done"}},
		},
	}
	out := renderToString(t, opts)
	if strings.Contains(out, "<script>") {
		t.Error("unescaped <script> tag found in output — XSS risk")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Error("expected HTML-escaped script tag")
	}
}

func TestRenderStandupHTML_NilSignals(t *testing.T) {
	opts := makeTestOpts()
	opts.Signals = nil
	// Must not panic
	out := renderToString(t, opts)
	if !strings.Contains(out, "Low") {
		t.Error("expected Low activity level for nil signals")
	}
}

func TestRenderStandupHTML_GeneratedTimestamp(t *testing.T) {
	opts := makeTestOpts()
	opts.Generated = time.Date(2026, 2, 5, 14, 30, 0, 0, time.UTC)
	out := renderToString(t, opts)
	if !strings.Contains(out, "2026-02-05 14:30 UTC") {
		t.Error("expected generated timestamp in footer")
	}
}

func TestRenderStandupHTML_ZeroGeneratedUsesNow(t *testing.T) {
	opts := makeTestOpts()
	opts.Generated = time.Time{} // zero value
	out := renderToString(t, opts)
	// Should still contain a valid year (don't check exact time since it varies)
	if !strings.Contains(out, "2026") {
		t.Error("expected current year in footer for zero Generated")
	}
}
