package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// ReportConfig holds flags for the report command.
type ReportConfig struct {
	Format      string // "md" or "json"
	SinceDays   int
	AgentFilter string
	EventsDir   string
	AgentsFile  string
	RecsFile    string
	Timeout     time.Duration
}

// AgentSummary aggregates stats for one agent type.
type AgentSummary struct {
	AgentType       string  `json:"agent_type"`
	TotalSessions   int     `json:"total_sessions"`
	HumanSessions   int     `json:"human_sessions"`
	AgenticSessions int     `json:"agentic_sessions"`
	TotalCostUSD    float64 `json:"total_cost_usd"`
	TopModel        string  `json:"top_model,omitempty"`
}

// AntiPatternEntry is one row from recommendations.jsonl.
type AntiPatternEntry struct {
	Pattern     string `json:"pattern"`
	Occurrences int    `json:"occurrences"`
	Trend       string `json:"trend"`
	Severity    string `json:"severity"`
	SignalID    string `json:"signal_id"`
}

// GradCandidate is a graduation or regression candidate event from the event bus.
type GradCandidate struct {
	AgentType string `json:"agent_type"`
	Pattern   string `json:"pattern"`
	EventType string `json:"event_type"` // "graduation_candidate" or "regression_candidate"
}

// ReportData is the full report payload.
type ReportData struct {
	WindowDays   int                `json:"window_days"`
	GeneratedAt  string             `json:"generated_at"`
	Agents       []AgentSummary     `json:"agents"`
	AntiPatterns []AntiPatternEntry `json:"anti_patterns,omitempty"`
	Graduation   []GradCandidate    `json:"graduation,omitempty"`
	Regression   []GradCandidate    `json:"regression,omitempty"`
}

// rawEvent holds just the fields castex report cares about.
type rawEvent struct {
	AgentTool   string  `json:"agent_tool"`
	AgentType   string  `json:"agent_type"`
	SessionType string  `json:"session_type"`
	CostUSD     float64 `json:"cost_usd"`
	Model       string  `json:"model"`
	EventType   string  `json:"event_type"`
	Pattern     string  `json:"pattern"`
	Timestamp   string  `json:"timestamp"`
}

// rawRecommendation is one line from recommendations.jsonl.
type rawRecommendation struct {
	SignalID     string `json:"signal_id"`
	Pattern      string `json:"pattern"`
	Occurrences  int    `json:"occurrences"`
	Severity     string `json:"severity"`
	CurrentValue int    `json:"current_value"`
	Baseline     int    `json:"baseline"`
}

func newReportCmd() *cobra.Command {
	var cfg ReportConfig

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Produce a unified signal report across all instrumented agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.Timeout = 5 * time.Second
			return RunReport(cmd, cfg)
		},
	}

	home, _ := os.UserHomeDir()
	cmd.Flags().StringVar(&cfg.Format, "format", "md", "output format: md or json")
	cmd.Flags().IntVar(&cfg.SinceDays, "since", 30, "include events from the last N days")
	cmd.Flags().StringVar(&cfg.AgentFilter, "agent", "", "filter to a single agent type")
	cmd.Flags().StringVar(&cfg.EventsDir, "events-dir", filepath.Join(home, ".automation-metrics", "events"), "directory containing *.jsonl event files")
	cmd.Flags().StringVar(&cfg.AgentsFile, "agents-file", filepath.Join(home, ".castex", "agents.jsonl"), "path to agents.jsonl registry")
	cmd.Flags().StringVar(&cfg.RecsFile, "recs-file", filepath.Join(home, ".automation-metrics", "recommendations.jsonl"), "path to recommendations.jsonl")
	return cmd
}

// RunReport is the testable entry point for the report command.
func RunReport(cmd *cobra.Command, cfg ReportConfig) error {
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	if _, err := os.Stat(cfg.EventsDir); os.IsNotExist(err) {
		return fmt.Errorf("[E201] events_dir_missing: %s", cfg.EventsDir)
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -cfg.SinceDays)

	type result struct {
		events []rawEvent
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		events, err := readEvents(cfg.EventsDir, cutoff, cfg.AgentFilter)
		ch <- result{events, err}
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("[E203] report_timeout: read exceeded %s deadline; try --since to narrow the window", cfg.Timeout)
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		return renderReport(cmd, cfg, r.events)
	}
}

func readEvents(eventsDir string, cutoff time.Time, agentFilter string) ([]rawEvent, error) {
	files, err := filepath.Glob(filepath.Join(eventsDir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var events []rawEvent
	for _, f := range files {
		evs, err := scanEventFile(f, cutoff, agentFilter)
		if err != nil {
			continue // non-fatal: E202 schema mismatch - skip file
		}
		events = append(events, evs...)
	}
	return events, nil
}

func scanEventFile(path string, cutoff time.Time, agentFilter string) ([]rawEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []rawEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev rawEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // E202: skip malformed lines
		}
		if ev.Timestamp != "" {
			ts, err := time.Parse("20060102T150405Z", ev.Timestamp)
			if err == nil && ts.Before(cutoff) {
				continue
			}
		}
		agentKey := ev.AgentType
		if agentKey == "" {
			agentKey = ev.AgentTool
		}
		if agentFilter != "" && agentKey != agentFilter {
			continue
		}
		events = append(events, ev)
	}
	return events, scanner.Err()
}

func renderReport(cmd *cobra.Command, cfg ReportConfig, events []rawEvent) error {
	// Build per-agent summaries.
	summaryMap := map[string]*AgentSummary{}
	modelCount := map[string]map[string]int{}

	for _, ev := range events {
		agentKey := ev.AgentType
		if agentKey == "" {
			agentKey = ev.AgentTool
		}
		if agentKey == "" {
			continue
		}
		s, ok := summaryMap[agentKey]
		if !ok {
			s = &AgentSummary{AgentType: agentKey}
			summaryMap[agentKey] = s
			modelCount[agentKey] = map[string]int{}
		}
		// Only count session events toward session totals.
		isSession := ev.EventType == "" || ev.EventType == "session_event"
		if isSession {
			s.TotalSessions++
			switch ev.SessionType {
			case "human":
				s.HumanSessions++
			case "agentic", "":
				s.AgenticSessions++
			}
		}
		s.TotalCostUSD += ev.CostUSD
		if ev.Model != "" {
			modelCount[agentKey][ev.Model]++
		}
	}

	// Resolve top model per agent.
	agents := make([]AgentSummary, 0, len(summaryMap))
	for id, s := range summaryMap {
		if mc := modelCount[id]; len(mc) > 0 {
			top, topN := "", 0
			for m, n := range mc {
				if n > topN {
					top, topN = m, n
				}
			}
			s.TopModel = top
		}
		agents = append(agents, *s)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].AgentType < agents[j].AgentType })

	// Read anti-patterns (non-fatal if missing).
	var antiPatterns []AntiPatternEntry
	if recs, err := readRecommendations(cfg.RecsFile); err == nil {
		antiPatterns = recs
	}

	// Collect graduation/regression candidates from event bus.
	var graduation, regression []GradCandidate
	for _, ev := range events {
		switch ev.EventType {
		case "graduation_candidate":
			graduation = append(graduation, GradCandidate{AgentType: ev.AgentTool, Pattern: ev.Pattern, EventType: ev.EventType})
		case "regression_candidate":
			regression = append(regression, GradCandidate{AgentType: ev.AgentTool, Pattern: ev.Pattern, EventType: ev.EventType})
		}
	}

	data := ReportData{
		WindowDays:   cfg.SinceDays,
		GeneratedAt:  time.Now().UTC().Format("20060102T150405Z"),
		Agents:       agents,
		AntiPatterns: antiPatterns,
		Graduation:   graduation,
		Regression:   regression,
	}

	if cfg.Format == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(data)
	}

	return renderMarkdown(cmd, data)
}

func renderMarkdown(cmd *cobra.Command, data ReportData) error {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "# castex report\n\n")
	fmt.Fprintf(w, "Generated: %s | Window: last %d days\n\n", data.GeneratedAt, data.WindowDays)

	if len(data.Agents) == 0 {
		fmt.Fprintln(w, "No data in selected window.")
		return nil
	}

	fmt.Fprintf(w, "## Summary\n\n")
	fmt.Fprintf(w, "| Agent | Total | Human | Agentic | Cost USD |\n")
	fmt.Fprintf(w, "|-------|-------|-------|---------|----------|\n")
	var totalSessions int
	var totalCost float64
	for _, s := range data.Agents {
		fmt.Fprintf(w, "| %s | %d | %d | %d | $%.4f |\n",
			s.AgentType, s.TotalSessions, s.HumanSessions, s.AgenticSessions, s.TotalCostUSD)
		totalSessions += s.TotalSessions
		totalCost += s.TotalCostUSD
	}
	fmt.Fprintf(w, "| **Total** | **%d** | - | - | **$%.4f** |\n\n", totalSessions, totalCost)

	if len(data.AntiPatterns) > 0 {
		fmt.Fprintf(w, "## Anti-Pattern Trends\n\n")
		fmt.Fprintf(w, "| Pattern | Occurrences | Severity | Trend |\n")
		fmt.Fprintf(w, "|---------|-------------|----------|-------|\n")
		for _, ap := range data.AntiPatterns {
			fmt.Fprintf(w, "| %s | %d | %s | %s |\n", ap.Pattern, ap.Occurrences, ap.Severity, ap.Trend)
		}
		fmt.Fprintln(w)
	}

	if len(data.Graduation) > 0 {
		fmt.Fprintf(w, "## Graduation Candidates\n\n")
		for _, g := range data.Graduation {
			fmt.Fprintf(w, "- %s: %s\n", g.AgentType, g.Pattern)
		}
		fmt.Fprintln(w)
	}

	if len(data.Regression) > 0 {
		fmt.Fprintf(w, "## Regression Candidates\n\n")
		for _, r := range data.Regression {
			fmt.Fprintf(w, "- %s: %s\n", r.AgentType, r.Pattern)
		}
		fmt.Fprintln(w)
	}

	return nil
}

func readRecommendations(path string) ([]AntiPatternEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []AntiPatternEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec rawRecommendation
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		trend := "stable"
		if rec.CurrentValue > rec.Baseline {
			trend = "up"
		} else if rec.CurrentValue < rec.Baseline {
			trend = "down"
		}
		name := rec.Pattern
		if name == "" {
			name = rec.SignalID
		}
		entries = append(entries, AntiPatternEntry{
			Pattern:     name,
			Occurrences: rec.Occurrences,
			Trend:       trend,
			Severity:    rec.Severity,
			SignalID:    rec.SignalID,
		})
	}
	return entries, scanner.Err()
}

// staleAgentsWarning checks agents.jsonl against the provided set of known agent IDs
// and writes an E204 warning for any listed but absent agents.
func staleAgentsWarning(cmd *cobra.Command, agentsFile string, knownIDs map[string]bool) {
	f, err := os.Open(agentsFile)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec AgentRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if !knownIDs[rec.AgentID] {
			fmt.Fprintf(cmd.ErrOrStderr(), "[W204] agents_registry_stale: agent %q in registry but not found in current events; re-run castex init\n", rec.AgentID)
		}
	}
}

// dedupGradCandidates removes duplicate entries from a slice of GradCandidate.
func dedupGradCandidates(in []GradCandidate) []GradCandidate {
	seen := map[string]bool{}
	var out []GradCandidate
	for _, g := range in {
		key := g.AgentType + ":" + g.Pattern + ":" + g.EventType
		if !seen[key] {
			seen[key] = true
			out = append(out, g)
		}
	}
	return out
}

// totalCost returns the sum of cost_usd across all agent summaries.
func totalCost(agents []AgentSummary) float64 {
	var sum float64
	for _, a := range agents {
		sum += a.TotalCostUSD
	}
	return sum
}

// containsField checks if a JSON-encoded string contains a given key.
func containsField(s, key string) bool {
	return strings.Contains(s, `"`+key+`"`)
}
