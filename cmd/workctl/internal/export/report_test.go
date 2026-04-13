package export_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/export"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/insights"
)

// math is used for infra_ratio_pct verification
var _ = math.Round

func testSignals() *insights.SignalSet {
	return &insights.SignalSet{
		TotalIssues:     10,
		TotalArticles:   3,
		TotalActivities: 20,
		ThemeCounts: map[insights.Theme]int{
			"infrastructure": 6,
			"feature":        4,
		},
		Velocity: []insights.MonthBucket{
			{Month: "2026-01", Created: 5, Closed: 4},
		},
		ProjectFocus: []insights.FocusItem{{Name: "ISRE", Count: 10}},
		Collaboration: insights.CollaborationSignals{
			PRReviews: 5, UniqueRepos: 2,
		},
		Ownership: insights.OwnershipSignals{
			TotalClosed: 4, IncidentRatio: 0.10,
		},
	}
}

func testDelta() *insights.DeltaReport {
	nan := math.NaN()
	return &insights.DeltaReport{
		PreviousPeriod: "prev",
		CurrentPeriod:  "curr",
		Items: []insights.DeltaItem{
			{Metric: "Jira Issues", Previous: 8, Current: 10, Delta: 2, PctDelta: 25},
			{Metric: "PR Reviews", Previous: 0, Current: 5, Delta: 5, PctDelta: nan},
			{Metric: "Articles", Previous: 5, Current: 3, Delta: -2, PctDelta: -40},
		},
	}
}

func testTrack() *insights.TrackResult {
	return &insights.TrackResult{
		Track:       "staff",
		Description: "Staff Engineer",
		Overall:     0.75,
		Dimensions: []insights.DimensionScore{
			{Name: "cross_team_impact", Raw: 0.8, Normalized: 0.8, Weight: 0.30, Weighted: 0.24},
			{Name: "pr_review_ratio", Raw: 0.5, Normalized: 0.5, Weight: 0.25, Weighted: 0.125},
		},
	}
}

// --------------------------------------------------------------------------
// Weekly JSON
// --------------------------------------------------------------------------

func TestWriteWeeklyJSON_Structure(t *testing.T) {
	var sb strings.Builder
	if err := export.WriteWeeklyJSON(&sb, testSignals(), "2026-01-01 to 2026-01-31"); err != nil {
		t.Fatalf("WriteWeeklyJSON: %v", err)
	}

	var out map[string]interface{}
	if err := json.Unmarshal([]byte(sb.String()), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, sb.String())
	}

	if out["type"] != "weekly" {
		t.Errorf("type: want %q, got %q", "weekly", out["type"])
	}
	if out["period"] != "2026-01-01 to 2026-01-31" {
		t.Errorf("period mismatch: %v", out["period"])
	}
	data, ok := out["data"].(map[string]interface{})
	if !ok {
		t.Fatal("data is not an object")
	}
	if data["total_issues"].(float64) != 10 {
		t.Errorf("total_issues: want 10, got %v", data["total_issues"])
	}
}

func TestWriteWeeklyJSON_ThemesOrdered(t *testing.T) {
	var sb strings.Builder
	export.WriteWeeklyJSON(&sb, testSignals(), "p") //nolint:errcheck

	var out struct {
		Data struct {
			Themes []struct {
				Name  string  `json:"name"`
				Count int     `json:"count"`
				Pct   float64 `json:"pct"`
			} `json:"themes"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(sb.String()), &out) //nolint:errcheck

	if len(out.Data.Themes) != 2 {
		t.Fatalf("want 2 themes, got %d", len(out.Data.Themes))
	}
	// infrastructure (6) should be first
	if out.Data.Themes[0].Name != "infrastructure" {
		t.Errorf("first theme: want infrastructure, got %s", out.Data.Themes[0].Name)
	}
	if out.Data.Themes[0].Pct != 60.0 {
		t.Errorf("infrastructure pct: want 60, got %v", out.Data.Themes[0].Pct)
	}
}

// --------------------------------------------------------------------------
// Quarterly JSON
// --------------------------------------------------------------------------

func TestWriteQuarterlyJSON_NaNBecomesNull(t *testing.T) {
	var sb strings.Builder
	if err := export.WriteQuarterlyJSON(&sb, testDelta()); err != nil {
		t.Fatalf("WriteQuarterlyJSON: %v", err)
	}

	// Verify valid JSON
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(sb.String()), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, sb.String())
	}

	// Find the PR Reviews row and check pct_delta is null
	data := out["data"].(map[string]interface{})
	items := data["items"].([]interface{})
	found := false
	for _, raw := range items {
		item := raw.(map[string]interface{})
		if item["metric"] == "PR Reviews" {
			found = true
			if item["pct_delta"] != nil {
				t.Errorf("PR Reviews pct_delta: want null, got %v", item["pct_delta"])
			}
			if item["trend"] != "up" {
				t.Errorf("PR Reviews trend: want up, got %v", item["trend"])
			}
		}
	}
	if !found {
		t.Error("PR Reviews row not found in output")
	}
}

func TestWriteQuarterlyJSON_DownTrend(t *testing.T) {
	var sb strings.Builder
	export.WriteQuarterlyJSON(&sb, testDelta()) //nolint:errcheck

	var out struct {
		Data struct {
			Items []struct {
				Metric string  `json:"metric"`
				Trend  string  `json:"trend"`
				Delta  float64 `json:"delta"`
			} `json:"items"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(sb.String()), &out) //nolint:errcheck

	for _, item := range out.Data.Items {
		if item.Metric == "Articles" {
			if item.Trend != "down" {
				t.Errorf("Articles trend: want down, got %s", item.Trend)
			}
			if item.Delta != -2 {
				t.Errorf("Articles delta: want -2, got %v", item.Delta)
			}
			return
		}
	}
	t.Error("Articles row not found")
}

// --------------------------------------------------------------------------
// Review JSON
// --------------------------------------------------------------------------

// --------------------------------------------------------------------------
// Trends JSON
// --------------------------------------------------------------------------

func testTrendPeriods() []export.TrendPeriodData {
	return []export.TrendPeriodData{
		{
			Label: "Jan–Mar 2025",
			Signals: &insights.SignalSet{
				TotalIssues: 8, TotalArticles: 3, TotalActivities: 20,
				Collaboration: insights.CollaborationSignals{PRReviews: 4, IssueComments: 10},
				Ownership:     insights.OwnershipSignals{TotalClosed: 6},
			},
		},
		{
			Label: "Apr–Jun 2025",
			Signals: &insights.SignalSet{
				TotalIssues: 10, TotalArticles: 4, TotalActivities: 25,
				Collaboration: insights.CollaborationSignals{PRReviews: 5, IssueComments: 12},
				Ownership:     insights.OwnershipSignals{TotalClosed: 8},
			},
		},
		{
			Label: "Jul–Sep 2025",
			Signals: &insights.SignalSet{
				TotalIssues: 11, TotalArticles: 4, TotalActivities: 28,
				Collaboration: insights.CollaborationSignals{PRReviews: 7, IssueComments: 14},
				Ownership:     insights.OwnershipSignals{TotalClosed: 9},
			},
		},
		{
			Label: "Oct–Dec 2025",
			Signals: &insights.SignalSet{
				TotalIssues: 12, TotalArticles: 5, TotalActivities: 30,
				Collaboration: insights.CollaborationSignals{PRReviews: 7, IssueComments: 15},
				Ownership:     insights.OwnershipSignals{TotalClosed: 9},
			},
		},
	}
}

func testTrendPeriodsWithTrack() []export.TrendPeriodData {
	periods := testTrendPeriods()
	scores := []float64{0.65, 0.68, 0.71, 0.75}
	for i := range periods {
		periods[i].TrackResult = &insights.TrackResult{
			Track:   "staff",
			Overall: scores[i],
		}
	}
	return periods
}

func TestWriteTrendsJSON_Structure(t *testing.T) {
	var sb strings.Builder
	if err := export.WriteTrendsJSON(&sb, testTrendPeriods(), "3m"); err != nil {
		t.Fatalf("WriteTrendsJSON: %v", err)
	}

	var out map[string]interface{}
	if err := json.Unmarshal([]byte(sb.String()), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, sb.String())
	}

	if out["type"] != "trends" {
		t.Errorf("type: want %q, got %q", "trends", out["type"])
	}
	if _, ok := out["generated"]; !ok {
		t.Error("missing generated field")
	}

	data := out["data"].(map[string]interface{})
	if data["period_size"] != "3 months" {
		t.Errorf("period_size: want %q, got %q", "3 months", data["period_size"])
	}

	periods := data["periods"].([]interface{})
	if len(periods) != 4 {
		t.Errorf("periods: want 4, got %d", len(periods))
	}
	if periods[0] != "Jan–Mar 2025" {
		t.Errorf("first period: want %q, got %q", "Jan–Mar 2025", periods[0])
	}
}

func TestWriteTrendsJSON_Metrics(t *testing.T) {
	var sb strings.Builder
	export.WriteTrendsJSON(&sb, testTrendPeriods(), "3m") //nolint:errcheck

	var out struct {
		Data struct {
			Metrics []struct {
				Name   string `json:"name"`
				Values []int  `json:"values"`
				Trend  string `json:"trend"`
			} `json:"metrics"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(sb.String()), &out) //nolint:errcheck

	if len(out.Data.Metrics) != 6 {
		t.Fatalf("want 6 metrics, got %d", len(out.Data.Metrics))
	}

	// Jira Issues should be first with values [8, 10, 11, 12] and trend "up"
	m := out.Data.Metrics[0]
	if m.Name != "Jira Issues" {
		t.Errorf("first metric: want Jira Issues, got %s", m.Name)
	}
	wantVals := []int{8, 10, 11, 12}
	for i, v := range m.Values {
		if v != wantVals[i] {
			t.Errorf("Jira Issues[%d]: want %d, got %d", i, wantVals[i], v)
		}
	}
	if m.Trend != "up" {
		t.Errorf("Jira Issues trend: want up, got %s", m.Trend)
	}

	// PR Reviews: [4,5,7,7] — trend "up" (last > first)
	pr := out.Data.Metrics[3]
	if pr.Name != "PR Reviews" {
		t.Errorf("metric[3]: want PR Reviews, got %s", pr.Name)
	}
	if pr.Trend != "up" {
		t.Errorf("PR Reviews trend: want up, got %s", pr.Trend)
	}
}

func TestWriteTrendsJSON_TrackScores(t *testing.T) {
	var sb strings.Builder
	export.WriteTrendsJSON(&sb, testTrendPeriodsWithTrack(), "3m") //nolint:errcheck

	var out struct {
		Data struct {
			Tracks []struct {
				Name   string     `json:"name"`
				Scores []*float64 `json:"scores"`
				Trend  string     `json:"trend"`
			} `json:"tracks"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(sb.String()), &out) //nolint:errcheck

	if len(out.Data.Tracks) != 1 {
		t.Fatalf("want 1 track, got %d", len(out.Data.Tracks))
	}

	tr := out.Data.Tracks[0]
	if tr.Name != "staff" {
		t.Errorf("track name: want staff, got %s", tr.Name)
	}
	if tr.Trend != "up" {
		t.Errorf("track trend: want up, got %s", tr.Trend)
	}
	if len(tr.Scores) != 4 {
		t.Fatalf("want 4 scores, got %d", len(tr.Scores))
	}
	// 0.65 * 100 = 65.0
	if tr.Scores[0] == nil || *tr.Scores[0] != 65.0 {
		t.Errorf("score[0]: want 65.0, got %v", tr.Scores[0])
	}
	// 0.75 * 100 = 75.0
	if tr.Scores[3] == nil || *tr.Scores[3] != 75.0 {
		t.Errorf("score[3]: want 75.0, got %v", tr.Scores[3])
	}
}

func TestWriteTrendsJSON_NoTrack(t *testing.T) {
	var sb strings.Builder
	export.WriteTrendsJSON(&sb, testTrendPeriods(), "3m") //nolint:errcheck

	var out struct {
		Data struct {
			Tracks []interface{} `json:"tracks"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(sb.String()), &out) //nolint:errcheck

	if len(out.Data.Tracks) != 0 {
		t.Errorf("want 0 tracks when no TrackResult, got %d", len(out.Data.Tracks))
	}
}

func testTrendPeriodsAllTracks() []export.TrendPeriodData {
	periods := testTrendPeriods()
	for i := range periods {
		periods[i].TrackResults = []*insights.TrackResult{
			{Track: "manager", Overall: 0.30 + float64(i)*0.02},
			{Track: "platform", Overall: 0.50 + float64(i)*0.03},
			{Track: "staff", Overall: 0.65 + float64(i)*0.03},
		}
	}
	return periods
}

func TestWriteTrendsJSON_AllTracks(t *testing.T) {
	var sb strings.Builder
	export.WriteTrendsJSON(&sb, testTrendPeriodsAllTracks(), "3m") //nolint:errcheck

	var out struct {
		Data struct {
			Tracks []struct {
				Name   string     `json:"name"`
				Scores []*float64 `json:"scores"`
				Trend  string     `json:"trend"`
			} `json:"tracks"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(sb.String()), &out) //nolint:errcheck

	// 3 tracks, sorted alphabetically
	if len(out.Data.Tracks) != 3 {
		t.Fatalf("want 3 tracks, got %d", len(out.Data.Tracks))
	}
	if out.Data.Tracks[0].Name != "manager" {
		t.Errorf("first track: want manager, got %s", out.Data.Tracks[0].Name)
	}
	if out.Data.Tracks[1].Name != "platform" {
		t.Errorf("second track: want platform, got %s", out.Data.Tracks[1].Name)
	}
	if out.Data.Tracks[2].Name != "staff" {
		t.Errorf("third track: want staff, got %s", out.Data.Tracks[2].Name)
	}

	// Each track should have 4 scores and "up" trend
	for _, tr := range out.Data.Tracks {
		if len(tr.Scores) != 4 {
			t.Errorf("track %s: want 4 scores, got %d", tr.Name, len(tr.Scores))
		}
		if tr.Trend != "up" {
			t.Errorf("track %s: want trend up, got %s", tr.Name, tr.Trend)
		}
	}
}

func TestWriteTrendsJSON_FlatTrend(t *testing.T) {
	periods := []export.TrendPeriodData{
		{
			Label: "Q1",
			Signals: &insights.SignalSet{
				TotalIssues: 10,
			},
		},
		{
			Label: "Q2",
			Signals: &insights.SignalSet{
				TotalIssues: 10,
			},
		},
	}

	var sb strings.Builder
	export.WriteTrendsJSON(&sb, periods, "3m") //nolint:errcheck

	var out struct {
		Data struct {
			Metrics []struct {
				Name  string `json:"name"`
				Trend string `json:"trend"`
			} `json:"metrics"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(sb.String()), &out) //nolint:errcheck

	if out.Data.Metrics[0].Trend != "flat" {
		t.Errorf("Jira Issues trend: want flat, got %s", out.Data.Metrics[0].Trend)
	}
}

// --------------------------------------------------------------------------
// Local activity JSON (EPIC-015 M6)
// --------------------------------------------------------------------------

func testSignalsWithLocal() *insights.SignalSet {
	s := testSignals()
	s.ShellActivity = &insights.ShellActivitySignals{
		TotalCommands:  342,
		DaysActive:     5,
		InfraCommands:  82,
		DeployCommands: 14,
		ToolCounts: map[string]int{
			"git":       80,
			"go":        55,
			"terraform": 30,
		},
		CategoryCounts: map[string]int{
			"git":        80,
			"general":    55,
			"terraform":  30,
			"kubernetes": 45,
			"aws":        7,
		},
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

func TestWriteWeeklyJSON_LocalActivityPresent(t *testing.T) {
	var sb strings.Builder
	if err := export.WriteWeeklyJSON(&sb, testSignalsWithLocal(), "2026-02-17 to 2026-02-23"); err != nil {
		t.Fatalf("WriteWeeklyJSON: %v", err)
	}

	var out struct {
		Data struct {
			ShellActivity *struct {
				TotalCommands  int     `json:"total_commands"`
				InfraCommands  int     `json:"infra_commands"`
				InfraRatioPct  float64 `json:"infra_ratio_pct"`
				DeployCommands int     `json:"deploy_commands"`
				Categories     []struct {
					Name  string  `json:"name"`
					Count int     `json:"count"`
					Pct   float64 `json:"pct"`
				} `json:"categories"`
				TopTools []struct {
					Name  string  `json:"name"`
					Count int     `json:"count"`
					Pct   float64 `json:"pct"`
				} `json:"top_tools"`
			} `json:"shell_activity"`
			AIActivity *struct {
				TotalSessions  int `json:"total_sessions"`
				TotalToolCalls int `json:"total_tool_calls"`
				TotalTokens    int `json:"total_tokens"`
				AgentProjects  []struct {
					Name  string `json:"name"`
					Count int    `json:"count"`
				} `json:"agent_projects"`
			} `json:"ai_activity"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(sb.String()), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, sb.String())
	}

	sa := out.Data.ShellActivity
	if sa == nil {
		t.Fatal("shell_activity should be present when ShellActivity is set")
	}
	if sa.TotalCommands != 342 {
		t.Errorf("total_commands: want 342, got %d", sa.TotalCommands)
	}
	if sa.InfraCommands != 82 {
		t.Errorf("infra_commands: want 82, got %d", sa.InfraCommands)
	}
	// InfraRatioPct = round(82/342 * 100, 2) = 23.98
	wantRatio := math.Round(float64(82)/float64(342)*10000) / 100
	if sa.InfraRatioPct != wantRatio {
		t.Errorf("infra_ratio_pct: want %.2f, got %.2f", wantRatio, sa.InfraRatioPct)
	}
	if sa.DeployCommands != 14 {
		t.Errorf("deploy_commands: want 14, got %d", sa.DeployCommands)
	}
	if len(sa.Categories) == 0 {
		t.Error("categories should be non-empty")
	}
	if len(sa.TopTools) == 0 {
		t.Error("top_tools should be non-empty")
	}
	// Top tool should be git (count=80, highest)
	if sa.TopTools[0].Name != "git" {
		t.Errorf("top tool: want git, got %s", sa.TopTools[0].Name)
	}

	aa := out.Data.AIActivity
	if aa == nil {
		t.Fatal("ai_activity should be present when AIActivity is set")
	}
	if aa.TotalSessions != 12 {
		t.Errorf("total_sessions: want 12, got %d", aa.TotalSessions)
	}
	if aa.TotalToolCalls != 975 {
		t.Errorf("total_tool_calls: want 975, got %d", aa.TotalToolCalls)
	}
	if aa.TotalTokens != 82000 {
		t.Errorf("total_tokens: want 82000, got %d", aa.TotalTokens)
	}
	if len(aa.AgentProjects) != 2 {
		t.Errorf("agent_projects: want 2, got %d", len(aa.AgentProjects))
	}
	if aa.AgentProjects[0].Name != "workctl-tool" {
		t.Errorf("first agent project: want workctl-tool, got %s", aa.AgentProjects[0].Name)
	}
}

func TestWriteWeeklyJSON_LocalActivityAbsent(t *testing.T) {
	// Without local data, shell_activity and ai_activity should be omitted (not null).
	var sb strings.Builder
	if err := export.WriteWeeklyJSON(&sb, testSignals(), "2026-02-17 to 2026-02-23"); err != nil {
		t.Fatalf("WriteWeeklyJSON: %v", err)
	}

	var out map[string]interface{}
	if err := json.Unmarshal([]byte(sb.String()), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, sb.String())
	}

	data := out["data"].(map[string]interface{})
	if _, ok := data["shell_activity"]; ok {
		t.Error("shell_activity key should be absent (omitempty) when ShellActivity is nil")
	}
	if _, ok := data["ai_activity"]; ok {
		t.Error("ai_activity key should be absent (omitempty) when AIActivity is nil")
	}
}

func TestWriteReviewJSON_WithLocalActivity(t *testing.T) {
	// AC: workctl review --format json includes shell_activity and ai_activity.
	var sb strings.Builder
	if err := export.WriteReviewJSON(&sb, testSignalsWithLocal(), testTrack(), "2025-01-01 to 2026-01-01"); err != nil {
		t.Fatalf("WriteReviewJSON: %v", err)
	}

	var out map[string]interface{}
	if err := json.Unmarshal([]byte(sb.String()), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, sb.String())
	}

	if out["type"] != "review" {
		t.Errorf("type: want review, got %v", out["type"])
	}
	data := out["data"].(map[string]interface{})

	if _, ok := data["shell_activity"]; !ok {
		t.Error("shell_activity missing from review JSON output")
	}
	if _, ok := data["ai_activity"]; !ok {
		t.Error("ai_activity missing from review JSON output")
	}

	// Verify track fields still present
	if data["track"] != "staff" {
		t.Errorf("track: want staff, got %v", data["track"])
	}
}

// --------------------------------------------------------------------------
// Review JSON
// --------------------------------------------------------------------------

func TestWriteReviewJSON_Structure(t *testing.T) {
	var sb strings.Builder
	if err := export.WriteReviewJSON(&sb, testSignals(), testTrack(), "2025-01-01 to 2026-01-01"); err != nil {
		t.Fatalf("WriteReviewJSON: %v", err)
	}

	var out map[string]interface{}
	if err := json.Unmarshal([]byte(sb.String()), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, sb.String())
	}

	if out["type"] != "review" {
		t.Errorf("type: want review, got %v", out["type"])
	}
	data := out["data"].(map[string]interface{})
	if data["track"] != "staff" {
		t.Errorf("track: want staff, got %v", data["track"])
	}
	if data["overall_pct"].(float64) != 75.0 {
		t.Errorf("overall_pct: want 75, got %v", data["overall_pct"])
	}

	dims, ok := data["dimensions"].([]interface{})
	if !ok || len(dims) != 2 {
		t.Errorf("dimensions: want 2, got %v", data["dimensions"])
	}
}
