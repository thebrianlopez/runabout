package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/api"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/insights"
)

func eventsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Summarize local automation-metrics events (no credentials required)",
		Long: `Summarize events from ~/.automation-metrics/events/ (primary) or
~/Downloads/terminal-history/ (fallback). No API credentials required.

Output includes:
  - Event counts by layer (interactive_shell, fish, claude_code, cloud_llm, go_cli)
  - Session count, avg duration, and longest session
  - Total estimated cost from inference events
  - Graduation candidates and topology signals
  - Tool distribution breakdown`,
		RunE: runEvents,
	}

	cmd.Flags().String("start", "", "Start date (YYYY-MM-DD, default: 7 days ago)")
	cmd.Flags().String("end", "", "End date (YYYY-MM-DD, default: today)")
	cmd.Flags().String("format", "md", "Output format: md|json")

	return cmd
}

func runEvents(cmd *cobra.Command, args []string) error {
	startFlag, _ := cmd.Flags().GetString("start")
	endFlag, _ := cmd.Flags().GetString("end")
	format, _ := cmd.Flags().GetString("format")

	// Resolve date range — default to last 7 days.
	startDate, endDate, err := resolveEventsDates(startFlag, endFlag)
	if err != nil {
		return err
	}

	client := api.NewAuditLogClient()

	events, err := client.GetEvents(startDate, endDate)
	if err != nil {
		return fmt.Errorf("reading events: %w", err)
	}

	summaries, err := client.GetSessionSummaries(startDate, endDate)
	if err != nil {
		return fmt.Errorf("reading session summaries: %w", err)
	}

	aiSignals := insights.ExtractAISignals(nil, events, summaries)
	sessionSignals := insights.AnalyzeSessions(summaries)
	topoSignals := insights.AnalyzeTopology(summaries)

	switch format {
	case "json":
		return printEventsJSON(startDate, endDate, len(events), aiSignals, sessionSignals, topoSignals)
	default:
		printEventsMD(startDate, endDate, len(events), aiSignals, sessionSignals, topoSignals)
		return nil
	}
}

func resolveEventsDates(startFlag, endFlag string) (string, string, error) {
	now := time.Now()
	end := now
	if endFlag != "" {
		t, err := time.Parse("2006-01-02", endFlag)
		if err != nil {
			return "", "", fmt.Errorf("invalid end date %q: %w", endFlag, err)
		}
		end = t
	}

	start := end.AddDate(0, 0, -7)
	if startFlag != "" {
		t, err := time.Parse("2006-01-02", startFlag)
		if err != nil {
			return "", "", fmt.Errorf("invalid start date %q: %w", startFlag, err)
		}
		start = t
	}

	return start.Format("2006-01-02"), end.Format("2006-01-02"), nil
}

func printEventsMD(startDate, endDate string, totalEvents int, ai *insights.AIActivitySignals, sess *insights.SessionSignals, topo *insights.TopologySignals) {
	fmt.Printf("# Events Summary — %s → %s\n\n", startDate, endDate)

	fmt.Printf("**Total events:** %d  \n", totalEvents)
	fmt.Printf("**Sessions:** %d  \n", sess.TotalSessions)
	if sess.TotalSessions > 0 {
		fmt.Printf("**Avg session duration:** %.1f min  \n", sess.AvgDurationMin)
		fmt.Printf("**Longest session:** %.1f min  \n", sess.LongestSessionMin)
	}
	if ai.TotalCostUSD > 0 {
		fmt.Printf("**Total estimated cost:** $%.4f  \n", ai.TotalCostUSD)
	}
	if ai.GraduationCandidates > 0 {
		fmt.Printf("**Graduation candidates:** %d  \n", ai.GraduationCandidates)
	}
	fmt.Println()

	// Layer breakdown
	if len(ai.LayerBreakdown) > 0 {
		fmt.Println("## Layer Breakdown")
		fmt.Println("| Layer | Events |")
		fmt.Println("|-------|--------|")
		for _, layer := range sortedKeys(ai.LayerBreakdown) {
			fmt.Printf("| `%s` | %d |\n", layer, ai.LayerBreakdown[layer])
		}
		fmt.Println()
	}

	// Tool distribution
	if len(ai.ToolDistribution) > 0 {
		fmt.Println("## Tool Distribution")
		fmt.Println("| Tool | Calls |")
		fmt.Println("|------|-------|")
		type kv struct {
			k string
			v int
		}
		var pairs []kv
		for k, v := range ai.ToolDistribution {
			pairs = append(pairs, kv{k, v})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].v > pairs[j].v })
		for _, p := range pairs {
			fmt.Printf("| `%s` | %d |\n", p.k, p.v)
		}
		fmt.Println()
	}

	// Session signals
	if sess.TotalSessions > 0 {
		fmt.Println("## Session Signals")
		fmt.Printf("- **Unique sessions:** %d\n", sess.TotalSessions)
		if sess.MultiProjectCount > 0 {
			fmt.Printf("- **Multi-project sessions:** %d\n", sess.MultiProjectCount)
		}
		fmt.Printf("- **Avg events/session:** %.1f\n", sess.AvgEventsPerSession)
		fmt.Printf("- **Avg tools/session:** %.1f\n", sess.AvgToolsPerSession)
		if len(sess.ProjectSessions) > 0 {
			top := sess.ProjectSessions
			if len(top) > 5 {
				top = top[:5]
			}
			var parts []string
			for _, p := range top {
				parts = append(parts, fmt.Sprintf("%s (%d)", p.Name, p.Count))
			}
			fmt.Printf("- **Top projects:** %s\n", strings.Join(parts, ", "))
		}
		fmt.Println()
	}

	// Topology
	if topo.GraduationDensity > 0 || topo.AntiPatternRate > 0 {
		fmt.Println("## Topology Signals")
		fmt.Printf("- **Graduation density:** %.3f (graduation candidates / total events)\n", topo.GraduationDensity)
		fmt.Printf("- **Anti-pattern rate:** %.3f (inference-only sessions / total sessions)\n", topo.AntiPatternRate)
		fmt.Printf("- **Tool sessions:** %d | **Inference-only sessions:** %d\n", topo.ToolSessions, topo.InferenceSessions)
		fmt.Println()
	}
}

type eventsJSONOutput struct {
	Period      string                      `json:"period"`
	StartDate   string                      `json:"start_date"`
	EndDate     string                      `json:"end_date"`
	TotalEvents int                         `json:"total_events"`
	AIActivity  *insights.AIActivitySignals `json:"ai_activity"`
	Sessions    *insights.SessionSignals    `json:"sessions"`
	Topology    *insights.TopologySignals   `json:"topology"`
}

func printEventsJSON(startDate, endDate string, totalEvents int, ai *insights.AIActivitySignals, sess *insights.SessionSignals, topo *insights.TopologySignals) error {
	out := eventsJSONOutput{
		Period:      fmt.Sprintf("%s → %s", startDate, endDate),
		StartDate:   startDate,
		EndDate:     endDate,
		TotalEvents: totalEvents,
		AIActivity:  ai,
		Sessions:    sess,
		Topology:    topo,
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// sortedKeys returns map keys sorted descending by value, then ascending by name.
func sortedKeys(m map[string]int) []string {
	type kv struct {
		k string
		v int
	}
	var pairs []kv
	for k, v := range m {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	keys := make([]string, len(pairs))
	for i, p := range pairs {
		keys[i] = p.k
	}
	return keys
}
