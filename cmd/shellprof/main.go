package main

import (
	"fmt"
	"os"

	"github.com/blo-grindr/runabout/internal/shellprof"
	"github.com/blo-grindr/runabout/internal/telemetry"
	"github.com/blo-grindr/runabout/internal/version"
	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "shellprof",
		Short:   "Shell function profiler with call graphs",
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version.Version, version.Commit, version.Date),
	}

	rootCmd.AddCommand(profileCmd())
	rootCmd.AddCommand(traceCmd())
	rootCmd.AddCommand(listCmd())

	t := telemetry.Instrument(rootCmd, "shellprof")
	err := rootCmd.Execute()
	t.Emit(err)
	if err != nil {
		os.Exit(1)
	}
}

func profileCmd() *cobra.Command {
	var depth int
	var format string

	cmd := &cobra.Command{
		Use:   "profile <function>",
		Short: "Profile a fish shell function",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := shellprof.ProfileConfig{
				Depth:  depth,
				Format: format,
			}
			profile, err := shellprof.Run(args[0], cfg)
			if err != nil {
				return err
			}
			_ = profile
			return nil
		},
	}

	cmd.Flags().IntVar(&depth, "depth", 3, "maximum call depth to trace")
	cmd.Flags().StringVar(&format, "format", "text", "output format (text, json, flame)")

	return cmd
}

func traceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "trace <function>",
		Short: "Single-run trace of a fish shell function",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := shellprof.ProfileConfig{
				Depth:  -1,
				Format: "text",
			}
			profile, err := shellprof.Run(args[0], cfg)
			if err != nil {
				return err
			}
			_ = profile
			return nil
		},
	}
}

func listCmd() *cobra.Command {
	var sortBy string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List profiled functions",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = sortBy
			return fmt.Errorf("not yet implemented: list --sort %s", sortBy)
		},
	}

	cmd.Flags().StringVar(&sortBy, "sort", "time", "sort by (time, calls, name)")

	return cmd
}
