package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/ui"
)

func weeklyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "weekly",
		Short: "Generate a 7-day work insights report",
		Long: `Fetch the last 7 days of Jira, Confluence, and GitHub activity
and produce a career-signals insights report.

Example:
  workctl weekly
  workctl weekly --format json
  workctl weekly --format pdf --output ~/reports/weekly.pdf
  workctl weekly --end 2025-06-15
  workctl weekly --email user@company.com --output weekly.md`,
		RunE: runWeekly,
	}

	cmd.Flags().String("end", "", "End date (YYYY-MM-DD, default: today)")
	cmd.Flags().Bool("summary", false, "Generate summary statistics")

	// Standup publisher flags (EPIC-014)
	cmd.Flags().Bool("publish", false, "Publish standup page to Confluence after generating the report")
	cmd.Flags().Bool("dry-run", false, "Render standup HTML and print to stdout; skip Confluence API call")
	cmd.Flags().String("confluence-space-key", "", "Confluence space key for the standup page (e.g. ~accountId)")
	cmd.Flags().String("confluence-folder-id", "", "Confluence folder/page ID to publish under")
	cmd.Flags().String("standup-author", "", "Author display name for the standup page (default: derived from email)")
	cmd.Flags().String("standup-notes", "", "YAML file with 'learnings' and 'next_week_plan' lists")

	registerAllFetchFlags(cmd)
	registerReportOutputFlags(cmd, "md")

	return cmd
}

func runWeekly(cmd *cobra.Command, args []string) error {
	fmtFlag, _ := cmd.Flags().GetString("format")
	rf, err := parseFormat(fmtFlag)
	if err != nil {
		return err
	}

	endOverride, _ := cmd.Flags().GetString("end")
	start, end, err := computeWindowFromEnd("7d", endOverride)
	if err != nil {
		return fmt.Errorf("computing date window: %w", err)
	}

	ui.Infof("Weekly period: %s to %s\n\n", start, end)

	rc := *resolved
	rc.StartDate = start
	rc.EndDate = end

	ctx := context.Background()
	rd, err := FetchReportData(ctx, &rc)
	if err != nil {
		return err
	}

	rd.ReportType = "weekly"
	rd.Format = rf
	rd.OutputPath, _ = cmd.Flags().GetString("output")
	if rd.OutputPath == "" {
		rd.OutputPath = defaultReportPath("weekly", rf)
	}

	if err := WriteReport(rd); err != nil {
		return err
	}
	ui.Successf("Wrote weekly report to %s\n", rd.OutputPath)

	// Standup publish: triggered by --publish or --dry-run
	publish, _ := cmd.Flags().GetBool("publish")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if publish || dryRun {
		if err := runStandupPublish(cmd, rd); err != nil {
			return err
		}
	}

	return nil
}
