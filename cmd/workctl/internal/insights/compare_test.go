package insights

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func TestComputeDelta_ZeroPrevious(t *testing.T) {
	prev := &SignalSet{ThemeCounts: make(map[Theme]int)}
	curr := &SignalSet{
		TotalIssues:     10,
		TotalArticles:   5,
		TotalActivities: 20,
		ThemeCounts:     map[Theme]int{ThemeBug: 3},
	}

	report := ComputeDelta(prev, curr, "Q1", "Q2")

	// Find Total Jira Issues
	for _, item := range report.Items {
		if item.Metric == "Total Jira Issues" {
			if item.Previous != 0 {
				t.Errorf("Previous = %f, want 0", item.Previous)
			}
			if item.Current != 10 {
				t.Errorf("Current = %f, want 10", item.Current)
			}
			if item.Delta != 10 {
				t.Errorf("Delta = %f, want 10", item.Delta)
			}
			if !math.IsNaN(item.PctDelta) {
				t.Errorf("PctDelta = %f, want NaN (division by zero)", item.PctDelta)
			}
		}
	}
}

func TestComputeDelta_NormalValues(t *testing.T) {
	prev := &SignalSet{
		TotalIssues:     20,
		TotalArticles:   10,
		TotalActivities: 50,
		ThemeCounts: map[Theme]int{
			ThemeBug:     5,
			ThemeFeature: 10,
		},
		Velocity: []MonthBucket{
			{Month: "2025-07", Created: 10, Closed: 8},
			{Month: "2025-08", Created: 12, Closed: 10},
		},
		Collaboration: CollaborationSignals{
			PRReviews:       5,
			IssueComments:   10,
			UniqueRepos:     3,
			CrossTeamIssues: 2,
		},
		Ownership: OwnershipSignals{
			TotalClosed:        15,
			HighPriorityClosed: 4,
			IncidentRatio:      0.1,
		},
	}

	curr := &SignalSet{
		TotalIssues:     30,
		TotalArticles:   15,
		TotalActivities: 70,
		ThemeCounts: map[Theme]int{
			ThemeBug:            3,
			ThemeFeature:        15,
			ThemeInfrastructure: 5,
		},
		Velocity: []MonthBucket{
			{Month: "2026-01", Created: 15, Closed: 14},
			{Month: "2026-02", Created: 18, Closed: 16},
		},
		Collaboration: CollaborationSignals{
			PRReviews:       10,
			IssueComments:   8,
			UniqueRepos:     5,
			CrossTeamIssues: 4,
		},
		Ownership: OwnershipSignals{
			TotalClosed:        25,
			HighPriorityClosed: 3,
			IncidentRatio:      0.05,
		},
	}

	report := ComputeDelta(prev, curr, "H1 2025", "H2 2025")

	if report.PreviousPeriod != "H1 2025" {
		t.Errorf("PreviousPeriod = %q", report.PreviousPeriod)
	}
	if report.CurrentPeriod != "H2 2025" {
		t.Errorf("CurrentPeriod = %q", report.CurrentPeriod)
	}

	tests := []struct {
		metric   string
		wantPrev float64
		wantCurr float64
		wantPct  float64
	}{
		{"Total Jira Issues", 20, 30, 50},
		{"PR Reviews", 5, 10, 100},
		{"Issues Closed", 15, 25, 66.67}, // approx
	}

	for _, tt := range tests {
		t.Run(tt.metric, func(t *testing.T) {
			found := false
			for _, item := range report.Items {
				if item.Metric == tt.metric {
					found = true
					if item.Previous != tt.wantPrev {
						t.Errorf("Previous = %f, want %f", item.Previous, tt.wantPrev)
					}
					if item.Current != tt.wantCurr {
						t.Errorf("Current = %f, want %f", item.Current, tt.wantCurr)
					}
					if math.IsNaN(item.PctDelta) {
						t.Error("PctDelta is NaN")
					} else if math.Abs(item.PctDelta-tt.wantPct) > 1 {
						t.Errorf("PctDelta = %.2f, want ~%.2f", item.PctDelta, tt.wantPct)
					}
				}
			}
			if !found {
				t.Errorf("metric %q not found in report", tt.metric)
			}
		})
	}
}

func TestComputeDelta_IdenticalPeriods(t *testing.T) {
	s := &SignalSet{
		TotalIssues:     10,
		TotalArticles:   5,
		TotalActivities: 20,
		ThemeCounts:     map[Theme]int{ThemeBug: 5},
		Collaboration:   CollaborationSignals{PRReviews: 3},
		Ownership:       OwnershipSignals{TotalClosed: 8},
	}

	report := ComputeDelta(s, s, "Q1", "Q1")

	for _, item := range report.Items {
		if item.Delta != 0 {
			t.Errorf("metric %q: Delta = %f, want 0 for identical periods", item.Metric, item.Delta)
		}
		if !math.IsNaN(item.PctDelta) && item.PctDelta != 0 {
			t.Errorf("metric %q: PctDelta = %f, want 0", item.Metric, item.PctDelta)
		}
	}
}

func TestRenderComparison(t *testing.T) {
	report := &DeltaReport{
		PreviousPeriod: "2025-H1",
		CurrentPeriod:  "2025-H2",
		Items: []DeltaItem{
			{Metric: "Total Issues", Previous: 10, Current: 15, Delta: 5, PctDelta: 50},
			{Metric: "PR Reviews", Previous: 0, Current: 5, Delta: 5, PctDelta: math.NaN()},
			{Metric: "Bugs", Previous: 10, Current: 8, Delta: -2, PctDelta: -20},
			{Metric: "Same", Previous: 5, Current: 5, Delta: 0, PctDelta: 0},
		},
	}

	var buf bytes.Buffer
	RenderComparison(&buf, report)
	out := buf.String()

	checks := []string{
		"# Growth Delta Report",
		"**Previous Period:** 2025-H1",
		"**Current Period:** 2025-H2",
		"| Total Issues | 10 | 15 | +5 | +50% | ^ |",
		"| PR Reviews | 0 | 5 | +5 | — | ^ |", // NaN → "—"
		"| Bugs | 10 | 8 | -2 | -20% | v |",   // negative
		"| Same | 5 | 5 | 0 | 0% | = |",       // zero
	}

	for _, check := range checks {
		if !strings.Contains(out, check) {
			t.Errorf("missing in output: %q\nGot:\n%s", check, out)
		}
	}
}

func TestAvgVelocity(t *testing.T) {
	tests := []struct {
		name        string
		buckets     []MonthBucket
		wantCreated float64
		wantClosed  float64
	}{
		{"empty", nil, 0, 0},
		{"single", []MonthBucket{{Created: 10, Closed: 8}}, 10, 8},
		{"multiple", []MonthBucket{
			{Created: 10, Closed: 8},
			{Created: 20, Closed: 12},
		}, 15, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, cl := avgVelocity(tt.buckets)
			if c != tt.wantCreated {
				t.Errorf("avgCreated = %f, want %f", c, tt.wantCreated)
			}
			if cl != tt.wantClosed {
				t.Errorf("avgClosed = %f, want %f", cl, tt.wantClosed)
			}
		})
	}
}

func TestFormatSigned(t *testing.T) {
	tests := []struct {
		val    float64
		suffix string
		want   string
	}{
		{5, "", "+5"},
		{-3, "", "-3"},
		{0, "", "0"},
		{50, "%", "+50%"},
		{-20, "%", "-20%"},
	}

	for _, tt := range tests {
		got := formatSigned(tt.val, tt.suffix)
		if got != tt.want {
			t.Errorf("formatSigned(%f, %q) = %q, want %q", tt.val, tt.suffix, got, tt.want)
		}
	}
}

func TestTrendIcon(t *testing.T) {
	if trendIcon(5) != "^" {
		t.Error("positive should be ^")
	}
	if trendIcon(-3) != "v" {
		t.Error("negative should be v")
	}
	if trendIcon(0) != "=" {
		t.Error("zero should be =")
	}
}
