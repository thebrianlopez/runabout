package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/api"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/insights"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ui"
)

func trendsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trends",
		Short: "Analyze work patterns across N consecutive periods",
		Long: `Fetch N consecutive equal-length periods of Jira, Confluence, and GitHub
activity and produce a multi-period trend report showing how work patterns
have evolved over time.

Example:
  workctl trends --periods 4 --period-size 3m
  workctl trends --periods 6 --period-size 1m --end 2025-12-31
  workctl trends --periods 4 --period-size 3m --track staff
  workctl trends --periods 4 --period-size 3m --format json --output trends.json

Note: fetching N periods is inherently N× slower than a single query on first run.
Subsequent runs benefit from the local cache (--refresh to bypass).

For consistent cross-period GitHub data use --github-api search; the default
"auto" strategy may use mixed APIs across periods (a warning is emitted when
this occurs).`,
		RunE: runTrends,
	}

	cmd.Flags().Int("periods", 4, "Number of periods to fetch (≥ 2)")
	cmd.Flags().String("period-size", "3m", "Length of each period: e.g. 3m, 1m, 7d, 90d")
	cmd.Flags().String("end", "", "End date of the most recent period (YYYY-MM-DD, default: today)")
	cmd.Flags().String("track", "", "Career track to score per period (staff, platform, manager, or custom)")
	cmd.Flags().Bool("all-tracks", false, "Score all available tracks (builtin + custom) per period")
	registerAllFetchFlags(cmd)
	registerReportOutputFlags(cmd, "md")

	return cmd
}

func runTrends(cmd *cobra.Command, args []string) error {
	fmtFlag, _ := cmd.Flags().GetString("format")
	rf, err := parseFormat(fmtFlag)
	if err != nil {
		return err
	}

	track, _ := cmd.Flags().GetString("track")
	allTracks, _ := cmd.Flags().GetBool("all-tracks")
	if track != "" && allTracks {
		return fmt.Errorf("--track and --all-tracks are mutually exclusive")
	}

	n, _ := cmd.Flags().GetInt("periods")
	periodSize, _ := cmd.Flags().GetString("period-size")
	endOverride, _ := cmd.Flags().GetString("end")

	var end time.Time
	if endOverride != "" {
		end, err = time.Parse("2006-01-02", endOverride)
		if err != nil {
			return fmt.Errorf("invalid --end date %q: %w", endOverride, err)
		}
	} else {
		end = time.Now()
	}

	periods, err := GeneratePeriods(n, periodSize, end)
	if err != nil {
		return fmt.Errorf("generating periods: %w", err)
	}

	ui.Infof("Trends: %d × %s periods ending %s\n\n", n, periodSize, end.Format("2006-01-02"))

	// Warn if GitHub auto-strategy would produce mixed API types across periods.
	rc := *resolved
	if rc.GitHub {
		if w := mixedGitHubStrategies(periods, rc.GitHubAPIStrategy); w != "" {
			fmt.Fprintln(os.Stderr, w)
		}
	}

	ts, err := FetchTrends(cmd.Context(), &rc, periods)
	if err != nil {
		return err
	}
	ts.PeriodSize = periodSize

	// Optional: score career tracks per period.
	var ceilings map[string]float64
	if fileConfig != nil && fileConfig.CareerLens != nil {
		ceilings = fileConfig.CareerLens.Ceilings
	}
	custom := buildCustomTracks()

	if allTracks {
		for _, prd := range ts.Periods {
			results, err := insights.ScoreAllTracks(prd.Signals, ceilings, custom)
			if err != nil {
				return fmt.Errorf("scoring all tracks for period %s: %w", prd.Period, err)
			}
			prd.TrackResults = results
		}
	} else if track != "" {
		for _, prd := range ts.Periods {
			result, err := insights.ScoreTrack(track, prd.Signals, ceilings, custom)
			if err != nil {
				return fmt.Errorf("scoring track for period %s: %w", prd.Period, err)
			}
			prd.TrackResult = result
		}
	}

	outputPath, _ := cmd.Flags().GetString("output")
	if outputPath == "" {
		outputPath = defaultReportPath("trends", rf)
	}

	if err := WriteTrendsReport(ts, rf, outputPath); err != nil {
		return err
	}
	ui.Successf("Wrote trends report to %s\n", outputPath)

	return nil
}

// mixedGitHubStrategies returns a warning string if the auto-selected GitHub
// API strategies differ across periods. Returns "" if strategies are consistent
// or if the override is not "auto".
func mixedGitHubStrategies(periods []Period, strategyOverride string) string {
	override := api.APIStrategy(strategyOverride)
	if override == "" {
		override = api.StrategyAuto
	}
	if override != api.StrategyAuto {
		return "" // explicit override — no mixing
	}

	seen := make(map[api.APIStrategy]bool)
	for _, p := range periods {
		startDate, err := time.Parse("2006-01-02", p.Start)
		if err != nil {
			continue
		}
		strategy, err := api.SelectStrategy(startDate, override)
		if err != nil {
			continue
		}
		seen[strategy] = true
	}
	if len(seen) <= 1 {
		return ""
	}
	return "warning: GitHub API strategy differs across periods (auto-selected).\n" +
		"For consistent cross-period data use: --github-api search"
}
