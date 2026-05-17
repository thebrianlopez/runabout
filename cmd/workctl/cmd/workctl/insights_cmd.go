package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/config"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/insights"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ui"
)

func insightsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "insights",
		Short: "Generate career growth insights from work activity data",
		Long: `Analyze Jira, Confluence, and GitHub data to extract career signals:
  - Theme distribution (bugs, features, infrastructure, incidents)
  - Monthly velocity (created vs closed)
  - Focus distribution across projects, spaces, and repos
  - Collaboration signals (PR reviews, issue comments, cross-team work)
  - Ownership signals (closure rate, incident ratio)`,
		RunE: runInsights,
	}

	cmd.Flags().String("end", "", "End date (YYYY-MM-DD)")
	cmd.Flags().Bool("summary", false, "Generate summary statistics")

	registerAllFetchFlags(cmd)
	registerReportOutputFlags(cmd, "json")

	return cmd
}

func runInsights(cmd *cobra.Command, args []string) error {
	rc := resolved

	// Fetch data using the same pipeline as the root command
	ctx := context.Background()
	rd, err := fetchAllData(ctx, rc)
	if err != nil {
		return err
	}

	// Determine output path
	outputPath, _ := cmd.Flags().GetString("output")
	if outputPath == "" {
		outputPath = filepath.Join(config.WorkctlStateDir(), "insights.md")
	}

	// Render report
	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer f.Close()

	insights.RenderInsights(f, rd.Signals, rd.Period)
	ui.Successf("Wrote insights report to %s\n", outputPath)

	return nil
}

// fetchAllData loads data from existing JSON exports if present, otherwise
// delegates to FetchReportData for a live API+cache fetch (including local signals).
func fetchAllData(ctx context.Context, rc *config.ResolvedConfig) (*ReportData, error) {
	var issues []models.Issue
	var articles []models.ConfluenceArticle
	var activities []models.GitHubActivity

	// Try loading from existing JSON exports first (avoids redundant API calls)
	jiraPath := filepath.Join(config.WorkctlStateDir(), "jira.json")
	confPath := filepath.Join(config.WorkctlStateDir(), "confluence.json")
	ghPath := filepath.Join(config.WorkctlStateDir(), "github.json")

	if data, err := os.ReadFile(jiraPath); err == nil && rc.Jira {
		if err := json.Unmarshal(data, &issues); err != nil {
			config.LogDebug("Failed to load cached Jira JSON: %v", err)
			issues = nil
		} else {
			ui.Successf("Loaded %d Jira issues from %s\n", len(issues), jiraPath)
		}
	}

	if data, err := os.ReadFile(confPath); err == nil && rc.Confluence {
		if err := json.Unmarshal(data, &articles); err != nil {
			config.LogDebug("Failed to load cached Confluence JSON: %v", err)
			articles = nil
		} else {
			ui.Successf("Loaded %d Confluence articles from %s\n", len(articles), confPath)
		}
	}

	if data, err := os.ReadFile(ghPath); err == nil && rc.GitHub {
		if err := json.Unmarshal(data, &activities); err != nil {
			config.LogDebug("Failed to load cached GitHub JSON: %v", err)
			activities = nil
		} else {
			ui.Successf("Loaded %d GitHub activities from %s\n", len(activities), ghPath)
		}
	}

	// If we have flat-file data, use it without hitting the API
	if len(issues) > 0 || len(articles) > 0 || len(activities) > 0 {
		return &ReportData{
			Period:     insights.FormatPeriod(rc.StartDate, rc.EndDate),
			Signals:    insights.ExtractSignals(issues, articles, activities),
			Issues:     issues,
			Activities: activities,
		}, nil
	}

	// Otherwise, delegate to the canonical API+cache fetch (includes local signals)
	return FetchReportData(ctx, rc)
}
