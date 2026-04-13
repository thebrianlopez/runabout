package insights

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// RenderInsights writes a structured markdown report from a SignalSet.
func RenderInsights(w io.Writer, s *SignalSet, period string) {
	fmt.Fprintf(w, "# Career Growth Insights\n\n")
	fmt.Fprintf(w, "**Generated:** %s  \n", time.Now().Format("2006-01-02 15:04"))
	fmt.Fprintf(w, "**Period:** %s  \n\n", period)
	fmt.Fprintf(w, "---\n\n")

	renderOverview(w, s)
	renderThemes(w, s)
	renderVelocity(w, s)
	renderFocus(w, s)
	renderCollaboration(w, s)
	renderOwnership(w, s)
}

func renderOverview(w io.Writer, s *SignalSet) {
	fmt.Fprintf(w, "## Overview\n\n")
	fmt.Fprintf(w, "| Metric | Count |\n")
	fmt.Fprintf(w, "|--------|------:|\n")
	fmt.Fprintf(w, "| Jira Issues | %d |\n", s.TotalIssues)
	fmt.Fprintf(w, "| Confluence Articles | %d |\n", s.TotalArticles)
	fmt.Fprintf(w, "| GitHub Activities | %d |\n\n", s.TotalActivities)
}

func renderThemes(w io.Writer, s *SignalSet) {
	if len(s.ThemeCounts) == 0 {
		return
	}

	fmt.Fprintf(w, "## Work Themes\n\n")
	fmt.Fprintf(w, "| Theme | Count | %% |\n")
	fmt.Fprintf(w, "|-------|------:|---:|\n")

	// Sort by count descending
	type kv struct {
		theme Theme
		count int
	}
	sorted := make([]kv, 0, len(s.ThemeCounts))
	for t, c := range s.ThemeCounts {
		sorted = append(sorted, kv{t, c})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })

	total := s.TotalIssues
	for _, item := range sorted {
		pct := 0.0
		if total > 0 {
			pct = float64(item.count) / float64(total) * 100
		}
		fmt.Fprintf(w, "| %s | %d | %.0f%% |\n", item.theme, item.count, pct)
	}
	fmt.Fprintln(w)
}

func renderVelocity(w io.Writer, s *SignalSet) {
	if len(s.Velocity) == 0 {
		return
	}

	fmt.Fprintf(w, "## Monthly Velocity\n\n")
	fmt.Fprintf(w, "| Month | Created | Closed | Net |\n")
	fmt.Fprintf(w, "|-------|--------:|-------:|----:|\n")

	for _, v := range s.Velocity {
		net := v.Closed - v.Created
		sign := ""
		if net > 0 {
			sign = "+"
		}
		fmt.Fprintf(w, "| %s | %d | %d | %s%d |\n", v.Month, v.Created, v.Closed, sign, net)
	}
	fmt.Fprintln(w)
}

func renderFocus(w io.Writer, s *SignalSet) {
	hasContent := len(s.ProjectFocus) > 0 || len(s.SpaceFocus) > 0 || len(s.RepoFocus) > 0
	if !hasContent {
		return
	}

	fmt.Fprintf(w, "## Focus Distribution\n\n")

	if len(s.ProjectFocus) > 0 {
		fmt.Fprintf(w, "### Projects\n\n")
		renderFocusTable(w, s.ProjectFocus)
	}
	if len(s.SpaceFocus) > 0 {
		fmt.Fprintf(w, "### Confluence Spaces\n\n")
		renderFocusTable(w, s.SpaceFocus)
	}
	if len(s.RepoFocus) > 0 {
		fmt.Fprintf(w, "### Repositories\n\n")
		renderFocusTable(w, s.RepoFocus)
	}
}

func renderFocusTable(w io.Writer, items []FocusItem) {
	total := 0
	for _, item := range items {
		total += item.Count
	}

	fmt.Fprintf(w, "| Name | Count | %% |\n")
	fmt.Fprintf(w, "|------|------:|---:|\n")
	for _, item := range items {
		pct := 0.0
		if total > 0 {
			pct = float64(item.Count) / float64(total) * 100
		}
		fmt.Fprintf(w, "| %s | %d | %.0f%% |\n", item.Name, item.Count, pct)
	}
	fmt.Fprintln(w)
}

func renderCollaboration(w io.Writer, s *SignalSet) {
	c := s.Collaboration
	if c.PRReviews == 0 && c.IssueComments == 0 && c.UniqueRepos == 0 && c.CrossTeamIssues == 0 {
		return
	}

	fmt.Fprintf(w, "## Collaboration Signals\n\n")
	fmt.Fprintf(w, "| Signal | Value |\n")
	fmt.Fprintf(w, "|--------|------:|\n")
	fmt.Fprintf(w, "| PR Reviews | %d |\n", c.PRReviews)
	fmt.Fprintf(w, "| Issue Comments | %d |\n", c.IssueComments)
	fmt.Fprintf(w, "| Unique Repos | %d |\n", c.UniqueRepos)
	fmt.Fprintf(w, "| Cross-Team Issues | %d |\n\n", c.CrossTeamIssues)
}

func renderOwnership(w io.Writer, s *SignalSet) {
	o := s.Ownership
	if o.TotalClosed == 0 && o.HighPriorityClosed == 0 {
		return
	}

	fmt.Fprintf(w, "## Ownership Signals\n\n")
	fmt.Fprintf(w, "| Signal | Value |\n")
	fmt.Fprintf(w, "|--------|------:|\n")
	fmt.Fprintf(w, "| Issues Closed | %d |\n", o.TotalClosed)
	fmt.Fprintf(w, "| High-Priority Closed | %d |\n", o.HighPriorityClosed)
	fmt.Fprintf(w, "| Incident Ratio | %.1f%% |\n\n", o.IncidentRatio*100)
}

// FormatPeriod builds a human-readable period string from start/end dates.
func FormatPeriod(start, end string) string {
	parts := []string{}
	if start != "" {
		parts = append(parts, start)
	}
	if end != "" {
		parts = append(parts, end)
	}
	if len(parts) == 2 {
		return parts[0] + " to " + parts[1]
	}
	return strings.Join(parts, " ")
}
