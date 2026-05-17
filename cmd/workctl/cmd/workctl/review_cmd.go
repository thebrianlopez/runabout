package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/insights"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ui"
)

func reviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Generate a full-year insights and career track report",
		Long: `Fetch 365 days of Jira, Confluence, and GitHub activity, extract
career signals, score against a career track, and produce a combined
insights + career report.

Example:
  workctl review
  workctl review --track platform --end 2025-12-31
  workctl review --email user@company.com --output review.md`,
		RunE: runReview,
	}

	cmd.Flags().String("track", "staff", "Career track to score against (staff, platform, manager)")
	cmd.Flags().String("end", "", "End date (YYYY-MM-DD, default: today)")
	cmd.Flags().Bool("summary", false, "Generate summary statistics")

	registerAllFetchFlags(cmd)
	registerReportOutputFlags(cmd, "md")

	return cmd
}

func runReview(cmd *cobra.Command, args []string) error {
	fmtFlag, _ := cmd.Flags().GetString("format")
	rf, err := parseFormat(fmtFlag)
	if err != nil {
		return err
	}

	// Validate custom tracks early
	custom := buildCustomTracks()
	for name, ct := range custom {
		if err := insights.ValidateTrackWeights(ct.Weights); err != nil {
			return fmt.Errorf("custom track %q: %w", name, err)
		}
	}

	endOverride, _ := cmd.Flags().GetString("end")

	start, end, err := computeWindowFromEnd("365d", endOverride)
	if err != nil {
		return fmt.Errorf("computing date window: %w", err)
	}

	ui.Infof("Review period: %s to %s\n\n", start, end)

	// Copy resolved config and inject computed dates
	rc := *resolved
	rc.StartDate = start
	rc.EndDate = end

	ctx := context.Background()
	rd, err := FetchReportData(ctx, &rc)
	if err != nil {
		return err
	}

	// Score career track
	track, _ := cmd.Flags().GetString("track")

	var ceilings map[string]float64
	if fileConfig != nil && fileConfig.CareerLens != nil {
		ceilings = fileConfig.CareerLens.Ceilings
	}

	result, err := insights.ScoreTrack(track, rd.Signals, ceilings, custom)
	if err != nil {
		return err
	}

	rd.TrackResult = result
	rd.ReportType = "review"
	rd.Format = rf
	rd.OutputPath, _ = cmd.Flags().GetString("output")
	if rd.OutputPath == "" {
		rd.OutputPath = defaultReportPath("review", rf)
	}

	if err := WriteReport(rd); err != nil {
		return err
	}

	ui.Infof("Career track: %s\n", result.Track)
	ui.Infof("Overall score: %.1f%%\n", result.Overall*100)
	ui.Successf("Wrote review report to %s\n", rd.OutputPath)

	return nil
}
