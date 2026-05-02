package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/thebrianlopez/runabout/internal/hookval"
	"github.com/spf13/cobra"
)

func validateCmd() *cobra.Command {
	var schemaPath, hookPath, workDir string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Run hook and validate emitted signals against schema",
		RunE: func(cmd *cobra.Command, args []string) error {
			schema, err := hookval.LoadSchema(schemaPath)
			if err != nil {
				return err
			}

			if workDir == "" {
				workDir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("getting working directory: %w", err)
				}
			}

			out, err := hookval.RunHook(hookPath, workDir)
			if err != nil {
				return fmt.Errorf("running hook: %w", err)
			}

			signals, err := hookval.ParseContext(out)
			if err != nil {
				return fmt.Errorf("parsing hook output: %w", err)
			}

			results := hookval.ValidateSignals(schema, signals)

			// Print in sorted signal order for deterministic output.
			sort.Slice(results, func(i, j int) bool {
				return results[i].Name < results[j].Name
			})

			failed := 0
			for _, r := range results {
				if r.Pass {
					fmt.Printf("✅ %s=%s\n", r.Name, r.Value)
				} else {
					fmt.Printf("❌ %s=%s (expected: %s)\n", r.Name, r.Value, r.Rule)
					failed++
				}
			}

			if failed > 0 {
				return fmt.Errorf("%d signal(s) failed validation", failed)
			}
			return nil
		},
	}

	home, _ := os.UserHomeDir()
	cmd.Flags().StringVar(&schemaPath, "schema", home+"/.claude/hook-signal-schema.yaml", "path to schema YAML")
	cmd.Flags().StringVar(&hookPath, "hook", home+"/.claude/hooks/prompt-context.fish", "path to hook script")
	cmd.Flags().StringVar(&workDir, "dir", "", "working directory for hook invocation (default: cwd)")

	return cmd
}
