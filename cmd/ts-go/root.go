package main

import (
	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "ts-go",
		Short:   "Tree-sitter powered Go context extraction",
		Long:    "Extract function signatures, type declarations, and function bodies from Go files using tree-sitter.",
		Version: versionString(),
	}

	rootCmd.PersistentFlags().StringVar(&formatFlag, "format", "json", "output format: json | compact")

	rootCmd.AddCommand(newFuncsCmd())
	rootCmd.AddCommand(newTypesCmd())
	rootCmd.AddCommand(newExtractCmd())
	rootCmd.AddCommand(newSearchCmd())
	rootCmd.AddCommand(newRewriteCmd())

	return rootCmd
}
