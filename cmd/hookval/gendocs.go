package main

import (
	"fmt"
	"os"

	"github.com/blo-grindr/runabout/internal/hookval"
	"github.com/spf13/cobra"
)

func genDocsCmd() *cobra.Command {
	var schemaPath string

	cmd := &cobra.Command{
		Use:   "gen-docs",
		Short: "Generate Hook Context Signals markdown table from schema",
		RunE: func(cmd *cobra.Command, args []string) error {
			schema, err := hookval.LoadSchema(schemaPath)
			if err != nil {
				return err
			}
			fmt.Print(hookval.GenDocsTable(schema))
			return nil
		},
	}

	home, _ := os.UserHomeDir()
	cmd.Flags().StringVar(&schemaPath, "schema", home+"/.claude/hook-signal-schema.yaml", "path to schema YAML")

	return cmd
}
