package insights

import (
	"fmt"
	"html"
	"io"
	"strings"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

// StandupNotes holds manually-authored narrative sections for the standup page.
// Populated from a YAML sidecar file via --standup-notes.
type StandupNotes struct {
	Learnings    []string `yaml:"learnings"`
	NextWeekPlan []string `yaml:"next_week_plan"`
}

// StandupOpts holds all inputs needed to render a Confluence standup HTML page.
type StandupOpts struct {
	AuthorName string
	Period     string    // "2026-02-17 to 2026-02-24" — used as fallback when Start is zero
	Start      time.Time // period start date; used for title formatting when non-zero
	End        time.Time // period end date; used for title formatting when non-zero
	Issues     []models.Issue
	Activities []models.GitHubActivity
	Signals    *SignalSet
	Notes      *StandupNotes // nil → placeholder sections rendered
	Generated  time.Time
	Version    string
}

// FormatStandupTitle formats a Confluence page title from the period start and end dates.
//
//	Same month:   "Week 08 | February 17 - 24, 2026"
//	Cross-month:  "Week 05 | January 29 - February 5, 2026"
func FormatStandupTitle(start, end time.Time) string {
	_, week := start.ISOWeek()
	if start.Month() == end.Month() {
		return fmt.Sprintf("Week %02d | %s %d - %d, %d",
			week, start.Month(), start.Day(), end.Day(), end.Year())
	}
	return fmt.Sprintf("Week %02d | %s %d - %s %d, %d",
		week, start.Month(), start.Day(), end.Month(), end.Day(), end.Year())
}

// RenderStandupHTML writes a Confluence storage-format HTML standup page to w.
// All user-supplied string data is HTML-escaped. Static HTML structure is written
// as literal markup.
func RenderStandupHTML(w io.Writer, opts StandupOpts) error {
	e := html.EscapeString
	h := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\n", args...)
	}

	sig := opts.Signals
	if sig == nil {
		sig = &SignalSet{ThemeCounts: make(map[Theme]int)}
	}

	// Title
	var title string
	if !opts.Start.IsZero() {
		title = FormatStandupTitle(opts.Start, opts.End)
	} else {
		title = fmt.Sprintf("Week %d — %s", weekNumFromPeriod(opts.Period), opts.Period)
	}
	h("<h1>%s</h1>", e(title))
	h("<h2>%s — Personal Weekly Standup</h2>", e(opts.AuthorName))

	// Weekly Summary
	prCount := countEvents(opts.Activities, "PullRequestEvent")
	topTheme := topThemeName(sig.ThemeCounts)
	topRepo := ""
	if len(sig.RepoFocus) > 0 {
		topRepo = sig.RepoFocus[0].Name
	}
	primaryFocus := buildPrimaryFocus(topTheme, topRepo)

	h("<h2>📊 Weekly Summary</h2>")
	h("<ul>")
	h("  <li><strong>Activity Level:</strong> %s (%d events)</li>",
		e(ActivityLevel(sig.TotalActivities)), sig.TotalActivities)
	h("  <li><strong>Tickets:</strong> %d completed, %d total</li>",
		sig.Ownership.TotalClosed, sig.TotalIssues)
	h("  <li><strong>PRs:</strong> %d opened/merged</li>", prCount)
	h("  <li><strong>Primary Focus:</strong> %s</li>", e(primaryFocus))
	h("</ul>")

	// Completed Work
	done, wip := partitionIssues(opts.Issues)
	prs := filterEvents(opts.Activities, "PullRequestEvent")

	h("<h2>✅ Completed Work</h2>")
	h("<h3>Jira Tickets</h3>")
	if len(done) == 0 {
		h("<ul><li><em>No completed tickets in this period.</em></li></ul>")
	} else {
		h("<ul>")
		for _, iss := range done {
			h("  <li><a href=\"%s\">%s</a> — %s</li>",
				e(iss.URL), e(iss.Key), e(iss.Fields.Summary))
		}
		h("</ul>")
	}

	h("<h3>GitHub Pull Requests</h3>")
	if len(prs) == 0 {
		h("<ul><li><em>No pull requests in this period.</em></li></ul>")
	} else {
		h("<ul>")
		for _, pr := range prs {
			lineInfo := ""
			if pr.Enriched {
				lineInfo = fmt.Sprintf(" <em>(+%d/-%d, %d files)</em>",
					pr.LinesAdded, pr.LinesRemoved, len(pr.FilesChanged))
			}
			if pr.URL != "" {
				h("  <li><a href=\"%s\">%s</a>%s</li>", e(pr.URL), e(pr.Description), lineInfo)
			} else {
				h("  <li>%s%s</li>", e(pr.Description), lineInfo)
			}
		}
		h("</ul>")
	}

	// Code Impact
	h("<h2>📊 Code Impact</h2>")
	if len(sig.RepoFocus) == 0 {
		h("<p><em>No code activity in this period.</em></p>")
	} else {
		limit := 10
		if len(sig.RepoFocus) < limit {
			limit = len(sig.RepoFocus)
		}
		h("<table>")
		h("  <tr><th>Repository</th><th>Events</th></tr>")
		for _, f := range sig.RepoFocus[:limit] {
			h("  <tr><td>%s</td><td>%d</td></tr>", e(f.Name), f.Count)
		}
		h("</table>")
	}

	// Key Activities
	h("<h2>🎯 Key Activities</h2>")
	h("<ul>")
	wrote := false
	for _, theme := range themeOrder {
		if n := sig.ThemeCounts[theme]; n > 0 {
			h("  <li><strong>%s:</strong> %d items</li>", e(string(theme)), n)
			wrote = true
		}
	}
	if sig.Collaboration.PRReviews > 0 {
		h("  <li><strong>PR Reviews:</strong> %d</li>", sig.Collaboration.PRReviews)
		wrote = true
	}
	if !wrote {
		h("  <li><em>No activity themes detected.</em></li>")
	}
	h("</ul>")

	// Learnings & Insights
	h("<h2>📝 Learnings &amp; Insights</h2>")
	if opts.Notes != nil && len(opts.Notes.Learnings) > 0 {
		h("<ul>")
		for _, l := range opts.Notes.Learnings {
			h("  <li>%s</li>", e(l))
		}
		h("</ul>")
	} else {
		h("<ul><li><em>Add learnings via --standup-notes notes.yaml</em></li></ul>")
	}

	// Work In Progress
	h("<h2>🔧 Work In Progress</h2>")
	if len(wip) == 0 {
		h("<ul><li><em>No in-progress tickets.</em></li></ul>")
	} else {
		h("<ul>")
		for _, iss := range wip {
			h("  <li><a href=\"%s\">%s</a> — %s <em>(%s)</em></li>",
				e(iss.URL), e(iss.Key), e(iss.Fields.Summary), e(iss.Fields.Status.Name))
		}
		h("</ul>")
	}

	// Next Week Plans
	h("<h2>📅 Next Week Plans</h2>")
	if opts.Notes != nil && len(opts.Notes.NextWeekPlan) > 0 {
		h("<ol>")
		for _, p := range opts.Notes.NextWeekPlan {
			h("  <li>%s</li>", e(p))
		}
		h("</ol>")
	} else {
		h("<ol><li><em>Add plans via --standup-notes notes.yaml</em></li></ol>")
	}

	// Footer
	gen := opts.Generated
	if gen.IsZero() {
		gen = time.Now().UTC()
	}
	ver := opts.Version
	if ver == "" {
		ver = "workctl"
	}
	h("<hr/>")
	h("<p><em>Generated: %s by %s</em></p>", e(gen.UTC().Format("2006-01-02 15:04 UTC")), e(ver))

	return nil
}

// ActivityLevel classifies a total activity count as High / Medium / Low.
func ActivityLevel(total int) string {
	switch {
	case total > 150:
		return "High"
	case total >= 50:
		return "Medium"
	default:
		return "Low"
	}
}

// themeOrder controls the display order for Key Activities.
var themeOrder = []Theme{
	ThemeFeature, ThemeInfrastructure, ThemeBug, ThemeMaintenance, ThemeIncident, ThemeOther,
}

// weekNumFromPeriod extracts the ISO week number from a period string like
// "2026-01-29 to 2026-02-05". Returns 0 if the string can't be parsed.
func weekNumFromPeriod(period string) int {
	parts := strings.SplitN(period, " to ", 2)
	if len(parts) == 0 {
		return 0
	}
	t, err := time.Parse("2006-01-02", strings.TrimSpace(parts[0]))
	if err != nil {
		return 0
	}
	_, week := t.ISOWeek()
	return week
}

// topThemeName returns the display name of the Theme with the highest count.
func topThemeName(m map[Theme]int) string {
	var best Theme
	var bestN int
	for k, n := range m {
		if n > bestN {
			best = k
			bestN = n
		}
	}
	return string(best)
}

// buildPrimaryFocus produces the "theme / repo" focus string.
func buildPrimaryFocus(theme, repo string) string {
	switch {
	case theme != "" && repo != "":
		return theme + " / " + repo
	case repo != "":
		return repo
	default:
		return theme
	}
}

// partitionIssues splits issues into completed and in-progress buckets.
func partitionIssues(issues []models.Issue) (done, wip []models.Issue) {
	for _, iss := range issues {
		s := strings.ToLower(iss.Fields.Status.Name)
		switch {
		case s == "done" || s == "closed" || s == "resolved" || s == "complete" || s == "completed":
			done = append(done, iss)
		case strings.Contains(s, "progress") || strings.Contains(s, "review") || s == "in development":
			wip = append(wip, iss)
		}
	}
	return done, wip
}

// filterEvents returns activities matching eventType.
func filterEvents(activities []models.GitHubActivity, eventType string) []models.GitHubActivity {
	var out []models.GitHubActivity
	for _, a := range activities {
		if a.EventType == eventType {
			out = append(out, a)
		}
	}
	return out
}

// countEvents counts activities matching eventType.
func countEvents(activities []models.GitHubActivity, eventType string) int {
	n := 0
	for _, a := range activities {
		if a.EventType == eventType {
			n++
		}
	}
	return n
}
