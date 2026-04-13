// Package templates provides embedded Go text/template renderers for workctl
// report output. Each workflow command (weekly, quarterly, review) has a
// corresponding .tmpl file embedded into the binary at compile time.
//
// Callers pass pre-built view models (WeeklyData, QuarterlyData, ReviewData)
// to the Render* functions. The view models are constructed from
// insights.SignalSet and insights.TrackResult by the New*Data factory
// functions in this package.
package templates

import (
	_ "embed"
	"fmt"
	"io"
	"math"
	"sort"
	"text/template"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/insights"
)

//go:embed trends.tmpl
var trendsTmpl string

var parsedTrends *template.Template

// --------------------------------------------------------------------------
// Embedded template files
// --------------------------------------------------------------------------

//go:embed weekly.tmpl
var weeklyTmpl string

//go:embed quarterly.tmpl
var quarterlyTmpl string

//go:embed review.tmpl
var reviewTmpl string

// parsed holds the compiled templates, initialised once in init().
var (
	parsedWeekly    *template.Template
	parsedQuarterly *template.Template
	parsedReview    *template.Template
)

func init() {
	parsedWeekly = template.Must(template.New("weekly").Parse(weeklyTmpl))
	parsedQuarterly = template.Must(template.New("quarterly").Parse(quarterlyTmpl))
	parsedReview = template.Must(template.New("review").Parse(reviewTmpl))
	parsedTrends = template.Must(template.New("trends").Parse(trendsTmpl))
}

// --------------------------------------------------------------------------
// View model types
// --------------------------------------------------------------------------

// ThemeRow is one row in the Work Themes table.
type ThemeRow struct {
	Name  string
	Count int
	Pct   string
}

// VelocityRow is one row in the Monthly Velocity table.
type VelocityRow struct {
	Month   string
	Created int
	Closed  int
	Net     string
}

// FocusRow is one row in a Focus Distribution table.
type FocusRow struct {
	Name  string
	Count int
	Pct   string
}

// CollabData holds formatted collaboration signals.
type CollabData struct {
	PRReviews       int
	IssueComments   int
	UniqueRepos     int
	CrossTeamIssues int
	HasContent      bool
}

// OwnershipData holds formatted ownership signals.
type OwnershipData struct {
	IssuesClosed       int
	HighPriorityClosed int
	IncidentRatioPct   string
	HasContent         bool
}

// ShellActivityData is the view model for the Shell Activity section.
// It is nil when --shell=false or when no shell data was collected.
type ShellActivityData struct {
	TotalCommands  int
	DaysActive     int
	InfraCommands  int
	InfraRatioPct  string
	DeployCommands int
	PeakHour       string // e.g. "10am" — empty if no data

	Categories []FocusRow // command category breakdown, sorted by count
	TopTools   []FocusRow // top-10 tools by frequency
	HasContent bool
}

// AIActivityData is the view model for the AI-Assisted Work section.
// It is nil when --ai-stats=false or when no AI data was collected.
type AIActivityData struct {
	TotalSessions  int
	TotalMessages  int
	TotalToolCalls int
	TotalTokens    int
	DaysActive     int
	HumanCommands  int
	AgentCommands  int

	AgentProjects []FocusRow // top project dirs by agent command count
	HasContent    bool

	// EPIC-019 M1+M2: events-native signals
	EventSessions         int
	AvgSessionDurationMin string // pre-formatted (e.g. "8.2")
	TotalCostUSD          string // pre-formatted (e.g. "$0.43")
	GraduationCandidates  int
	ToolDistribution      []FocusRow // aggregated tool usage across sessions
	HasEventSignals       bool       // true when EventSessions > 0

	// EPIC-019 M3: layer-aware breakdown
	LayerBreakdown []FocusRow // source/layer → count (sorted by count)
}

// SessionData is the view model for the Session Analysis section (EPIC-020).
type SessionData struct {
	TotalSessions       int
	MultiProjectCount   int
	AvgEventsPerSession string
	AvgToolsPerSession  string
	AvgDurationMin      string
	LongestSessionMin   string
	ProjectSessions     []FocusRow
	HasContent          bool
}

// TopologyData is the view model for the Topology Signals section (EPIC-020).
type TopologyData struct {
	GraduationDensityPct string
	InferenceSessions    int
	ToolSessions         int
	AntiPatternRatePct   string
	HasContent           bool
}

// WeeklyData is the view model for weekly.tmpl.
type WeeklyData struct {
	Generated string
	Period    string

	TotalIssues     int
	TotalArticles   int
	TotalActivities int

	Themes   []ThemeRow
	Velocity []VelocityRow
	Projects []FocusRow
	Spaces   []FocusRow
	Repos    []FocusRow

	Collab    CollabData
	Ownership OwnershipData

	// Local activity sections (nil when disabled via --shell / --ai-stats).
	ShellActivity *ShellActivityData
	AIActivity    *AIActivityData

	// EPIC-020: session and topology signals (nil when no session data).
	Sessions *SessionData
	Topology *TopologyData
}

// DeltaRow is one row in the Growth Delta table.
type DeltaRow struct {
	Metric   string
	Previous string
	Current  string
	Delta    string
	PctDelta string
	Trend    string
}

// QuarterlyData is the view model for quarterly.tmpl.
type QuarterlyData struct {
	Generated      string
	PreviousPeriod string
	CurrentPeriod  string
	Items          []DeltaRow
}

// DimRow is one row in the Dimension Scores table.
type DimRow struct {
	Name       string
	Raw        string
	Normalized string
	Weight     string
	Weighted   string
	ContribPct string
	CeilingPct string
}

// ReviewData is the view model for review.tmpl.
// It embeds WeeklyData so the insights section reuses the same fields.
type ReviewData struct {
	WeeklyData

	Track      string
	TrackDesc  string
	OverallPct string

	Dimensions  []DimRow
	Strengths   []DimRow
	GrowthAreas []DimRow
}

// TrendPeriod holds one period's data for trend analysis. It uses only types
// from internal/ packages, keeping the templates package independent of cmd.
type TrendPeriod struct {
	Label        string
	Signals      *insights.SignalSet
	TrackResult  *insights.TrackResult   // single track (--track)
	TrackResults []*insights.TrackResult // all tracks (--all-tracks)
}

// TrendMetricRow is one signal metric row in the trend table.
type TrendMetricRow struct {
	Name   string   // "Jira Issues", "PR Reviews", …
	Values []string // pre-formatted, one per period (oldest first)
	Trend  string   // "↑", "↓", "="
}

// TrendTrackRow is one career-track row in the track-score table.
type TrendTrackRow struct {
	Track  string   // track name
	Scores []string // pre-formatted pct strings, one per period (oldest first)
	Trend  string   // "↑", "↓", "="
}

// TrendData is the view model for trends.tmpl.
type TrendData struct {
	Generated  string
	PeriodSize string // "3 months", "1 month", "7 days" …
	Periods    []string
	Metrics    []TrendMetricRow
	TrackRows  []TrendTrackRow // empty unless TrackResult is set on periods
}

// --------------------------------------------------------------------------
// Factory functions — convert insight structs to view models
// --------------------------------------------------------------------------

// NewWeeklyData builds a WeeklyData from a SignalSet.
func NewWeeklyData(s *insights.SignalSet, period string) *WeeklyData {
	d := &WeeklyData{
		Generated:       time.Now().Format("2006-01-02 15:04"),
		Period:          period,
		TotalIssues:     s.TotalIssues,
		TotalArticles:   s.TotalArticles,
		TotalActivities: s.TotalActivities,
		Themes:          buildThemeRows(s.ThemeCounts, s.TotalIssues),
		Velocity:        buildVelocityRows(s.Velocity),
		Projects:        buildFocusRows(s.ProjectFocus),
		Spaces:          buildFocusRows(s.SpaceFocus),
		Repos:           buildFocusRows(s.RepoFocus),
		Collab:          buildCollabData(s.Collaboration),
		Ownership:       buildOwnershipData(s.Ownership),
		ShellActivity:   buildShellActivityData(s.ShellActivity),
		AIActivity:      buildAIActivityData(s.AIActivity),
		Sessions:        buildSessionData(s.SessionSignals),
		Topology:        buildTopologyData(s.TopologySignals),
	}
	return d
}

// NewQuarterlyData builds a QuarterlyData from a DeltaReport.
func NewQuarterlyData(r *insights.DeltaReport) *QuarterlyData {
	rows := make([]DeltaRow, 0, len(r.Items))
	for _, item := range r.Items {
		rows = append(rows, DeltaRow{
			Metric:   item.Metric,
			Previous: fmt.Sprintf("%.0f", item.Previous),
			Current:  fmt.Sprintf("%.0f", item.Current),
			Delta:    formatSigned(item.Delta),
			PctDelta: formatPctDelta(item.PctDelta),
			Trend:    trendIcon(item.Delta),
		})
	}
	return &QuarterlyData{
		Generated:      time.Now().Format("2006-01-02 15:04"),
		PreviousPeriod: r.PreviousPeriod,
		CurrentPeriod:  r.CurrentPeriod,
		Items:          rows,
	}
}

// NewReviewData builds a ReviewData from a SignalSet and TrackResult.
func NewReviewData(s *insights.SignalSet, result *insights.TrackResult, period string) *ReviewData {
	weekly := NewWeeklyData(s, period)
	dims := buildDimRows(result)
	strengths, growthAreas := buildStrengthsAndGrowth(result)
	return &ReviewData{
		WeeklyData:  *weekly,
		Track:       result.Track,
		TrackDesc:   result.Description,
		OverallPct:  fmt.Sprintf("%.1f%%", result.Overall*100),
		Dimensions:  dims,
		Strengths:   strengths,
		GrowthAreas: growthAreas,
	}
}

// NewTrendData builds a TrendData from a slice of TrendPeriod values and the
// period-size string (e.g. "3m").
func NewTrendData(periods []TrendPeriod, periodSize string) *TrendData {
	labels := make([]string, len(periods))
	for i, p := range periods {
		labels[i] = p.Label
	}
	return &TrendData{
		Generated:  time.Now().Format("2006-01-02 15:04"),
		PeriodSize: formatPeriodSize(periodSize),
		Periods:    labels,
		Metrics:    buildTrendMetricRows(periods),
		TrackRows:  buildTrendTrackRows(periods),
	}
}

// --------------------------------------------------------------------------
// Render functions
// --------------------------------------------------------------------------

// RenderWeekly writes a weekly Markdown report from a SignalSet.
func RenderWeekly(w io.Writer, s *insights.SignalSet, period string) error {
	return RenderWeeklyData(w, NewWeeklyData(s, period))
}

// RenderWeeklyData writes a weekly report from a pre-built WeeklyData view model.
// Use this variant in tests to control the Generated timestamp.
func RenderWeeklyData(w io.Writer, d *WeeklyData) error {
	return parsedWeekly.Execute(w, d)
}

// RenderQuarterly writes a quarterly delta Markdown report from a DeltaReport.
func RenderQuarterly(w io.Writer, r *insights.DeltaReport) error {
	return RenderQuarterlyData(w, NewQuarterlyData(r))
}

// RenderQuarterlyData writes a quarterly report from a pre-built QuarterlyData view model.
func RenderQuarterlyData(w io.Writer, d *QuarterlyData) error {
	return parsedQuarterly.Execute(w, d)
}

// RenderReview writes a full-year insights + career track Markdown report.
func RenderReview(w io.Writer, s *insights.SignalSet, result *insights.TrackResult, period string) error {
	return RenderReviewData(w, NewReviewData(s, result, period))
}

// RenderReviewData writes a review report from a pre-built ReviewData view model.
func RenderReviewData(w io.Writer, d *ReviewData) error {
	return parsedReview.Execute(w, d)
}

// RenderTrends writes a multi-period trend Markdown report.
func RenderTrends(w io.Writer, periods []TrendPeriod, periodSize string) error {
	return RenderTrendsData(w, NewTrendData(periods, periodSize))
}

// RenderTrendsData writes a trend report from a pre-built TrendData view model.
// Use this variant in tests to control the Generated timestamp.
func RenderTrendsData(w io.Writer, d *TrendData) error {
	return parsedTrends.Execute(w, d)
}

// --------------------------------------------------------------------------
// Internal helpers
// --------------------------------------------------------------------------

// buildToolCountRows converts a map[string]int into sorted FocusRow values.
// limit=0 means no limit; positive limit keeps only the top-N entries.
func buildToolCountRows(counts map[string]int, limit int) []FocusRow {
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
	rows := make([]FocusRow, 0, len(pairs))
	for _, p := range pairs {
		pct := ""
		if total > 0 {
			pct = fmt.Sprintf("%.0f%%", float64(p.count)/float64(total)*100)
		}
		rows = append(rows, FocusRow{Name: p.name, Count: p.count, Pct: pct})
	}
	return rows
}

// peakHourLabel returns a human-readable label for the hour with the most
// activity in a 24-element distribution (e.g. "10am", "2pm"). Returns ""
// when the distribution is all-zero.
func peakHourLabel(dist [24]int) string {
	peak, max := 0, 0
	for h, c := range dist {
		if c > max {
			max = c
			peak = h
		}
	}
	if max == 0 {
		return ""
	}
	if peak == 0 {
		return "12am"
	}
	if peak < 12 {
		return fmt.Sprintf("%dam", peak)
	}
	if peak == 12 {
		return "12pm"
	}
	return fmt.Sprintf("%dpm", peak-12)
}

// buildShellActivityData converts ShellActivitySignals to a ShellActivityData
// view model. Returns nil when sa is nil (flag disabled or no data collected).
func buildShellActivityData(sa *insights.ShellActivitySignals) *ShellActivityData {
	if sa == nil {
		return nil
	}
	infraRatioPct := ""
	if sa.TotalCommands > 0 {
		infraRatioPct = fmt.Sprintf("%.0f%%", float64(sa.InfraCommands)/float64(sa.TotalCommands)*100)
	}
	return &ShellActivityData{
		TotalCommands:  sa.TotalCommands,
		DaysActive:     sa.DaysActive,
		InfraCommands:  sa.InfraCommands,
		InfraRatioPct:  infraRatioPct,
		DeployCommands: sa.DeployCommands,
		PeakHour:       peakHourLabel(sa.HourDistribution),
		Categories:     buildToolCountRows(sa.CategoryCounts, 0),
		TopTools:       buildToolCountRows(sa.ToolCounts, 10),
		HasContent:     sa.TotalCommands > 0,
	}
}

// buildAIActivityData converts AIActivitySignals to an AIActivityData view
// model. Returns nil when aa is nil (flag disabled or no data collected).
func buildAIActivityData(aa *insights.AIActivitySignals) *AIActivityData {
	if aa == nil {
		return nil
	}
	d := &AIActivityData{
		TotalSessions:  aa.TotalSessions,
		TotalMessages:  aa.TotalMessages,
		TotalToolCalls: aa.TotalToolCalls,
		TotalTokens:    aa.TotalTokens,
		DaysActive:     aa.DaysActive,
		HumanCommands:  aa.HumanCommands,
		AgentCommands:  aa.AgentCommands,
		AgentProjects:  buildFocusRows(aa.AgentProjects),
		HasContent:     aa.TotalSessions > 0 || aa.TotalMessages > 0,
	}

	// EPIC-019 M1+M2: events-native signals
	if aa.EventSessions > 0 {
		d.EventSessions = aa.EventSessions
		d.AvgSessionDurationMin = fmt.Sprintf("%.1f", aa.AvgSessionDurationMin)
		d.TotalCostUSD = fmt.Sprintf("$%.2f", aa.TotalCostUSD)
		d.GraduationCandidates = aa.GraduationCandidates
		d.ToolDistribution = buildToolCountRows(aa.ToolDistribution, 10)
		d.HasEventSignals = true
		d.HasContent = true
	}

	// EPIC-019 M3: layer-aware breakdown
	if len(aa.LayerBreakdown) > 0 {
		d.LayerBreakdown = buildToolCountRows(aa.LayerBreakdown, 0)
	}

	return d
}

// buildSessionData converts SessionSignals to a SessionData view model.
func buildSessionData(ss *insights.SessionSignals) *SessionData {
	if ss == nil || ss.TotalSessions == 0 {
		return nil
	}
	return &SessionData{
		TotalSessions:       ss.TotalSessions,
		MultiProjectCount:   ss.MultiProjectCount,
		AvgEventsPerSession: fmt.Sprintf("%.1f", ss.AvgEventsPerSession),
		AvgToolsPerSession:  fmt.Sprintf("%.1f", ss.AvgToolsPerSession),
		AvgDurationMin:      fmt.Sprintf("%.1f", ss.AvgDurationMin),
		LongestSessionMin:   fmt.Sprintf("%.1f", ss.LongestSessionMin),
		ProjectSessions:     buildFocusRows(ss.ProjectSessions),
		HasContent:          true,
	}
}

// buildTopologyData converts TopologySignals to a TopologyData view model.
func buildTopologyData(ts *insights.TopologySignals) *TopologyData {
	if ts == nil || (ts.InferenceSessions == 0 && ts.ToolSessions == 0) {
		return nil
	}
	return &TopologyData{
		GraduationDensityPct: fmt.Sprintf("%.1f%%", ts.GraduationDensity*100),
		InferenceSessions:    ts.InferenceSessions,
		ToolSessions:         ts.ToolSessions,
		AntiPatternRatePct:   fmt.Sprintf("%.0f%%", ts.AntiPatternRate*100),
		HasContent:           true,
	}
}

func buildThemeRows(counts map[insights.Theme]int, total int) []ThemeRow {
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
	rows := make([]ThemeRow, 0, len(pairs))
	for _, p := range pairs {
		pct := ""
		if total > 0 {
			pct = fmt.Sprintf("%.0f%%", float64(p.count)/float64(total)*100)
		}
		rows = append(rows, ThemeRow{
			Name:  string(p.theme),
			Count: p.count,
			Pct:   pct,
		})
	}
	return rows
}

func buildVelocityRows(buckets []insights.MonthBucket) []VelocityRow {
	rows := make([]VelocityRow, 0, len(buckets))
	for _, b := range buckets {
		net := b.Closed - b.Created
		netStr := fmt.Sprintf("%d", net)
		if net > 0 {
			netStr = "+" + netStr
		}
		rows = append(rows, VelocityRow{
			Month:   b.Month,
			Created: b.Created,
			Closed:  b.Closed,
			Net:     netStr,
		})
	}
	return rows
}

func buildFocusRows(items []insights.FocusItem) []FocusRow {
	if len(items) == 0 {
		return nil
	}
	total := 0
	for _, item := range items {
		total += item.Count
	}
	rows := make([]FocusRow, 0, len(items))
	for _, item := range items {
		pct := ""
		if total > 0 {
			pct = fmt.Sprintf("%.0f%%", float64(item.Count)/float64(total)*100)
		}
		rows = append(rows, FocusRow{
			Name:  item.Name,
			Count: item.Count,
			Pct:   pct,
		})
	}
	return rows
}

func buildCollabData(c insights.CollaborationSignals) CollabData {
	return CollabData{
		PRReviews:       c.PRReviews,
		IssueComments:   c.IssueComments,
		UniqueRepos:     c.UniqueRepos,
		CrossTeamIssues: c.CrossTeamIssues,
		HasContent:      c.PRReviews > 0 || c.IssueComments > 0 || c.UniqueRepos > 0 || c.CrossTeamIssues > 0,
	}
}

func buildOwnershipData(o insights.OwnershipSignals) OwnershipData {
	return OwnershipData{
		IssuesClosed:       o.TotalClosed,
		HighPriorityClosed: o.HighPriorityClosed,
		IncidentRatioPct:   fmt.Sprintf("%.1f%%", o.IncidentRatio*100),
		HasContent:         o.TotalClosed > 0 || o.HighPriorityClosed > 0,
	}
}

func buildDimRows(result *insights.TrackResult) []DimRow {
	rows := make([]DimRow, 0, len(result.Dimensions))
	for _, d := range result.Dimensions {
		rows = append(rows, DimRow{
			Name:       d.Name,
			Raw:        fmt.Sprintf("%.2f", d.Raw),
			Normalized: fmt.Sprintf("%.3f", d.Normalized),
			Weight:     fmt.Sprintf("%.2f", d.Weight),
			Weighted:   fmt.Sprintf("%.3f", d.Weighted),
		})
	}
	return rows
}

func buildStrengthsAndGrowth(result *insights.TrackResult) (strengths, growth []DimRow) {
	if len(result.Dimensions) == 0 {
		return nil, nil
	}

	// Strengths: top 3 by weighted score
	byWeighted := make([]insights.DimensionScore, len(result.Dimensions))
	copy(byWeighted, result.Dimensions)
	sort.Slice(byWeighted, func(i, j int) bool {
		return byWeighted[i].Weighted > byWeighted[j].Weighted
	})
	for i := 0; i < 3 && i < len(byWeighted); i++ {
		d := byWeighted[i]
		if d.Weighted <= 0 {
			break
		}
		contribPct := ""
		if result.Overall > 0 {
			contribPct = fmt.Sprintf("%.1f%%", d.Weighted/result.Overall*100)
		}
		strengths = append(strengths, DimRow{
			Name:       d.Name,
			ContribPct: contribPct,
		})
	}

	// Growth areas: bottom 3 by normalized score, with non-zero weight
	byNorm := make([]insights.DimensionScore, len(result.Dimensions))
	copy(byNorm, result.Dimensions)
	sort.Slice(byNorm, func(i, j int) bool {
		return byNorm[i].Normalized < byNorm[j].Normalized
	})
	for i := 0; i < 3 && i < len(byNorm); i++ {
		d := byNorm[i]
		if d.Weight <= 0 {
			continue
		}
		growth = append(growth, DimRow{
			Name:       d.Name,
			CeilingPct: fmt.Sprintf("%.0f%%", d.Normalized*100),
		})
	}

	return strengths, growth
}

func formatSigned(v float64) string {
	if v > 0 {
		return fmt.Sprintf("+%.0f", v)
	}
	return fmt.Sprintf("%.0f", v)
}

func formatPctDelta(v float64) string {
	if math.IsNaN(v) {
		return "—"
	}
	if v > 0 {
		return fmt.Sprintf("+%.0f%%", v)
	}
	return fmt.Sprintf("%.0f%%", v)
}

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

// trendArrow returns ↑, ↓, or = based on whether the last value is greater
// than, less than, or equal to the first value in vals.
func trendArrow(vals []float64) string {
	if len(vals) < 2 {
		return "="
	}
	first, last := vals[0], vals[len(vals)-1]
	switch {
	case last > first:
		return "↑"
	case last < first:
		return "↓"
	default:
		return "="
	}
}

// buildTrendMetricRows builds the signal overview rows for the trend table.
func buildTrendMetricRows(periods []TrendPeriod) []TrendMetricRow {
	n := len(periods)
	issueV := make([]float64, n)
	articleV := make([]float64, n)
	activityV := make([]float64, n)
	prV := make([]float64, n)
	commentV := make([]float64, n)
	closedV := make([]float64, n)

	for i, p := range periods {
		if p.Signals == nil {
			continue
		}
		issueV[i] = float64(p.Signals.TotalIssues)
		articleV[i] = float64(p.Signals.TotalArticles)
		activityV[i] = float64(p.Signals.TotalActivities)
		prV[i] = float64(p.Signals.Collaboration.PRReviews)
		commentV[i] = float64(p.Signals.Collaboration.IssueComments)
		closedV[i] = float64(p.Signals.Ownership.TotalClosed)
	}

	intRow := func(name string, vals []float64) TrendMetricRow {
		strs := make([]string, n)
		for i, v := range vals {
			strs[i] = fmt.Sprintf("%.0f", v)
		}
		return TrendMetricRow{Name: name, Values: strs, Trend: trendArrow(vals)}
	}

	return []TrendMetricRow{
		intRow("Jira Issues", issueV),
		intRow("Confluence Articles", articleV),
		intRow("GitHub Activities", activityV),
		intRow("PR Reviews", prV),
		intRow("Issue Comments", commentV),
		intRow("Issues Closed", closedV),
	}
}

// buildTrendTrackRows builds career track score rows. When periods carry
// TrackResults (--all-tracks), one row per unique track is produced. When
// only TrackResult is set (--track), a single row is produced.
func buildTrendTrackRows(periods []TrendPeriod) []TrendTrackRow {
	if len(periods) == 0 {
		return nil
	}

	// Check for multi-track (--all-tracks) first.
	if len(periods[0].TrackResults) > 0 {
		return buildMultiTrackRows(periods)
	}

	// Single-track (--track) fallback.
	if periods[0].TrackResult == nil {
		return nil
	}
	track := periods[0].TrackResult.Track
	scores := make([]string, len(periods))
	vals := make([]float64, len(periods))
	for i, p := range periods {
		if p.TrackResult != nil {
			scores[i] = fmt.Sprintf("%.0f%%", p.TrackResult.Overall*100)
			vals[i] = p.TrackResult.Overall
		} else {
			scores[i] = "—"
		}
	}
	return []TrendTrackRow{{Track: track, Scores: scores, Trend: trendArrow(vals)}}
}

// buildMultiTrackRows produces one TrendTrackRow per track across all periods.
func buildMultiTrackRows(periods []TrendPeriod) []TrendTrackRow {
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

	rows := make([]TrendTrackRow, 0, len(trackNames))
	for _, name := range trackNames {
		scores := make([]string, len(periods))
		vals := make([]float64, len(periods))
		for i, p := range periods {
			found := false
			for _, r := range p.TrackResults {
				if r.Track == name {
					scores[i] = fmt.Sprintf("%.0f%%", r.Overall*100)
					vals[i] = r.Overall
					found = true
					break
				}
			}
			if !found {
				scores[i] = "—"
			}
		}
		rows = append(rows, TrendTrackRow{Track: name, Scores: scores, Trend: trendArrow(vals)})
	}
	return rows
}

// formatPeriodSize converts a duration string (e.g. "3m") to a human label
// (e.g. "3 months").
func formatPeriodSize(size string) string {
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
