package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/cache"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/config"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ui"
)

func cacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage the local result cache",
	}
	cmd.AddCommand(cacheStatsCmd())
	cmd.AddCommand(cacheClearCmd())
	cmd.AddCommand(cacheWarmCmd())
	return cmd
}

func cacheStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show cache statistics",
		RunE:  runCacheStats,
	}
}

func cacheClearCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear cached results",
		RunE:  runCacheClear,
	}
	cmd.Flags().String("source", "", "Clear only entries for a specific source (jira, confluence, github_events, github_search, github_graphql)")
	cmd.Flags().String("older-than", "", "Clear entries older than duration (e.g. 7d, 24h)")
	return cmd
}

func runCacheStats(cmd *cobra.Command, args []string) error {
	dbPath := filepath.Join(config.WorkctlCacheDir(), "cache.db")
	store := cache.Open(dbPath)
	if store == nil {
		fmt.Println("No cache database found.")
		return nil
	}
	defer store.Close()

	stats, err := store.GetStats()
	if err != nil {
		return fmt.Errorf("reading cache stats: %w", err)
	}

	fmt.Printf("Cache: %s\n", dbPath)
	fmt.Printf("Total entries: %d\n", stats.TotalEntries)
	fmt.Printf("Total size:    %s (uncompressed)\n", humanSize(stats.TotalBytes))

	if len(stats.BySource) > 0 {
		fmt.Println("\nBy source:")
		for source, ss := range stats.BySource {
			fmt.Printf("  %-20s %d entries  %s\n", source, ss.Entries, humanSize(int64(ss.Bytes)))
		}
	}
	return nil
}

func runCacheClear(cmd *cobra.Command, args []string) error {
	dbPath := filepath.Join(config.WorkctlCacheDir(), "cache.db")
	store := cache.Open(dbPath)
	if store == nil {
		fmt.Println("No cache database found.")
		return nil
	}
	defer store.Close()

	source, _ := cmd.Flags().GetString("source")
	olderThanStr, _ := cmd.Flags().GetString("older-than")

	var olderThan time.Duration
	if olderThanStr != "" {
		d, err := parseDurationFlexible(olderThanStr)
		if err != nil {
			return fmt.Errorf("invalid --older-than: %w", err)
		}
		olderThan = d
	}

	if err := store.Clear(source, olderThan); err != nil {
		return fmt.Errorf("clearing cache: %w", err)
	}

	parts := []string{"Cache cleared"}
	if source != "" {
		parts = append(parts, fmt.Sprintf("source=%s", source))
	}
	if olderThan > 0 {
		parts = append(parts, fmt.Sprintf("older-than=%s", olderThan))
	}
	fmt.Println(strings.Join(parts, " "))
	return nil
}

func cacheWarmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "warm",
		Short: "Pre-fetch data to populate the cache",
		Long: `Pre-fetch all data for a profile's date range, populating the cache
for faster subsequent runs.

Examples:
  workctl cache warm                              # warm the current profile's date range
  workctl cache warm --profile annual-review      # warm the annual-review profile
  workctl cache warm --periods 4 --period-size 3m # warm 4 quarterly periods`,
		RunE: runCacheWarm,
	}
	cmd.Flags().Int("periods", 0, "Number of periods to warm (≥ 2; 0 for single range)")
	cmd.Flags().String("period-size", "3m", "Length of each period: e.g. 3m, 1m, 7d")
	cmd.Flags().String("end", "", "End date of the most recent period (YYYY-MM-DD, default: today)")
	registerAllFetchFlags(cmd)
	return cmd
}

func runCacheWarm(cmd *cobra.Command, args []string) error {
	rc := *resolved
	n, _ := cmd.Flags().GetInt("periods")
	periodSize, _ := cmd.Flags().GetString("period-size")
	endOverride, _ := cmd.Flags().GetString("end")

	ctx := cmd.Context()
	spin := ui.NewSpinner()

	if n >= 2 {
		// Multi-period warming (like trends)
		var end time.Time
		if endOverride != "" {
			var err error
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

		ui.Infof("Cache warm: %d × %s periods ending %s\n", n, periodSize, end.Format("2006-01-02"))
		for i, p := range periods {
			periodRC := rc
			periodRC.StartDate = p.Start
			periodRC.EndDate = p.End
			ws, err := WarmReportData(ctx, &periodRC)
			if err != nil {
				spin.Stop("")
				return fmt.Errorf("period %s: %w", p.Label, err)
			}
			if ws.AnythingFetched {
				spin.Stop(ui.SuccessSprintf("  %d/%d: %s fetched", i+1, len(periods), p.Label))
			} else {
				spin.Stop(ui.DimSprintf("  %d/%d: %s cached", i+1, len(periods), p.Label))
			}
		}
	} else {
		// Single date range warming
		ui.Infof("Cache warm: %s to %s\n", rc.StartDate, rc.EndDate)
		spin.Start("Warming cache...")
		ws, err := WarmReportData(ctx, &rc)
		if err != nil {
			spin.Stop("")
			return err
		}
		if ws.AnythingFetched {
			spin.Stop("Fetched and cached.")
		} else {
			spin.Stop("Already cached.")
		}
	}

	// Print summary
	dbPath := filepath.Join(config.WorkctlCacheDir(), "cache.db")
	store := cache.Open(dbPath)
	if store != nil {
		defer store.Close()
		stats, err := store.GetStats()
		if err == nil {
			ui.Successf("Cache warmed: %d entries, %s total\n", stats.TotalEntries, humanSize(stats.TotalBytes))
		}
	}
	return nil
}

// parseDurationFlexible extends time.ParseDuration with "d" (days) support.
func parseDurationFlexible(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		numStr := strings.TrimSuffix(s, "d")
		var n int
		if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid duration: %s", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func humanSize(bytes int64) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
