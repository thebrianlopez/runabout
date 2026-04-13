package export

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/insights"
)

// --------------------------------------------------------------------------
// JSON report types — raw numeric values, NaN-safe, stable key ordering
// --------------------------------------------------------------------------

// ReportEnvelope wraps any report payload for JSON output.
type ReportEnvelope struct {
	Type      string      `json:"type"`
	Generated string      `json:"generated"`
	Period    string      `json:"period,omitempty"`
	Data      interface{} `json:"data"`
}

// ThemeJSON is one entry in the themes list.
type ThemeJSON struct {
	Name  string  `json:"name"`
	Count int     `json:"count"`
	Pct   float64 `json:"pct"` // 0–100
}

// VelocityJSON is one monthly velocity bucket.
type VelocityJSON struct {
	Month   string `json:"month"`
	Created int    `json:"created"`
	Closed  int    `json:"closed"`
	Net     int    `json:"net"`
}

// FocusJSON is one focus distribution entry.
type FocusJSON struct {
	Name  string  `json:"name"`
	Count int     `json:"count"`
	Pct   float64 `json:"pct"` // 0–100
}

// CollabJSON holds collaboration signals.
type CollabJSON struct {
	PRReviews       int `json:"pr_reviews"`
	IssueComments   int `json:"issue_comments"`
	UniqueRepos     int `json:"unique_repos"`
	CrossTeamIssues int `json:"cross_team_issues"`
}

// OwnershipJSON holds ownership signals.
type OwnershipJSON struct {
	IssuesClosed       int     `json:"issues_closed"`
	HighPriorityClosed int     `json:"high_priority_closed"`
	IncidentRatioPct   float64 `json:"incident_ratio_pct"` // 0–100
}

// ShellActivityJSON is the JSON representation of shell activity signals.
// Omitted from output when shell data collection is disabled (--shell=false).
type ShellActivityJSON struct {
	TotalCommands  int         `json:"total_commands"`
	DaysActive     int         `json:"days_active"`
	InfraCommands  int         `json:"infra_commands"`
	InfraRatioPct  float64     `json:"infra_ratio_pct"` // 0–100
	DeployCommands int         `json:"deploy_commands"`
	Categories     []FocusJSON `json:"categories"`
	TopTools       []FocusJSON `json:"top_tools"`
}

// AIActivityJSON is the JSON representation of AI-assisted work signals.
// Omitted from output when AI stats collection is disabled (--ai-stats=false).
type AIActivityJSON struct {
	TotalSessions  int         `json:"total_sessions"`
	TotalMessages  int         `json:"total_messages"`
	TotalToolCalls int         `json:"total_tool_calls"`
	TotalTokens    int         `json:"total_tokens"`
	DaysActive     int         `json:"days_active"`
	HumanCommands  int         `json:"human_commands"`
	AgentCommands  int         `json:"agent_commands"`
	AgentProjects  []FocusJSON `json:"agent_projects"`
}

// WeeklyJSON is the JSON payload for a weekly report.
type WeeklyJSON struct {
	TotalIssues     int            `json:"total_issues"`
	TotalArticles   int            `json:"total_articles"`
	TotalActivities int            `json:"total_activities"`
	Themes          []ThemeJSON    `json:"themes"`
	Velocity        []VelocityJSON `json:"velocity"`
	Projects        []FocusJSON    `json:"projects"`
	Spaces          []FocusJSON    `json:"spaces"`
	Repos           []FocusJSON    `json:"repos"`
	Collaboration   CollabJSON     `json:"collaboration"`
	Ownership       OwnershipJSON  `json:"ownership"`

	// Local activity sections (EPIC-015); omitted when sources are disabled.
	ShellActivity *ShellActivityJSON `json:"shell_activity,omitempty"`
	AIActivity    *AIActivityJSON    `json:"ai_activity,omitempty"`
}

// DeltaItemJSON is one row in a quarterly delta report.
// PctDelta is nil when the previous value was zero (undefined % change).
type DeltaItemJSON struct {
	Metric   string   `json:"metric"`
	Previous float64  `json:"previous"`
	Current  float64  `json:"current"`
	Delta    float64  `json:"delta"`
	PctDelta *float64 `json:"pct_delta"` // null when undefined
	Trend    string   `json:"trend"`     // "up" | "down" | "flat"
}

// QuarterlyJSON is the JSON payload for a quarterly delta report.
type QuarterlyJSON struct {
	PreviousPeriod string          `json:"previous_period"`
	CurrentPeriod  string          `json:"current_period"`
	Items          []DeltaItemJSON `json:"items"`
}

// DimJSON is one dimension score in a career track result.
type DimJSON struct {
	Name       string  `json:"name"`
	Raw        float64 `json:"raw"`
	Normalized float64 `json:"normalized"`
	Weight     float64 `json:"weight"`
	Weighted   float64 `json:"weighted"`
}

// ReviewJSON is the JSON payload for a review report.
type ReviewJSON struct {
	WeeklyJSON
	Track      string    `json:"track"`
	TrackDesc  string    `json:"track_desc"`
	OverallPct float64   `json:"overall_pct"` // 0–100
	Dimensions []DimJSON `json:"dimensions"`
}

// --------------------------------------------------------------------------
// Trends JSON types
// --------------------------------------------------------------------------

// TrendPeriodData holds one period's data for JSON trend export.
type TrendPeriodData struct {
	Label        string
	Signals      *insights.SignalSet
	TrackResult  *insights.TrackResult   // single track (--track)
	TrackResults []*insights.TrackResult // all tracks (--all-tracks)
}

// TrendMetricJSON is one metric row across all periods in a trend report.
type TrendMetricJSON struct {
	Name   string `json:"name"`
	Values []int  `json:"values"`
	Trend  string `json:"trend"` // "up" | "down" | "flat"
}

// TrendTrackJSON is one career-track row across all periods.
type TrendTrackJSON struct {
	Name   string     `json:"name"`
	Scores []*float64 `json:"scores"` // 0–100; null when track was not scored
	Trend  string     `json:"trend"`  // "up" | "down" | "flat"
}

// TrendsJSON is the JSON payload for a multi-period trend report.
type TrendsJSON struct {
	PeriodSize string            `json:"period_size"`
	Periods    []string          `json:"periods"`
	Metrics    []TrendMetricJSON `json:"metrics"`
	Tracks     []TrendTrackJSON  `json:"tracks,omitempty"`
}

// --------------------------------------------------------------------------
// Conversion helpers
// --------------------------------------------------------------------------

// WeeklyToJSON converts a SignalSet to a WeeklyJSON struct.
func WeeklyToJSON(s *insights.SignalSet) WeeklyJSON {
	return WeeklyJSON{
		TotalIssues:     s.TotalIssues,
		TotalArticles:   s.TotalArticles,
		TotalActivities: s.TotalActivities,
		Themes:          themesToJSON(s.ThemeCounts, s.TotalIssues),
		Velocity:        velocityToJSON(s.Velocity),
		Projects:        focusToJSON(s.ProjectFocus),
		Spaces:          focusToJSON(s.SpaceFocus),
		Repos:           focusToJSON(s.RepoFocus),
		Collaboration: CollabJSON{
			PRReviews:       s.Collaboration.PRReviews,
			IssueComments:   s.Collaboration.IssueComments,
			UniqueRepos:     s.Collaboration.UniqueRepos,
			CrossTeamIssues: s.Collaboration.CrossTeamIssues,
		},
		Ownership: OwnershipJSON{
			IssuesClosed:       s.Ownership.TotalClosed,
			HighPriorityClosed: s.Ownership.HighPriorityClosed,
			IncidentRatioPct:   math.Round(s.Ownership.IncidentRatio*10000) / 100,
		},
		ShellActivity: shellActivityToJSON(s.ShellActivity),
		AIActivity:    aiActivityToJSON(s.AIActivity),
	}
}

// QuarterlyToJSON converts a DeltaReport to a QuarterlyJSON struct.
func QuarterlyToJSON(r *insights.DeltaReport) QuarterlyJSON {
	items := make([]DeltaItemJSON, 0, len(r.Items))
	for _, item := range r.Items {
		var pct *float64
		if !math.IsNaN(item.PctDelta) {
			v := math.Round(item.PctDelta*10) / 10
			pct = &v
		}
		trend := "flat"
		if item.Delta > 0 {
			trend = "up"
		} else if item.Delta < 0 {
			trend = "down"
		}
		items = append(items, DeltaItemJSON{
			Metric:   item.Metric,
			Previous: item.Previous,
			Current:  item.Current,
			Delta:    item.Delta,
			PctDelta: pct,
			Trend:    trend,
		})
	}
	return QuarterlyJSON{
		PreviousPeriod: r.PreviousPeriod,
		CurrentPeriod:  r.CurrentPeriod,
		Items:          items,
	}
}

// ReviewToJSON converts a SignalSet + TrackResult to a ReviewJSON struct.
func ReviewToJSON(s *insights.SignalSet, result *insights.TrackResult) ReviewJSON {
	dims := make([]DimJSON, 0, len(result.Dimensions))
	for _, d := range result.Dimensions {
		dims = append(dims, DimJSON{
			Name:       d.Name,
			Raw:        d.Raw,
			Normalized: d.Normalized,
			Weight:     d.Weight,
			Weighted:   d.Weighted,
		})
	}
	return ReviewJSON{
		WeeklyJSON: WeeklyToJSON(s),
		Track:      result.Track,
		TrackDesc:  result.Description,
		OverallPct: math.Round(result.Overall*10000) / 100,
		Dimensions: dims,
	}
}

// --------------------------------------------------------------------------
// Writers
// --------------------------------------------------------------------------

// WriteWeeklyJSON encodes a weekly report as indented JSON to w.
func WriteWeeklyJSON(w io.Writer, s *insights.SignalSet, period string) error {
	env := ReportEnvelope{
		Type:      "weekly",
		Generated: time.Now().UTC().Format(time.RFC3339),
		Period:    period,
		Data:      WeeklyToJSON(s),
	}
	return writeJSON(w, env)
}

// WriteQuarterlyJSON encodes a quarterly delta report as indented JSON to w.
func WriteQuarterlyJSON(w io.Writer, r *insights.DeltaReport) error {
	env := ReportEnvelope{
		Type:      "quarterly",
		Generated: time.Now().UTC().Format(time.RFC3339),
		Data:      QuarterlyToJSON(r),
	}
	return writeJSON(w, env)
}

// WriteReviewJSON encodes a review report as indented JSON to w.
func WriteReviewJSON(w io.Writer, s *insights.SignalSet, result *insights.TrackResult, period string) error {
	env := ReportEnvelope{
		Type:      "review",
		Generated: time.Now().UTC().Format(time.RFC3339),
		Period:    period,
		Data:      ReviewToJSON(s, result),
	}
	return writeJSON(w, env)
}

// TrendsToJSON converts a slice of TrendPeriodData to a TrendsJSON struct.
func TrendsToJSON(periods []TrendPeriodData, periodSize string) TrendsJSON {
	n := len(periods)
	labels := make([]string, n)
	for i, p := range periods {
		labels[i] = p.Label
	}

	metrics := buildTrendMetricsJSON(periods)
	tracks := buildTrendTracksJSON(periods)

	return TrendsJSON{
		PeriodSize: formatPeriodSizeJSON(periodSize),
		Periods:    labels,
		Metrics:    metrics,
		Tracks:     tracks,
	}
}

// WriteTrendsJSON encodes a multi-period trend report as indented JSON to w.
func WriteTrendsJSON(w io.Writer, periods []TrendPeriodData, periodSize string) error {
	env := ReportEnvelope{
		Type:      "trends",
		Generated: time.Now().UTC().Format(time.RFC3339),
		Data:      TrendsToJSON(periods, periodSize),
	}
	return writeJSON(w, env)
}

func writeJSON(w io.Writer, v interface{}) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// --------------------------------------------------------------------------
// Internal helpers
// --------------------------------------------------------------------------

// countsToFocusJSON converts a map[string]int into sorted []FocusJSON.
// limit=0 means no limit; positive limit keeps only the top-N entries.
func countsToFocusJSON(counts map[string]int, limit int) []FocusJSON {
	type kv struct {
		name  string
		count int
	}
	total := 0
	pairs := make([]kv, 0, len(counts))
	for n, c := range counts {
		pairs = append(pairs, kv{n, c})
		total += c
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].name < pairs[j].name
	})
	if limit > 0 && len(pairs) > limit {
		pairs = pairs[:limit]
	}
	rows := make([]FocusJSON, 0, len(pairs))
	for _, p := range pairs {
		pct := 0.0
		if total > 0 {
			pct = math.Round(float64(p.count)/float64(total)*10000) / 100
		}
		rows = append(rows, FocusJSON{Name: p.name, Count: p.count, Pct: pct})
	}
	return rows
}

// shellActivityToJSON converts ShellActivitySignals to ShellActivityJSON.
// Returns nil when sa is nil (flag disabled or no data collected).
func shellActivityToJSON(sa *insights.ShellActivitySignals) *ShellActivityJSON {
	if sa == nil {
		return nil
	}
	infraRatioPct := 0.0
	if sa.TotalCommands > 0 {
		infraRatioPct = math.Round(float64(sa.InfraCommands)/float64(sa.TotalCommands)*10000) / 100
	}
	return &ShellActivityJSON{
		TotalCommands:  sa.TotalCommands,
		DaysActive:     sa.DaysActive,
		InfraCommands:  sa.InfraCommands,
		InfraRatioPct:  infraRatioPct,
		DeployCommands: sa.DeployCommands,
		Categories:     countsToFocusJSON(sa.CategoryCounts, 0),
		TopTools:       countsToFocusJSON(sa.ToolCounts, 10),
	}
}

// aiActivityToJSON converts AIActivitySignals to AIActivityJSON.
// Returns nil when aa is nil (flag disabled or no data collected).
func aiActivityToJSON(aa *insights.AIActivitySignals) *AIActivityJSON {
	if aa == nil {
		return nil
	}
	return &AIActivityJSON{
		TotalSessions:  aa.TotalSessions,
		TotalMessages:  aa.TotalMessages,
		TotalToolCalls: aa.TotalToolCalls,
		TotalTokens:    aa.TotalTokens,
		DaysActive:     aa.DaysActive,
		HumanCommands:  aa.HumanCommands,
		AgentCommands:  aa.AgentCommands,
		AgentProjects:  focusToJSON(aa.AgentProjects),
	}
}

func themesToJSON(counts map[insights.Theme]int, total int) []ThemeJSON {
	type kv struct {
		theme insights.Theme
		count int
	}
	pairs := make([]kv, 0, len(counts))
	for t, c := range counts {
		pairs = append(pairs, kv{t, c})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].theme < pairs[j].theme
	})
	rows := make([]ThemeJSON, 0, len(pairs))
	for _, p := range pairs {
		pct := 0.0
		if total > 0 {
			pct = math.Round(float64(p.count)/float64(total)*10000) / 100
		}
		rows = append(rows, ThemeJSON{
			Name:  string(p.theme),
			Count: p.count,
			Pct:   pct,
		})
	}
	return rows
}

func velocityToJSON(buckets []insights.MonthBucket) []VelocityJSON {
	rows := make([]VelocityJSON, 0, len(buckets))
	for _, b := range buckets {
		rows = append(rows, VelocityJSON{
			Month:   b.Month,
			Created: b.Created,
			Closed:  b.Closed,
			Net:     b.Closed - b.Created,
		})
	}
	return rows
}

func buildTrendMetricsJSON(periods []TrendPeriodData) []TrendMetricJSON {
	n := len(periods)

	type metric struct {
		name string
		vals []int
	}
	defs := []metric{
		{name: "Jira Issues"},
		{name: "Confluence Articles"},
		{name: "GitHub Activities"},
		{name: "PR Reviews"},
		{name: "Issue Comments"},
		{name: "Issues Closed"},
	}
	for i := range defs {
		defs[i].vals = make([]int, n)
	}
	for i, p := range periods {
		if p.Signals == nil {
			continue
		}
		defs[0].vals[i] = p.Signals.TotalIssues
		defs[1].vals[i] = p.Signals.TotalArticles
		defs[2].vals[i] = p.Signals.TotalActivities
		defs[3].vals[i] = p.Signals.Collaboration.PRReviews
		defs[4].vals[i] = p.Signals.Collaboration.IssueComments
		defs[5].vals[i] = p.Signals.Ownership.TotalClosed
	}

	rows := make([]TrendMetricJSON, len(defs))
	for i, d := range defs {
		rows[i] = TrendMetricJSON{
			Name:   d.name,
			Values: d.vals,
			Trend:  trendDirection(intsToFloats(d.vals)),
		}
	}
	return rows
}

func buildTrendTracksJSON(periods []TrendPeriodData) []TrendTrackJSON {
	if len(periods) == 0 {
		return nil
	}

	// Multi-track (--all-tracks).
	if len(periods[0].TrackResults) > 0 {
		return buildMultiTracksJSON(periods)
	}

	// Single-track (--track).
	if periods[0].TrackResult == nil {
		return nil
	}
	track := periods[0].TrackResult.Track
	scores := make([]*float64, len(periods))
	vals := make([]float64, len(periods))
	for i, p := range periods {
		if p.TrackResult != nil {
			v := math.Round(p.TrackResult.Overall*10000) / 100
			scores[i] = &v
			vals[i] = p.TrackResult.Overall
		}
	}
	return []TrendTrackJSON{{Name: track, Scores: scores, Trend: trendDirection(vals)}}
}

func buildMultiTracksJSON(periods []TrendPeriodData) []TrendTrackJSON {
	// Collect unique track names in sorted order from the first period.
	var trackNames []string
	seen := make(map[string]bool)
	for _, r := range periods[0].TrackResults {
		if !seen[r.Track] {
			seen[r.Track] = true
			trackNames = append(trackNames, r.Track)
		}
	}
	sort.Strings(trackNames)

	rows := make([]TrendTrackJSON, 0, len(trackNames))
	for _, name := range trackNames {
		scores := make([]*float64, len(periods))
		vals := make([]float64, len(periods))
		for i, p := range periods {
			found := false
			for _, r := range p.TrackResults {
				if r.Track == name {
					v := math.Round(r.Overall*10000) / 100
					scores[i] = &v
					vals[i] = r.Overall
					found = true
					break
				}
			}
			if !found {
				scores[i] = nil
			}
		}
		rows = append(rows, TrendTrackJSON{Name: name, Scores: scores, Trend: trendDirection(vals)})
	}
	return rows
}

func trendDirection(vals []float64) string {
	if len(vals) < 2 {
		return "flat"
	}
	first, last := vals[0], vals[len(vals)-1]
	switch {
	case last > first:
		return "up"
	case last < first:
		return "down"
	default:
		return "flat"
	}
}

func intsToFloats(vals []int) []float64 {
	out := make([]float64, len(vals))
	for i, v := range vals {
		out[i] = float64(v)
	}
	return out
}

func formatPeriodSizeJSON(size string) string {
	if len(size) < 2 {
		return size
	}
	numStr := size[:len(size)-1]
	unit := size[len(size)-1]
	var n int
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil {
		return size
	}
	switch unit {
	case 'd':
		if n == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", n)
	case 'm':
		if n == 1 {
			return "1 month"
		}
		return fmt.Sprintf("%d months", n)
	case 'y':
		if n == 1 {
			return "1 year"
		}
		return fmt.Sprintf("%d years", n)
	default:
		return size
	}
}

func focusToJSON(items []insights.FocusItem) []FocusJSON {
	total := 0
	for _, item := range items {
		total += item.Count
	}
	rows := make([]FocusJSON, 0, len(items))
	for _, item := range items {
		pct := 0.0
		if total > 0 {
			pct = math.Round(float64(item.Count)/float64(total)*10000) / 100
		}
		rows = append(rows, FocusJSON{
			Name:  item.Name,
			Count: item.Count,
			Pct:   pct,
		})
	}
	return rows
}
