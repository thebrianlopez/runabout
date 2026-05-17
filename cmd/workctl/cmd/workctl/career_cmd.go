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
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ui"
)

func careerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "career",
		Short: "Score your work against a career track",
		Long: `Analyze Jira, Confluence, and GitHub data to score against a career track.

Built-in tracks: staff, platform, manager
Each track weights 8 signal dimensions differently to measure alignment.`,
		RunE: runCareer,
	}

	cmd.Flags().String("track", "staff", "Career track to score against (staff, platform, manager)")
	cmd.Flags().Bool("list-tracks", false, "List available career tracks and exit")
	cmd.Flags().Bool("json", false, "Output TrackResult as JSON to stdout")
	cmd.Flags().String("end", "", "End date (YYYY-MM-DD)")
	cmd.Flags().Bool("summary", false, "Generate summary statistics")

	registerAllFetchFlags(cmd)
	registerReportOutputFlags(cmd, "json")

	return cmd
}

func buildCustomTracks() map[string]insights.CustomTrack {
	if fileConfig == nil || fileConfig.CareerLens == nil || len(fileConfig.CareerLens.Tracks) == 0 {
		return nil
	}
	custom := make(map[string]insights.CustomTrack, len(fileConfig.CareerLens.Tracks))
	for name, tc := range fileConfig.CareerLens.Tracks {
		custom[name] = insights.CustomTrack{
			Description: tc.Description,
			Inherit:     tc.Inherit,
			Weights:     tc.Weights,
		}
	}
	return custom
}

func runCareer(cmd *cobra.Command, args []string) error {
	custom := buildCustomTracks()

	// Validate custom track weights before doing anything expensive
	for name, ct := range custom {
		if err := insights.ValidateTrackWeights(ct.Weights); err != nil {
			return fmt.Errorf("custom track %q: %w", name, err)
		}
	}

	// Handle --list-tracks
	listTracks, _ := cmd.Flags().GetBool("list-tracks")
	if listTracks {
		fmt.Println("Available career tracks:")
		for _, name := range insights.ListTracks(custom) {
			_, desc, _ := insights.ResolveTrack(name, custom)
			label := ""
			if _, isCustom := custom[name]; isCustom {
				label = " [custom]"
			}
			fmt.Printf("  %-10s %s%s\n", name, desc, label)
		}
		return nil
	}

	rc := resolved

	ctx := context.Background()
	rd, err := FetchReportData(ctx, rc)
	if err != nil {
		return err
	}

	signals := rd.Signals

	// Resolve track and ceilings
	track, _ := cmd.Flags().GetString("track")

	var ceilings map[string]float64
	if fileConfig != nil && fileConfig.CareerLens != nil {
		ceilings = fileConfig.CareerLens.Ceilings
	}

	result, err := insights.ScoreTrack(track, signals, ceilings, custom)
	if err != nil {
		return err
	}

	// Handle --json output
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	// Determine output path
	outputPath, _ := cmd.Flags().GetString("output")
	if outputPath == "" {
		outputPath = filepath.Join(config.WorkctlStateDir(), "career.md")
	}

	// Render report
	period := rd.Period
	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer f.Close()

	insights.RenderCareer(f, result, period)

	// Print summary to stdout
	ui.Infof("Career track: %s\n", result.Track)
	ui.Infof("Overall score: %.1f%%\n", result.Overall*100)
	ui.Successf("Wrote career report to %s\n", outputPath)

	return nil
}
