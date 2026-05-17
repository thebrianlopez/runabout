package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/insights"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ui"
)

func quarterlyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quarterly",
		Short: "Compare the last 90 days against the prior 90 days",
		Long: `Fetch two consecutive 90-day periods of Jira, Confluence, and GitHub
activity and produce a delta report showing growth across all signals.

Example:
  workctl quarterly
  workctl quarterly --end 2025-06-30
  workctl quarterly --email user@company.com`,
		RunE: runQuarterly,
	}

	cmd.Flags().String("end", "", "End date (YYYY-MM-DD, default: today)")
	cmd.Flags().Bool("summary", false, "Generate summary statistics")

	registerAllFetchFlags(cmd)
	registerReportOutputFlags(cmd, "md")

	return cmd
}

func runQuarterly(cmd *cobra.Command, args []string) error {
	fmtFlag, _ := cmd.Flags().GetString("format")
	rf, err := parseFormat(fmtFlag)
	if err != nil {
		return err
	}

	endOverride, _ := cmd.Flags().GetString("end")

	// Current quarter: last 90 days ending at --end (or today)
	currStart, currEnd, err := computeWindowFromEnd("90d", endOverride)
	if err != nil {
		return fmt.Errorf("computing current quarter: %w", err)
	}

	// Previous quarter: 90 days before currStart
	currStartTime, _ := time.Parse("2006-01-02", currStart)
	prevStart, err := subtractDuration(currStartTime, "90d")
	if err != nil {
		return fmt.Errorf("computing previous quarter: %w", err)
	}

	prevStartStr := prevStart.Format("2006-01-02")
	prevEndStr := currStart

	currentPeriod := fmt.Sprintf("%s to %s", currStart, currEnd)
	previousPeriod := fmt.Sprintf("%s to %s", prevStartStr, prevEndStr)

	ui.Infof("Previous period: %s\n", previousPeriod)
	ui.Infof("Current period:  %s\n\n", currentPeriod)

	ctx := context.Background()

	// Fetch previous period
	prevRC := *resolved
	prevRC.StartDate = prevStartStr
	prevRC.EndDate = prevEndStr
	prevRD, err := FetchReportData(ctx, &prevRC)
	if err != nil {
		return fmt.Errorf("fetching previous period: %w", err)
	}

	// Fetch current period
	currRC := *resolved
	currRC.StartDate = currStart
	currRC.EndDate = currEnd
	currRD, err := FetchReportData(ctx, &currRC)
	if err != nil {
		return fmt.Errorf("fetching current period: %w", err)
	}

	// Compute delta and populate output descriptor
	currRD.Delta = insights.ComputeDelta(prevRD.Signals, currRD.Signals, previousPeriod, currentPeriod)
	currRD.ReportType = "quarterly"
	currRD.Format = rf
	currRD.OutputPath, _ = cmd.Flags().GetString("output")
	if currRD.OutputPath == "" {
		currRD.OutputPath = defaultReportPath("quarterly", rf)
	}

	if err := WriteReport(currRD); err != nil {
		return err
	}
	ui.Successf("Wrote quarterly report to %s\n", currRD.OutputPath)

	return nil
}
