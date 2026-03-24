package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/blo-grindr/runabout/internal/effiscore"
	"github.com/blo-grindr/runabout/internal/telemetry"
	versionpkg "github.com/blo-grindr/runabout/internal/version"
	"github.com/spf13/cobra"
)

var (
	version = "0.1.0"
	commit  = "dev"
	date    = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "effiscore",
		Short:   "Anthropic API efficiency scoring via Datadog metrics",
		Version: versionpkg.Format(version, commit, date),
	}

	rootCmd.AddCommand(scoreCmd())

	t := telemetry.Instrument(rootCmd, "effiscore")
	err := rootCmd.Execute()
	t.Emit(err)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func scoreCmd() *cobra.Command {
	var user, window string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "score",
		Short: "Compute efficiency score for a user",
		RunE: func(cmd *cobra.Command, args []string) error {
			apiKey := os.Getenv("DD_API_KEY")
			appKey := os.Getenv("DD_APP_KEY")
			if apiKey == "" {
				return fmt.Errorf("DD_API_KEY environment variable required")
			}
			if appKey == "" {
				return fmt.Errorf("DD_APP_KEY environment variable required")
			}

			windowDays := parseWindow(window)

			client := effiscore.NewClient(apiKey, appKey)
			raw, health, err := client.FetchAll(user, windowDays)
			if err != nil {
				return err
			}

			result := effiscore.Compute(user, windowDays, raw)

			// Topology bus emissions (non-blocking, fire-and-forget)
			effiscore.EmitEfficiency(result)
			effiscore.EmitHealth(user, health)

			if jsonOut {
				data, err := effiscore.RenderJSON(result)
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}
			effiscore.RenderText(os.Stdout, result)
			return nil
		},
	}

	cmd.Flags().StringVar(&user, "user", "", "Datadog user_name tag (required)")
	cmd.Flags().StringVar(&window, "window", "7d", "query window (e.g. 7d, 14d, 30d)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON instead of plain text")
	_ = cmd.MarkFlagRequired("user")

	return cmd
}

func parseWindow(s string) int {
	s = strings.TrimSuffix(strings.TrimSpace(s), "d")
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 7
	}
	return n
}
