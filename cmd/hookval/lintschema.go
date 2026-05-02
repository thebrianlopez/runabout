package main

import (
	"fmt"
	"os"

	"github.com/thebrianlopez/runabout/internal/hookval"
	"github.com/spf13/cobra"
)

func lintSchemaCmd() *cobra.Command {
	var schemaPath string

	cmd := &cobra.Command{
		Use:   "lint-schema",
		Short: "Validate that the schema YAML is well-formed and complete",
		RunE: func(cmd *cobra.Command, args []string) error {
			schema, err := hookval.LoadSchema(schemaPath)
			if err != nil {
				return err
			}
			errs := hookval.LintSchema(schema)
			if len(errs) == 0 {
				fmt.Println("✅ schema is valid")
				return nil
			}
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "❌ %s\n", e)
			}
			return fmt.Errorf("%d schema error(s)", len(errs))
		},
	}

	home, _ := os.UserHomeDir()
	cmd.Flags().StringVar(&schemaPath, "schema", home+"/.claude/hook-signal-schema.yaml", "path to schema YAML")

	return cmd
}
