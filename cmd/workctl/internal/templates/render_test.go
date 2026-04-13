package templates_test

import (
	"flag"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/insights"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/templates"
)

// Run with -update to regenerate golden files.
var update = flag.Bool("update", false, "regenerate golden files")

// --------------------------------------------------------------------------
// Fixtures
// --------------------------------------------------------------------------

func fixedSignals() *insights.SignalSet {
	return &insights.SignalSet{
		TotalIssues:     12,
		TotalArticles:   5,
		TotalActivities: 30,
		ThemeCounts: map[insights.Theme]int{
			"infrastructure": 5,
			"feature":        4,
			"bug":            3,
		},
		Velocity: []insights.MonthBucket{
			{Month: "2026-01", Created: 4, Closed: 3},
			{Month: "2026-02", Created: 8, Closed: 9},
		},
		ProjectFocus: []insights.FocusItem{
			{Name: "ISRE", Count: 8},
			{Name: "AA", Count: 4},
		},
		SpaceFocus: []insights.FocusItem{
			{Name: "Engineering", Count: 3},
			{Name: "Platform", Count: 2},
		},
		RepoFocus: []insights.FocusItem{
			{Name: "infra-terraform", Count: 18},
			{Name: "my-service", Count: 12},
		},
		Collaboration: insights.CollaborationSignals{
			PRReviews:       7,
			IssueComments:   15,
			UniqueRepos:     4,
			CrossTeamIssues: 2,
		},
		Ownership: insights.OwnershipSignals{
			TotalClosed:        9,
			HighPriorityClosed: 3,
			IncidentRatio:      0.083,
		},
	}
}

func fixedSignalsWithLocal() *insights.SignalSet {
	s := fixedSignals()
	s.ShellActivity = &insights.ShellActivitySignals{
		TotalCommands:  342,
		DaysActive:     5,
		InfraCommands:  82,
		DeployCommands: 14,
		ToolCounts: map[string]int{
			"kubectl":   45,
			"terraform": 30,
			"git":       80,
			"go":        55,
			"aws":       7,
		},
		CategoryCounts: map[string]int{
			"kubernetes": 45,
			"terraform":  30,
			"git":        80,
			"general":    80,
			"aws":        7,
		},
		HourDistribution: func() [24]int {
			var h [24]int
			h[10] = 120
			h[14] = 95
			h[11] = 85
			return h
		}(),
	}
	s.AIActivity = &insights.AIActivitySignals{
		TotalSessions:  12,
		TotalMessages:  340,
		TotalToolCalls: 975,
		TotalTokens:    82000,
		DaysActive:     5,
		HumanCommands:  180,
		AgentCommands:  162,
		AgentProjects: []insights.FocusItem{
			{Name: "workctl-tool", Count: 95},
			{Name: "infra-terraform", Count: 67},
		},
	}
	return s
}

func fixedTrendPeriods() []templates.TrendPeriod {
	mkSig := func(issues, articles, activities, prReviews, comments, closed int) *insights.SignalSet {
		return &insights.SignalSet{
			TotalIssues:     issues,
			TotalArticles:   articles,
			TotalActivities: activities,
			Collaboration: insights.CollaborationSignals{
				PRReviews:     prReviews,
				IssueComments: comments,
			},
			Ownership: insights.OwnershipSignals{
				TotalClosed: closed,
			},
		}
	}
	return []templates.TrendPeriod{
		{Label: "Jan–Mar 2025", Signals: mkSig(8, 3, 20, 4, 10, 6)},
		{Label: "Apr–Jun 2025", Signals: mkSig(10, 4, 25, 5, 12, 8)},
		{Label: "Jul–Sep 2025", Signals: mkSig(11, 4, 28, 7, 14, 9)},
		{Label: "Oct–Dec 2025", Signals: mkSig(12, 5, 30, 7, 15, 9)},
	}
}

func fixedTrendPeriodsWithTrack() []templates.TrendPeriod {
	periods := fixedTrendPeriods()
	scores := []float64{0.65, 0.68, 0.71, 0.75}
	for i := range periods {
		periods[i].TrackResult = &insights.TrackResult{
			Track:   "staff",
			Overall: scores[i],
		}
	}
	return periods
}

func fixedDeltaReport() *insights.DeltaReport {
	return &insights.DeltaReport{
		PreviousPeriod: "2025-10-26 to 2026-01-24",
		CurrentPeriod:  "2026-01-25 to 2026-04-25",
		Items: []insights.DeltaItem{
			{Metric: "Total Jira Issues", Previous: 10, Current: 12, Delta: 2, PctDelta: 20},
			{Metric: "Total Confluence Articles", Previous: 4, Current: 5, Delta: 1, PctDelta: 25},
			{Metric: "Total GitHub Activities", Previous: 25, Current: 30, Delta: 5, PctDelta: 20},
			{Metric: "Theme: infrastructure", Previous: 6, Current: 5, Delta: -1, PctDelta: -16.67},
			{Metric: "Avg Monthly Created", Previous: 3, Current: 6, Delta: 3, PctDelta: 100},
			{Metric: "PR Reviews", Previous: 0, Current: 7, Delta: 7, PctDelta: math.NaN()},
		},
	}
}

func fixedTrackResult() *insights.TrackResult {
	return &insights.TrackResult{
		Track:       "staff",
		Description: "Staff Engineer — technical leadership and cross-team impact",
		Overall:     0.723,
		Dimensions: []insights.DimensionScore{
			{Name: "cross_team_impact", Raw: 0.67, Normalized: 0.67, Weight: 0.30, Weighted: 0.201},
			{Name: "pr_review_ratio", Raw: 0.58, Normalized: 0.58, Weight: 0.25, Weighted: 0.145},
			{Name: "multi_project_span", Raw: 2.00, Normalized: 0.40, Weight: 0.20, Weighted: 0.080},
			{Name: "infra_theme_ratio", Raw: 0.42, Normalized: 0.42, Weight: 0.15, Weighted: 0.063},
			{Name: "change_velocity", Raw: 6.50, Normalized: 0.33, Weight: 0.10, Weighted: 0.033},
		},
	}
}

// --------------------------------------------------------------------------
// Golden file helpers
// --------------------------------------------------------------------------

func goldenPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", name+".golden.md")
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := goldenPath(t, name)

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating testdata dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing golden file %s: %v", path, err)
		}
		t.Logf("updated golden file: %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file %s (run with -update to create): %v", path, err)
	}

	if string(want) != got {
		// Show a diff-friendly error: find first differing line.
		wantLines := strings.Split(string(want), "\n")
		gotLines := strings.Split(got, "\n")
		for i := 0; i < len(wantLines) && i < len(gotLines); i++ {
			if wantLines[i] != gotLines[i] {
				t.Errorf("golden mismatch at line %d:\n  want: %q\n   got: %q",
					i+1, wantLines[i], gotLines[i])
				break
			}
		}
		if len(wantLines) != len(gotLines) {
			t.Errorf("golden line count: want %d, got %d", len(wantLines), len(gotLines))
		}
		t.Logf("run `go test ./internal/templates/... -update` to regenerate golden files")
	}
}

// --------------------------------------------------------------------------
// Tests
// --------------------------------------------------------------------------

func TestRenderWeekly(t *testing.T) {
	var sb strings.Builder
	data := templates.NewWeeklyData(fixedSignals(), "2026-01-25 to 2026-02-23")
	data.Generated = "2026-02-23 12:00" // fix timestamp for golden comparison

	if err := templates.RenderWeeklyData(&sb, data); err != nil {
		t.Fatalf("RenderWeekly: %v", err)
	}
	checkGolden(t, "weekly", sb.String())
}

// TestRenderWeekly_WithLocal is the golden file test for the weekly report
// when Shell Activity and AI-Assisted Work sections are populated.
func TestRenderWeekly_WithLocal(t *testing.T) {
	var sb strings.Builder
	data := templates.NewWeeklyData(fixedSignalsWithLocal(), "2026-02-17 to 2026-02-23")
	data.Generated = "2026-02-23 12:00" // fix timestamp for golden comparison

	if err := templates.RenderWeeklyData(&sb, data); err != nil {
		t.Fatalf("RenderWeekly with local: %v", err)
	}
	checkGolden(t, "weekly_with_local", sb.String())
}

func TestRenderQuarterly(t *testing.T) {
	var sb strings.Builder
	data := templates.NewQuarterlyData(fixedDeltaReport())
	data.Generated = "2026-02-23 12:00"

	if err := templates.RenderQuarterlyData(&sb, data); err != nil {
		t.Fatalf("RenderQuarterly: %v", err)
	}
	checkGolden(t, "quarterly", sb.String())
}

func TestRenderReview(t *testing.T) {
	var sb strings.Builder
	data := templates.NewReviewData(fixedSignals(), fixedTrackResult(), "2025-02-23 to 2026-02-23")
	data.Generated = "2026-02-23 12:00"

	if err := templates.RenderReviewData(&sb, data); err != nil {
		t.Fatalf("RenderReview: %v", err)
	}
	checkGolden(t, "review", sb.String())
}

func TestRenderTrends(t *testing.T) {
	var sb strings.Builder
	periods := fixedTrendPeriods()
	data := templates.NewTrendData(periods, "3m")
	data.Generated = "2026-02-23 12:00" // fix timestamp for golden comparison

	if err := templates.RenderTrendsData(&sb, data); err != nil {
		t.Fatalf("RenderTrendsData: %v", err)
	}
	checkGolden(t, "trends", sb.String())
}

func TestRenderTrends_WithTrack(t *testing.T) {
	var sb strings.Builder
	periods := fixedTrendPeriodsWithTrack()
	data := templates.NewTrendData(periods, "3m")
	data.Generated = "2026-02-23 12:00"

	if err := templates.RenderTrendsData(&sb, data); err != nil {
		t.Fatalf("RenderTrendsData with track: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Career Track Scores") {
		t.Error("expected career track section in output")
	}
	if !strings.Contains(out, "staff") {
		t.Error("expected track name in output")
	}
}

func TestRenderTrends_AllTracks(t *testing.T) {
	var sb strings.Builder
	periods := fixedTrendPeriods()
	// Add multiple track results per period
	for i := range periods {
		periods[i].TrackResults = []*insights.TrackResult{
			{Track: "manager", Overall: 0.30 + float64(i)*0.02},
			{Track: "platform", Overall: 0.50 + float64(i)*0.03},
			{Track: "staff", Overall: 0.65 + float64(i)*0.03},
		}
	}

	data := templates.NewTrendData(periods, "3m")
	data.Generated = "2026-02-23 12:00"

	if err := templates.RenderTrendsData(&sb, data); err != nil {
		t.Fatalf("RenderTrendsData all-tracks: %v", err)
	}
	out := sb.String()

	// Should have all 3 track names
	for _, name := range []string{"manager", "platform", "staff"} {
		if !strings.Contains(out, name) {
			t.Errorf("expected track %q in output", name)
		}
	}
	// Should have career track section
	if !strings.Contains(out, "Career Track Scores") {
		t.Error("expected Career Track Scores section")
	}
}

// --------------------------------------------------------------------------
// Edge case tests
// --------------------------------------------------------------------------

func TestRenderWeekly_Empty(t *testing.T) {
	var sb strings.Builder
	empty := &insights.SignalSet{
		ThemeCounts: make(map[insights.Theme]int),
	}
	if err := templates.RenderWeekly(&sb, empty, "2026-02-23 to 2026-02-23"); err != nil {
		t.Fatalf("RenderWeekly empty: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Career Growth Insights") {
		t.Error("expected header in empty output")
	}
}

func TestRenderQuarterly_NaNPct(t *testing.T) {
	// Verify NaN % change is rendered as em-dash, not "NaN%"
	var sb strings.Builder
	report := &insights.DeltaReport{
		PreviousPeriod: "prev",
		CurrentPeriod:  "curr",
		Items: []insights.DeltaItem{
			{Metric: "PR Reviews", Previous: 0, Current: 7, Delta: 7, PctDelta: math.NaN()},
		},
	}
	if err := templates.RenderQuarterly(&sb, report); err != nil {
		t.Fatalf("RenderQuarterly: %v", err)
	}
	out := sb.String()
	if strings.Contains(out, "NaN") {
		t.Errorf("NaN should render as em-dash, got output:\n%s", out)
	}
	if !strings.Contains(out, "—") {
		t.Errorf("expected em-dash for NaN pct, got output:\n%s", out)
	}
}

func TestRenderReview_ZeroScore(t *testing.T) {
	var sb strings.Builder
	result := &insights.TrackResult{
		Track:       "staff",
		Description: "Staff Engineer",
		Overall:     0,
		Dimensions:  nil,
	}
	if err := templates.RenderReview(&sb, fixedSignals(), result, "2026-02-23 to 2026-02-23"); err != nil {
		t.Fatalf("RenderReview zero score: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Career Track Analysis") {
		t.Error("expected career section in zero-score output")
	}
}

func TestRenderWeekly_WithShellAndAI(t *testing.T) {
	var sb strings.Builder
	if err := templates.RenderWeekly(&sb, fixedSignalsWithLocal(), "2026-02-17 to 2026-02-23"); err != nil {
		t.Fatalf("RenderWeekly with local data: %v", err)
	}
	out := sb.String()

	// Shell Activity section
	if !strings.Contains(out, "## Shell Activity") {
		t.Error("expected Shell Activity section")
	}
	if !strings.Contains(out, "342") { // TotalCommands
		t.Error("expected TotalCommands=342 in output")
	}
	if !strings.Contains(out, "24%") { // InfraRatioPct = 82/342 ≈ 24%
		t.Error("expected infra ratio ~24% in output")
	}
	if !strings.Contains(out, "10am") { // peak hour
		t.Error("expected peak hour 10am in output")
	}
	if !strings.Contains(out, "Command Categories") {
		t.Error("expected Command Categories subsection")
	}
	if !strings.Contains(out, "Top Tools") {
		t.Error("expected Top Tools subsection")
	}
	if !strings.Contains(out, "kubectl") {
		t.Error("expected kubectl in top tools")
	}

	// AI-Assisted Work section
	if !strings.Contains(out, "## AI-Assisted Work") {
		t.Error("expected AI-Assisted Work section")
	}
	if !strings.Contains(out, "975") { // TotalToolCalls
		t.Error("expected TotalToolCalls=975 in output")
	}
	if !strings.Contains(out, "workctl-tool") { // AgentProject
		t.Error("expected agent project in output")
	}
}

func TestRenderWeekly_NoLocalData(t *testing.T) {
	// Signals with no local activity set → sections must be absent.
	var sb strings.Builder
	if err := templates.RenderWeekly(&sb, fixedSignals(), "2026-02-17 to 2026-02-23"); err != nil {
		t.Fatalf("RenderWeekly no local data: %v", err)
	}
	out := sb.String()
	if strings.Contains(out, "Shell Activity") {
		t.Error("Shell Activity section should be absent when ShellActivity is nil")
	}
	if strings.Contains(out, "AI-Assisted Work") {
		t.Error("AI-Assisted Work section should be absent when AIActivity is nil")
	}
}

func TestRenderReview_WithLocalData(t *testing.T) {
	var sb strings.Builder
	result := fixedTrackResult()
	if err := templates.RenderReview(&sb, fixedSignalsWithLocal(), result, "2025-02-23 to 2026-02-23"); err != nil {
		t.Fatalf("RenderReview with local data: %v", err)
	}
	out := sb.String()

	// Both local sections should appear before the career track section.
	shellIdx := strings.Index(out, "## Shell Activity")
	aiIdx := strings.Index(out, "## AI-Assisted Work")
	careerIdx := strings.Index(out, "# Career Track Analysis")

	if shellIdx < 0 {
		t.Error("expected Shell Activity section in review output")
	}
	if aiIdx < 0 {
		t.Error("expected AI-Assisted Work section in review output")
	}
	if careerIdx < 0 {
		t.Error("expected Career Track Analysis section in review output")
	}
	// Local sections should precede the career track section.
	if shellIdx > careerIdx {
		t.Error("Shell Activity should appear before Career Track Analysis")
	}
	if aiIdx > careerIdx {
		t.Error("AI-Assisted Work should appear before Career Track Analysis")
	}
}
