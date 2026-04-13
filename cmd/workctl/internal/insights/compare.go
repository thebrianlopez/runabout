package insights

import (
	"fmt"
	"io"
	"math"
	"sort"
)

// DeltaItem represents a single metric's change between two periods.
type DeltaItem struct {
	Metric   string
	Previous float64
	Current  float64
	Delta    float64 // Current - Previous
	PctDelta float64 // Percentage change (NaN if previous == 0)
}

// DeltaReport holds the full comparison between two periods.
type DeltaReport struct {
	PreviousPeriod string
	CurrentPeriod  string
	Items          []DeltaItem
}

// ComputeDelta compares two SignalSets and produces a DeltaReport.
func ComputeDelta(prev, curr *SignalSet, prevPeriod, currPeriod string) *DeltaReport {
	report := &DeltaReport{
		PreviousPeriod: prevPeriod,
		CurrentPeriod:  currPeriod,
	}

	// Volume metrics
	report.addDelta("Total Jira Issues", float64(prev.TotalIssues), float64(curr.TotalIssues))
	report.addDelta("Total Confluence Articles", float64(prev.TotalArticles), float64(curr.TotalArticles))
	report.addDelta("Total GitHub Activities", float64(prev.TotalActivities), float64(curr.TotalActivities))

	// Theme deltas
	allThemes := mergeThemeKeys(prev.ThemeCounts, curr.ThemeCounts)
	for _, theme := range allThemes {
		report.addDelta(
			fmt.Sprintf("Theme: %s", theme),
			float64(prev.ThemeCounts[theme]),
			float64(curr.ThemeCounts[theme]),
		)
	}

	// Velocity: average monthly created/closed
	prevAvgCreated, prevAvgClosed := avgVelocity(prev.Velocity)
	currAvgCreated, currAvgClosed := avgVelocity(curr.Velocity)
	report.addDelta("Avg Monthly Created", prevAvgCreated, currAvgCreated)
	report.addDelta("Avg Monthly Closed", prevAvgClosed, currAvgClosed)

	// Collaboration
	report.addDelta("PR Reviews", float64(prev.Collaboration.PRReviews), float64(curr.Collaboration.PRReviews))
	report.addDelta("Issue Comments", float64(prev.Collaboration.IssueComments), float64(curr.Collaboration.IssueComments))
	report.addDelta("Unique Repos", float64(prev.Collaboration.UniqueRepos), float64(curr.Collaboration.UniqueRepos))
	report.addDelta("Cross-Team Issues", float64(prev.Collaboration.CrossTeamIssues), float64(curr.Collaboration.CrossTeamIssues))

	// Ownership
	report.addDelta("Issues Closed", float64(prev.Ownership.TotalClosed), float64(curr.Ownership.TotalClosed))
	report.addDelta("High-Priority Closed", float64(prev.Ownership.HighPriorityClosed), float64(curr.Ownership.HighPriorityClosed))
	report.addDelta("Incident Ratio", prev.Ownership.IncidentRatio*100, curr.Ownership.IncidentRatio*100)

	return report
}

// addDelta appends a delta item to the report.
func (r *DeltaReport) addDelta(metric string, prev, curr float64) {
	delta := curr - prev
	pct := math.NaN()
	if prev != 0 {
		pct = (delta / prev) * 100
	}
	r.Items = append(r.Items, DeltaItem{
		Metric:   metric,
		Previous: prev,
		Current:  curr,
		Delta:    delta,
		PctDelta: pct,
	})
}

// RenderComparison writes a delta report as markdown.
func RenderComparison(w io.Writer, report *DeltaReport) {
	fmt.Fprintf(w, "# Growth Delta Report\n\n")
	fmt.Fprintf(w, "**Previous Period:** %s  \n", report.PreviousPeriod)
	fmt.Fprintf(w, "**Current Period:** %s  \n\n", report.CurrentPeriod)
	fmt.Fprintf(w, "---\n\n")

	fmt.Fprintf(w, "| Metric | Previous | Current | Delta | %% Change | Trend |\n")
	fmt.Fprintf(w, "|--------|--------:|--------:|------:|---------:|:-----:|\n")

	for _, item := range report.Items {
		pctStr := "—"
		if !math.IsNaN(item.PctDelta) {
			pctStr = formatSigned(item.PctDelta, "%")
		}
		trend := trendIcon(item.Delta)
		fmt.Fprintf(w, "| %s | %.0f | %.0f | %s | %s | %s |\n",
			item.Metric,
			item.Previous,
			item.Current,
			formatSigned(item.Delta, ""),
			pctStr,
			trend,
		)
	}
	fmt.Fprintln(w)
}

// avgVelocity computes average monthly created and closed from velocity buckets.
func avgVelocity(buckets []MonthBucket) (avgCreated, avgClosed float64) {
	if len(buckets) == 0 {
		return 0, 0
	}
	var totalCreated, totalClosed int
	for _, b := range buckets {
		totalCreated += b.Created
		totalClosed += b.Closed
	}
	return float64(totalCreated) / float64(len(buckets)),
		float64(totalClosed) / float64(len(buckets))
}

// mergeThemeKeys returns a sorted list of all themes present in either map.
func mergeThemeKeys(a, b map[Theme]int) []Theme {
	seen := make(map[Theme]bool)
	for t := range a {
		seen[t] = true
	}
	for t := range b {
		seen[t] = true
	}
	themes := make([]Theme, 0, len(seen))
	for t := range seen {
		themes = append(themes, t)
	}
	sort.Slice(themes, func(i, j int) bool { return themes[i] < themes[j] })
	return themes
}

// formatSigned formats a number with explicit +/- sign.
func formatSigned(v float64, suffix string) string {
	if v > 0 {
		return fmt.Sprintf("+%.0f%s", v, suffix)
	}
	return fmt.Sprintf("%.0f%s", v, suffix)
}

// trendIcon returns an arrow indicating direction.
func trendIcon(delta float64) string {
	switch {
	case delta > 0:
		return "^"
	case delta < 0:
		return "v"
	default:
		return "="
	}
}
