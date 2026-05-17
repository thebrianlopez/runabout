package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/config"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/insights"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ui"
)

func compareCmd() *cobra.Command {
	var (
		since    string
		previous string
	)

	cmd := &cobra.Command{
		Use:   "compare",
		Short: "Compare work activity between two time periods",
		Long: `Run the same analysis for two time periods and produce a delta report
showing growth and changes across all career signals.

Example:
  workctl compare --since 6m --previous 6m --email user@company.com
  workctl compare --since 3m --previous 3m --project-keys SR,ISRE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCompare(cmd, since, previous)
		},
	}

	cmd.Flags().StringVar(&since, "since", "6m", "Current period duration (e.g., 6m, 3m, 1y)")
	cmd.Flags().StringVar(&previous, "previous", "6m", "Previous period duration (e.g., 6m, 3m, 1y)")
	cmd.Flags().String("end", "", "End date (YYYY-MM-DD)")
	cmd.Flags().Bool("summary", false, "Generate summary statistics")

	registerAllFetchFlags(cmd)
	registerReportOutputFlags(cmd, "json")

	return cmd
}

func runCompare(cmd *cobra.Command, since, previous string) error {
	rc := resolved

	// Parse durations
	now := time.Now()
	sinceDur, err := parseDuration(since)
	if err != nil {
		return fmt.Errorf("invalid --since %q: %w", since, err)
	}
	prevDur, err := parseDuration(previous)
	if err != nil {
		return fmt.Errorf("invalid --previous %q: %w", previous, err)
	}

	// Calculate date ranges
	currentEnd := now
	currentStart := now.Add(-sinceDur)
	previousEnd := currentStart
	previousStart := previousEnd.Add(-prevDur)

	currentPeriod := fmt.Sprintf("%s to %s", currentStart.Format("2006-01-02"), currentEnd.Format("2006-01-02"))
	previousPeriod := fmt.Sprintf("%s to %s", previousStart.Format("2006-01-02"), previousEnd.Format("2006-01-02"))

	ui.Infof("Previous period: %s\n", previousPeriod)
	ui.Infof("Current period:  %s\n\n", currentPeriod)

	// Fetch data for previous period
	prevRC := *rc
	prevRC.StartDate = previousStart.Format("2006-01-02")
	prevRC.EndDate = previousEnd.Format("2006-01-02")

	ctx := context.Background()
	prevRD, err := FetchReportData(ctx, &prevRC)
	if err != nil {
		return fmt.Errorf("fetching previous period: %w", err)
	}

	// Fetch data for current period
	currRC := *rc
	currRC.StartDate = currentStart.Format("2006-01-02")
	currRC.EndDate = currentEnd.Format("2006-01-02")

	currRD, err := FetchReportData(ctx, &currRC)
	if err != nil {
		return fmt.Errorf("fetching current period: %w", err)
	}

	// Compute delta
	report := insights.ComputeDelta(prevRD.Signals, currRD.Signals, previousPeriod, currentPeriod)

	// Output
	outputPath, _ := cmd.Flags().GetString("output")
	if outputPath == "" {
		outputPath = filepath.Join(config.WorkctlStateDir(), "comparison.md")
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output: %w", err)
	}
	defer f.Close()

	insights.RenderComparison(f, report)
	ui.Successf("Wrote comparison report to %s\n", outputPath)

	return nil
}

// parseDuration parses human-friendly durations like "6m", "3m", "1y", "30d".
func parseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("duration too short: %q", s)
	}

	numStr := s[:len(s)-1]
	unit := s[len(s)-1]

	var n int
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil {
		return 0, fmt.Errorf("invalid number in %q: %w", s, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("duration must be positive: %q", s)
	}

	switch unit {
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	case 'm':
		return time.Duration(n) * 30 * 24 * time.Hour, nil // approximate
	case 'y':
		return time.Duration(n) * 365 * 24 * time.Hour, nil // approximate
	default:
		return 0, fmt.Errorf("unknown unit %q in %q (use d/w/m/y)", string(unit), s)
	}
}
