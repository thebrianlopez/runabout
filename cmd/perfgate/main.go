package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/blo-grindr/runabouts/internal/perfgate"
	"github.com/blo-grindr/runabouts/internal/telemetry"
	"github.com/blo-grindr/runabouts/internal/version"
	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "perfgate",
		Short:   "Performance gate with statistical before/after comparison",
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version.Version, version.Commit, version.Date),
	}

	rootCmd.AddCommand(runCmd())
	rootCmd.AddCommand(compareCmd())
	rootCmd.AddCommand(gateCmd())

	t := telemetry.Instrument(rootCmd, "perfgate")
	err := rootCmd.Execute()
	t.Emit(err)
	if err != nil {
		os.Exit(1)
	}
}

func runCmd() *cobra.Command {
	var cmdStr string
	var runs int
	var warmup int
	var save string
	var format string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Benchmark a command",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := perfgate.RunConfig{
				Cmd:    cmdStr,
				Runs:   runs,
				Warmup: warmup,
				SaveTo: save,
			}
			result, err := perfgate.Run(cfg)
			if err != nil {
				return err
			}
			if format == "json" {
				return printJSON(result)
			}
			printStats("Results", result.Stats)
			if save != "" {
				fmt.Fprintf(os.Stderr, "\nSaved to %s\n", save)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&cmdStr, "cmd", "", "command to benchmark")
	cmd.Flags().IntVar(&runs, "runs", 10, "number of runs")
	cmd.Flags().IntVar(&warmup, "warmup", 2, "number of warmup iterations")
	cmd.Flags().StringVar(&save, "save", "", "save results to file")
	cmd.Flags().StringVar(&format, "format", "text", "output format (text/json)")
	_ = cmd.MarkFlagRequired("cmd")

	return cmd
}

func compareCmd() *cobra.Command {
	var before, after string
	var format string

	cmd := &cobra.Command{
		Use:   "compare",
		Short: "Compare two benchmark results",
		RunE: func(cmd *cobra.Command, args []string) error {
			beforeResult, err := perfgate.LoadResult(before)
			if err != nil {
				return fmt.Errorf("load before: %w", err)
			}
			afterResult, err := perfgate.LoadResult(after)
			if err != nil {
				return fmt.Errorf("load after: %w", err)
			}

			if format == "json" {
				comparison := map[string]interface{}{
					"before":     beforeResult.Stats,
					"after":      afterResult.Stats,
					"regression": (afterResult.Stats.Mean - beforeResult.Stats.Mean) / beforeResult.Stats.Mean * 100,
				}
				return printJSON(comparison)
			}

			printStats("Before", beforeResult.Stats)
			fmt.Println()
			printStats("After", afterResult.Stats)
			fmt.Println()

			if beforeResult.Stats.Mean > 0 {
				regression := (afterResult.Stats.Mean - beforeResult.Stats.Mean) / beforeResult.Stats.Mean * 100
				fmt.Printf("Change: %+.2f%%\n", regression)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&before, "before", "", "baseline results file")
	cmd.Flags().StringVar(&after, "after", "", "current results file")
	cmd.Flags().StringVar(&format, "format", "text", "output format (text/json)")
	_ = cmd.MarkFlagRequired("before")
	_ = cmd.MarkFlagRequired("after")

	return cmd
}

func gateCmd() *cobra.Command {
	var cmdStr, baseline string
	var runs, warmup int
	var maxRegression, minImprovement float64
	var format string

	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Run a performance gate (pass/fail)",
		RunE: func(cmd *cobra.Command, args []string) error {
			baselineResult, err := perfgate.LoadResult(baseline)
			if err != nil {
				return fmt.Errorf("load baseline: %w", err)
			}

			cfg := perfgate.RunConfig{
				Cmd:    cmdStr,
				Runs:   runs,
				Warmup: warmup,
			}
			currentResult, err := perfgate.Run(cfg)
			if err != nil {
				return err
			}

			gateCfg := perfgate.GateConfig{
				MaxRegression:  maxRegression,
				MinImprovement: minImprovement,
			}
			gateResult := perfgate.Evaluate(baselineResult.Stats, currentResult.Stats, gateCfg)

			if format == "json" {
				out := map[string]interface{}{
					"baseline": baselineResult.Stats,
					"current":  currentResult.Stats,
					"gate":     gateResult,
				}
				if err := printJSON(out); err != nil {
					return err
				}
			} else {
				printStats("Baseline", baselineResult.Stats)
				fmt.Println()
				printStats("Current", currentResult.Stats)
				fmt.Println()
				fmt.Println(gateResult.Message)
			}

			if !gateResult.Passed {
				os.Exit(1)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&cmdStr, "cmd", "", "command to benchmark")
	cmd.Flags().StringVar(&baseline, "baseline", "", "baseline results file")
	cmd.Flags().IntVar(&runs, "runs", 10, "number of runs")
	cmd.Flags().IntVar(&warmup, "warmup", 2, "number of warmup iterations")
	cmd.Flags().Float64Var(&maxRegression, "max-regression", 5.0, "maximum allowed regression percentage")
	cmd.Flags().Float64Var(&minImprovement, "min-improvement", 0, "minimum required improvement percentage")
	cmd.Flags().StringVar(&format, "format", "text", "output format (text/json)")
	_ = cmd.MarkFlagRequired("cmd")
	_ = cmd.MarkFlagRequired("baseline")

	return cmd
}

func printStats(label string, s perfgate.Stats) {
	fmt.Printf("%s (%d runs):\n", label, s.N)
	fmt.Printf("  Mean:   %.4fs\n", s.Mean)
	fmt.Printf("  Median: %.4fs\n", s.Median)
	fmt.Printf("  P95:    %.4fs\n", s.P95)
	fmt.Printf("  StdDev: %.4fs\n", s.StdDev)
	fmt.Printf("  Min:    %.4fs\n", s.Min)
	fmt.Printf("  Max:    %.4fs\n", s.Max)
}

func printJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
