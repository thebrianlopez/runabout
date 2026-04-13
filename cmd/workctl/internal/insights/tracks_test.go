package insights

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func populatedSignalSet() *SignalSet {
	return &SignalSet{
		ThemeCounts: map[Theme]int{
			ThemeFeature:        10,
			ThemeBug:            5,
			ThemeInfrastructure: 8,
			ThemeIncident:       2,
			ThemeMaintenance:    5,
		},
		Velocity: []MonthBucket{
			{Month: "2026-01", Created: 12, Closed: 10},
			{Month: "2026-02", Created: 8, Closed: 14},
		},
		ProjectFocus: []FocusItem{
			{Name: "SR", Count: 15},
			{Name: "ISRE", Count: 10},
			{Name: "PLAT", Count: 5},
		},
		RepoFocus: []FocusItem{
			{Name: "org/repo1", Count: 20},
			{Name: "org/repo2", Count: 15},
			{Name: "org/repo3", Count: 5},
		},
		SpaceFocus: []FocusItem{
			{Name: "ENG", Count: 8},
			{Name: "INFRA", Count: 4},
		},
		Collaboration: CollaborationSignals{
			PRReviews:       25,
			IssueComments:   30,
			CrossTeamIssues: 15,
			UniqueRepos:     6,
		},
		Ownership: OwnershipSignals{
			TotalClosed:        20,
			HighPriorityClosed: 5,
			IncidentRatio:      0.066,
		},
		TotalIssues:     30,
		TotalArticles:   12,
		TotalActivities: 80,
	}
}

func TestScoreTrack_AllBuiltinTracks(t *testing.T) {
	signals := populatedSignalSet()

	for _, track := range ListTracks(nil) {
		t.Run(track, func(t *testing.T) {
			result, err := ScoreTrack(track, signals, nil, nil)
			if err != nil {
				t.Fatalf("ScoreTrack(%q): %v", track, err)
			}

			// Score in [0, 1]
			if result.Overall < 0 || result.Overall > 1.0 {
				t.Errorf("overall score %.3f out of [0,1]", result.Overall)
			}

			// 10 dimensions
			if len(result.Dimensions) != 10 {
				t.Errorf("dimensions count = %d, want 10", len(result.Dimensions))
			}

			// Weights sum to 1.0
			var weightSum float64
			for _, d := range result.Dimensions {
				weightSum += d.Weight
			}
			if math.Abs(weightSum-1.0) > 0.001 {
				t.Errorf("weight sum = %.3f, want 1.0", weightSum)
			}

			// All normalized values in [0, 1]
			for _, d := range result.Dimensions {
				if d.Normalized < 0 || d.Normalized > 1.0 {
					t.Errorf("dimension %q normalized = %.3f, want [0,1]", d.Name, d.Normalized)
				}
			}
		})
	}
}

func TestScoreTrack_EmptySignals(t *testing.T) {
	signals := &SignalSet{
		ThemeCounts: make(map[Theme]int),
	}

	result, err := ScoreTrack("staff", signals, nil, nil)
	if err != nil {
		t.Fatalf("ScoreTrack: %v", err)
	}

	// Should not panic, should have valid result
	if result.Track != "staff" {
		t.Errorf("track = %q, want staff", result.Track)
	}
	if len(result.Dimensions) != 10 {
		t.Errorf("dimensions count = %d, want 10", len(result.Dimensions))
	}

	// Empty signals: incident_reduction dimension should be 1.0 (1 - 0)
	for _, d := range result.Dimensions {
		if d.Name == "incident_reduction" && d.Raw != 1.0 {
			t.Errorf("incident_reduction raw = %.3f, want 1.0 for empty signals", d.Raw)
		}
	}
}

func TestScoreTrack_ExtremeValues(t *testing.T) {
	signals := populatedSignalSet()
	signals.Collaboration.PRReviews = 10000
	signals.Collaboration.CrossTeamIssues = 10000
	signals.TotalActivities = 100

	result, err := ScoreTrack("staff", signals, nil, nil)
	if err != nil {
		t.Fatalf("ScoreTrack: %v", err)
	}

	// All normalized values capped at 1.0
	for _, d := range result.Dimensions {
		if d.Normalized > 1.0 {
			t.Errorf("dimension %q normalized = %.3f, should be capped at 1.0", d.Name, d.Normalized)
		}
	}
}

func TestScoreTrack_CeilingOverrides(t *testing.T) {
	signals := populatedSignalSet()

	// Score with default ceilings
	defaultResult, err := ScoreTrack("staff", signals, nil, nil)
	if err != nil {
		t.Fatalf("ScoreTrack default: %v", err)
	}

	// Score with much higher ceilings (should lower normalized scores)
	overrides := map[string]float64{
		"change_velocity":    100.0,
		"multi_project_span": 50.0,
		"collaborator_span":  80.0,
	}
	overrideResult, err := ScoreTrack("staff", signals, overrides, nil)
	if err != nil {
		t.Fatalf("ScoreTrack override: %v", err)
	}

	// Higher ceilings should produce lower or equal overall score
	if overrideResult.Overall > defaultResult.Overall+0.001 {
		t.Errorf("override score %.3f > default score %.3f (higher ceilings should lower scores)",
			overrideResult.Overall, defaultResult.Overall)
	}
}

func TestScoreTrack_UnknownTrack(t *testing.T) {
	signals := populatedSignalSet()

	_, err := ScoreTrack("nonexistent", signals, nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown track")
	}
	if !strings.Contains(err.Error(), "unknown track") {
		t.Errorf("error = %q, want to contain 'unknown track'", err.Error())
	}
	if !strings.Contains(err.Error(), "manager") {
		t.Errorf("error = %q, should list available tracks", err.Error())
	}
}

func TestListTracks(t *testing.T) {
	tracks := ListTracks(nil)
	if len(tracks) != 3 {
		t.Errorf("tracks count = %d, want 3", len(tracks))
	}
	// Should be sorted
	for i := 1; i < len(tracks); i++ {
		if tracks[i] < tracks[i-1] {
			t.Errorf("tracks not sorted: %v", tracks)
			break
		}
	}
	// Check expected names
	expected := map[string]bool{"staff": true, "platform": true, "manager": true}
	for _, name := range tracks {
		if !expected[name] {
			t.Errorf("unexpected track %q", name)
		}
	}
}

func TestResolveCeilings_Nil(t *testing.T) {
	merged := ResolveCeilings(nil)
	if len(merged) != len(defaultCeilings) {
		t.Errorf("merged count = %d, want %d", len(merged), len(defaultCeilings))
	}
	for k, v := range defaultCeilings {
		if merged[k] != v {
			t.Errorf("ceiling %q = %.1f, want %.1f", k, merged[k], v)
		}
	}
}

func TestRenderCareer(t *testing.T) {
	signals := populatedSignalSet()
	result, err := ScoreTrack("staff", signals, nil, nil)
	if err != nil {
		t.Fatalf("ScoreTrack: %v", err)
	}

	var buf bytes.Buffer
	RenderCareer(&buf, result, "2026-01-01 to 2026-02-20")
	output := buf.String()

	checks := []string{
		"# Career Track Analysis: staff",
		"**Track:** Staff Engineer",
		"**Period:** 2026-01-01 to 2026-02-20",
		"**Overall Score:**",
		"## Dimension Scores",
		"| Dimension |",
		"cross_team_impact",
		"change_velocity",
		"## Interpretation",
		"**Strengths:**",
		"**Growth Areas:**",
	}
	for _, want := range checks {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

// --- New tests for custom tracks ---

func TestScoreTrack_CustomTrack(t *testing.T) {
	signals := populatedSignalSet()

	custom := map[string]CustomTrack{
		"security": {
			Description: "Security Engineer — incident focus",
			Weights: map[string]float64{
				"cross_team_impact":   0.05,
				"pr_review_ratio":     0.10,
				"multi_project_span":  0.05,
				"infra_theme_ratio":   0.10,
				"change_velocity":     0.10,
				"incident_reduction":  0.40,
				"pr_comment_ratio":    0.10,
				"collaborator_span":   0.10,
				"operational_cadence": 0.00,
			},
		},
	}

	result, err := ScoreTrack("security", signals, nil, custom)
	if err != nil {
		t.Fatalf("ScoreTrack: %v", err)
	}

	if result.Track != "security" {
		t.Errorf("track = %q, want security", result.Track)
	}
	if result.Description != "Security Engineer — incident focus" {
		t.Errorf("description = %q, want custom description", result.Description)
	}

	// Verify custom weights are used
	for _, d := range result.Dimensions {
		if d.Name == "incident_reduction" && d.Weight != 0.40 {
			t.Errorf("incident_reduction weight = %.2f, want 0.40", d.Weight)
		}
	}

	// Score in valid range
	if result.Overall < 0 || result.Overall > 1.0 {
		t.Errorf("overall score %.3f out of [0,1]", result.Overall)
	}
}

func TestScoreTrack_CustomOverridesBuiltin(t *testing.T) {
	signals := populatedSignalSet()

	// Custom track named "staff" should override the builtin
	custom := map[string]CustomTrack{
		"staff": {
			Description: "Custom Staff Track",
			Weights: map[string]float64{
				"cross_team_impact":  0.00,
				"pr_review_ratio":    0.00,
				"multi_project_span": 0.00,
				"infra_theme_ratio":  0.00,
				"change_velocity":    1.00,
				"incident_reduction": 0.00,
				"pr_comment_ratio":   0.00,
				"collaborator_span":  0.00,
			},
		},
	}

	result, err := ScoreTrack("staff", signals, nil, custom)
	if err != nil {
		t.Fatalf("ScoreTrack: %v", err)
	}

	if result.Description != "Custom Staff Track" {
		t.Errorf("description = %q, want custom override", result.Description)
	}

	// Only change_velocity should have non-zero weight
	for _, d := range result.Dimensions {
		if d.Name == "change_velocity" {
			if d.Weight != 1.00 {
				t.Errorf("change_velocity weight = %.2f, want 1.00", d.Weight)
			}
		} else {
			if d.Weight != 0.00 {
				t.Errorf("%s weight = %.2f, want 0.00", d.Name, d.Weight)
			}
		}
	}
}

func TestListTracks_WithCustom(t *testing.T) {
	custom := map[string]CustomTrack{
		"security": {Description: "Security track"},
		"data":     {Description: "Data track"},
	}

	tracks := ListTracks(custom)

	// Should have 3 builtins + 2 custom = 5
	if len(tracks) != 5 {
		t.Errorf("tracks count = %d, want 5", len(tracks))
	}

	// Should be sorted
	for i := 1; i < len(tracks); i++ {
		if tracks[i] < tracks[i-1] {
			t.Errorf("tracks not sorted: %v", tracks)
			break
		}
	}

	// Check all expected names present
	expected := map[string]bool{
		"staff": true, "platform": true, "manager": true,
		"security": true, "data": true,
	}
	for _, name := range tracks {
		if !expected[name] {
			t.Errorf("unexpected track %q", name)
		}
	}
}

func TestListTracks_CustomOverlapDeduplicates(t *testing.T) {
	// Custom track with same name as builtin should not duplicate
	custom := map[string]CustomTrack{
		"staff": {Description: "Custom Staff"},
	}

	tracks := ListTracks(custom)
	if len(tracks) != 3 {
		t.Errorf("tracks count = %d, want 3 (deduplicated)", len(tracks))
	}
}

func TestValidateTrackWeights_Valid(t *testing.T) {
	weights := map[string]float64{
		"cross_team_impact":  0.20,
		"pr_review_ratio":    0.10,
		"multi_project_span": 0.15,
		"infra_theme_ratio":  0.05,
		"change_velocity":    0.20,
		"incident_reduction": 0.05,
		"pr_comment_ratio":   0.10,
		"collaborator_span":  0.15,
	}
	if err := ValidateTrackWeights(weights); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateTrackWeights_BadSum(t *testing.T) {
	weights := map[string]float64{
		"cross_team_impact":  0.50,
		"pr_review_ratio":    0.50,
		"multi_project_span": 0.50,
	}
	err := ValidateTrackWeights(weights)
	if err == nil {
		t.Fatal("expected error for bad weight sum")
	}
	if !strings.Contains(err.Error(), "sum to") {
		t.Errorf("error = %q, want to contain 'sum to'", err.Error())
	}
}

// --- Inheritance tests ---

func TestResolveTrack_InheritBuiltin(t *testing.T) {
	custom := map[string]CustomTrack{
		"senior-staff": {
			Description: "Senior Staff — broad influence",
			Inherit:     "staff",
			Weights: map[string]float64{
				"cross_team_impact": 0.25, // was 0.20
				"collaborator_span": 0.20, // was 0.15
				"change_velocity":   0.10, // was 0.20: reduced by 0.10 to keep sum=1.0
			},
		},
	}

	weights, desc, err := ResolveTrack("senior-staff", custom)
	if err != nil {
		t.Fatalf("ResolveTrack: %v", err)
	}
	if desc != "Senior Staff — broad influence" {
		t.Errorf("desc = %q", desc)
	}

	// Overridden dimensions
	if weights["cross_team_impact"] != 0.25 {
		t.Errorf("cross_team_impact = %v, want 0.25", weights["cross_team_impact"])
	}
	if weights["collaborator_span"] != 0.20 {
		t.Errorf("collaborator_span = %v, want 0.20", weights["collaborator_span"])
	}
	if weights["change_velocity"] != 0.10 {
		t.Errorf("change_velocity = %v, want 0.10", weights["change_velocity"])
	}

	// Inherited dimensions unchanged from staff
	if weights["pr_review_ratio"] != 0.10 {
		t.Errorf("pr_review_ratio = %v, want 0.10 (inherited from staff)", weights["pr_review_ratio"])
	}
	if weights["multi_project_span"] != 0.15 {
		t.Errorf("multi_project_span = %v, want 0.15 (inherited from staff)", weights["multi_project_span"])
	}
}

func TestResolveTrack_InheritChain(t *testing.T) {
	custom := map[string]CustomTrack{
		"level-a": {
			Description: "Level A",
			Inherit:     "staff",
			Weights: map[string]float64{
				"cross_team_impact": 0.30, // override +0.10
				"change_velocity":   0.10, // override -0.10 to keep sum
			},
		},
		"level-b": {
			Description: "Level B",
			Inherit:     "level-a",
			Weights: map[string]float64{
				"pr_review_ratio":   0.15, // override +0.05
				"infra_theme_ratio": 0.00, // override -0.05 to keep sum
			},
		},
	}

	weights, desc, err := ResolveTrack("level-b", custom)
	if err != nil {
		t.Fatalf("ResolveTrack: %v", err)
	}
	if desc != "Level B" {
		t.Errorf("desc = %q", desc)
	}

	// From level-a override (inherited by level-b)
	if weights["cross_team_impact"] != 0.30 {
		t.Errorf("cross_team_impact = %v, want 0.30 (from level-a)", weights["cross_team_impact"])
	}
	if weights["change_velocity"] != 0.10 {
		t.Errorf("change_velocity = %v, want 0.10 (from level-a)", weights["change_velocity"])
	}
	// From level-b override
	if weights["pr_review_ratio"] != 0.15 {
		t.Errorf("pr_review_ratio = %v, want 0.15 (level-b override)", weights["pr_review_ratio"])
	}
	// Inherited from staff (untouched by either)
	if weights["multi_project_span"] != 0.15 {
		t.Errorf("multi_project_span = %v, want 0.15 (from staff)", weights["multi_project_span"])
	}
}

func TestResolveTrack_CycleDetection(t *testing.T) {
	custom := map[string]CustomTrack{
		"track-a": {
			Description: "A",
			Inherit:     "track-b",
			Weights:     map[string]float64{},
		},
		"track-b": {
			Description: "B",
			Inherit:     "track-a",
			Weights:     map[string]float64{},
		},
	}

	_, _, err := ResolveTrack("track-a", custom)
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error = %q, want to contain 'cycle'", err.Error())
	}
}

func TestResolveTrack_SelfInheritCycle(t *testing.T) {
	custom := map[string]CustomTrack{
		"self-ref": {
			Description: "Self",
			Inherit:     "self-ref",
			Weights:     map[string]float64{},
		},
	}

	_, _, err := ResolveTrack("self-ref", custom)
	if err == nil {
		t.Fatal("expected cycle detection error for self-inheritance")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error = %q, want to contain 'cycle'", err.Error())
	}
}

func TestScoreAllTracks(t *testing.T) {
	signals := populatedSignalSet()

	results, err := ScoreAllTracks(signals, nil, nil)
	if err != nil {
		t.Fatalf("ScoreAllTracks: %v", err)
	}

	// 3 builtin tracks
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}

	// Results should be sorted by track name
	for i := 1; i < len(results); i++ {
		if results[i].Track < results[i-1].Track {
			t.Errorf("results not sorted: %s < %s", results[i].Track, results[i-1].Track)
		}
	}

	// Each result should have valid scores
	for _, r := range results {
		if r.Overall < 0 || r.Overall > 1.0 {
			t.Errorf("track %s: overall %.3f out of [0,1]", r.Track, r.Overall)
		}
		if len(r.Dimensions) != 10 {
			t.Errorf("track %s: %d dimensions, want 10", r.Track, len(r.Dimensions))
		}
	}
}

func TestScoreAllTracks_WithCustom(t *testing.T) {
	signals := populatedSignalSet()

	custom := map[string]CustomTrack{
		"security": {
			Description: "Security Engineer",
			Weights: map[string]float64{
				"cross_team_impact":   0.05,
				"pr_review_ratio":     0.10,
				"multi_project_span":  0.05,
				"infra_theme_ratio":   0.10,
				"change_velocity":     0.10,
				"incident_reduction":  0.40,
				"pr_comment_ratio":    0.10,
				"collaborator_span":   0.10,
				"operational_cadence": 0.00,
			},
		},
	}

	results, err := ScoreAllTracks(signals, nil, custom)
	if err != nil {
		t.Fatalf("ScoreAllTracks: %v", err)
	}

	// 3 builtin + 1 custom = 4
	if len(results) != 4 {
		t.Fatalf("want 4 results, got %d", len(results))
	}

	// Check security track is present
	found := false
	for _, r := range results {
		if r.Track == "security" {
			found = true
		}
	}
	if !found {
		t.Error("security track not found in results")
	}
}

func TestValidateTrackWeights_UnknownDimension(t *testing.T) {
	weights := map[string]float64{
		"cross_team_impact": 0.50,
		"made_up_metric":    0.50,
	}
	err := ValidateTrackWeights(weights)
	if err == nil {
		t.Fatal("expected error for unknown dimension")
	}
	if !strings.Contains(err.Error(), "unknown dimension") {
		t.Errorf("error = %q, want to contain 'unknown dimension'", err.Error())
	}
	if !strings.Contains(err.Error(), "made_up_metric") {
		t.Errorf("error = %q, want to contain the bad dimension name", err.Error())
	}
}

// --- operational_cadence tests (EPIC-015 M5) ---

func TestOperationalCadence_WithShellData(t *testing.T) {
	signals := populatedSignalSet()
	signals.ShellActivity = &ShellActivitySignals{
		TotalCommands: 200,
		InfraCommands: 60, // 30% infra = raw 0.30
	}

	result, err := ScoreTrack("platform", signals, nil, nil)
	if err != nil {
		t.Fatalf("ScoreTrack: %v", err)
	}

	var cadenceDim *DimensionScore
	for i := range result.Dimensions {
		if result.Dimensions[i].Name == "operational_cadence" {
			cadenceDim = &result.Dimensions[i]
			break
		}
	}
	if cadenceDim == nil {
		t.Fatal("operational_cadence dimension missing from platform result")
	}

	// Raw = 60/200 = 0.30
	if math.Abs(cadenceDim.Raw-0.30) > 0.001 {
		t.Errorf("operational_cadence raw = %.4f, want 0.30", cadenceDim.Raw)
	}
	// Ceiling = 0.5 → normalized = 0.30/0.50 = 0.60
	if math.Abs(cadenceDim.Normalized-0.60) > 0.001 {
		t.Errorf("operational_cadence normalized = %.4f, want 0.60", cadenceDim.Normalized)
	}
	// Weight for platform = 0.10 → weighted = 0.60 * 0.10 = 0.06
	if math.Abs(cadenceDim.Weight-0.10) > 0.001 {
		t.Errorf("operational_cadence weight = %.4f, want 0.10 for platform track", cadenceDim.Weight)
	}
	if math.Abs(cadenceDim.Weighted-0.06) > 0.001 {
		t.Errorf("operational_cadence weighted = %.4f, want 0.06", cadenceDim.Weighted)
	}
}

func TestOperationalCadence_NoShellData(t *testing.T) {
	// Without shell data, operational_cadence should contribute 0.
	signals := populatedSignalSet()
	// signals.ShellActivity is nil — shell disabled

	result, err := ScoreTrack("platform", signals, nil, nil)
	if err != nil {
		t.Fatalf("ScoreTrack: %v", err)
	}

	for _, d := range result.Dimensions {
		if d.Name == "operational_cadence" {
			if d.Raw != 0 || d.Normalized != 0 || d.Weighted != 0 {
				t.Errorf("operational_cadence should be zero when ShellActivity is nil: raw=%.3f norm=%.3f weighted=%.3f",
					d.Raw, d.Normalized, d.Weighted)
			}
			return
		}
	}
	t.Error("operational_cadence dimension missing from result")
}

func TestOperationalCadence_Ceiling(t *testing.T) {
	// 80% infra commands → raw = 0.80, ceiling = 0.50 → normalized capped at 1.0
	signals := populatedSignalSet()
	signals.ShellActivity = &ShellActivitySignals{
		TotalCommands: 100,
		InfraCommands: 80,
	}

	result, err := ScoreTrack("platform", signals, nil, nil)
	if err != nil {
		t.Fatalf("ScoreTrack: %v", err)
	}

	for _, d := range result.Dimensions {
		if d.Name == "operational_cadence" {
			if d.Normalized > 1.0 {
				t.Errorf("operational_cadence normalized = %.4f, should be capped at 1.0", d.Normalized)
			}
			if d.Normalized != 1.0 {
				t.Errorf("operational_cadence normalized = %.4f, want 1.0 (above ceiling)", d.Normalized)
			}
			return
		}
	}
	t.Error("operational_cadence dimension missing")
}

func TestOperationalCadence_PlatformWeightHigherThanStaff(t *testing.T) {
	// platform track should weight operational_cadence more than staff track.
	signals := populatedSignalSet()
	signals.ShellActivity = &ShellActivitySignals{
		TotalCommands: 100,
		InfraCommands: 30,
	}

	platformResult, err := ScoreTrack("platform", signals, nil, nil)
	if err != nil {
		t.Fatalf("ScoreTrack platform: %v", err)
	}
	staffResult, err := ScoreTrack("staff", signals, nil, nil)
	if err != nil {
		t.Fatalf("ScoreTrack staff: %v", err)
	}

	var platformW, staffW float64
	for _, d := range platformResult.Dimensions {
		if d.Name == "operational_cadence" {
			platformW = d.Weight
		}
	}
	for _, d := range staffResult.Dimensions {
		if d.Name == "operational_cadence" {
			staffW = d.Weight
		}
	}

	if platformW <= staffW {
		t.Errorf("platform operational_cadence weight %.2f should be > staff weight %.2f", platformW, staffW)
	}
}

func TestOperationalCadence_ManagerWeightZero(t *testing.T) {
	signals := populatedSignalSet()
	signals.ShellActivity = &ShellActivitySignals{
		TotalCommands: 100,
		InfraCommands: 50,
	}

	result, err := ScoreTrack("manager", signals, nil, nil)
	if err != nil {
		t.Fatalf("ScoreTrack: %v", err)
	}

	for _, d := range result.Dimensions {
		if d.Name == "operational_cadence" {
			if d.Weight != 0.0 {
				t.Errorf("manager operational_cadence weight = %.2f, want 0.0", d.Weight)
			}
			if d.Weighted != 0.0 {
				t.Errorf("manager operational_cadence weighted = %.4f, want 0.0", d.Weighted)
			}
			return
		}
	}
	t.Error("operational_cadence dimension missing from manager result")
}

func TestValidateTrackWeights_OperationalCadenceAccepted(t *testing.T) {
	// operational_cadence should be accepted as a valid dimension name.
	weights := map[string]float64{
		"cross_team_impact":   0.10,
		"pr_review_ratio":     0.10,
		"multi_project_span":  0.05,
		"infra_theme_ratio":   0.20,
		"change_velocity":     0.10,
		"incident_reduction":  0.15,
		"pr_comment_ratio":    0.05,
		"collaborator_span":   0.10,
		"operational_cadence": 0.15,
	}
	if err := ValidateTrackWeights(weights); err != nil {
		t.Errorf("unexpected error for valid weights with operational_cadence: %v", err)
	}
}
