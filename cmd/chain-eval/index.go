package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// renameFunc is injectable for tests to simulate atomic write failures.
var renameFunc = os.Rename

func indexCmd() *cobra.Command {
	var (
		flagDocsRoot      string
		flagOutput        string
		flagSchemaDir     string
		flagIncludeLegacy bool
		flagQuiet         bool
	)

	cmd := &cobra.Command{
		Use:   "index",
		Short: "Build a deterministic chain index from docs/",
		Long: `Scans docs/ artifact directories and writes .chain-index.json.
No LLM is invoked. Exit 0 on success, 1 on fatal error, 2 on CUE validation failure.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := indexRunConfig{
				docsRoot:      flagDocsRoot,
				output:        flagOutput,
				schemaDir:     flagSchemaDir,
				includeLegacy: flagIncludeLegacy,
				quiet:         flagQuiet,
			}
			os.Exit(runIndex(cmd, cfg))
			return nil
		},
	}

	cmd.Flags().StringVar(&flagDocsRoot, "docs-root", "", "Absolute path to docs root (overrides $CHAIN_DOCS_ROOT)")
	cmd.Flags().StringVar(&flagOutput, "output", "", "Index output path (default: {docs-root}/.chain-index.json)")
	cmd.Flags().StringVar(&flagSchemaDir, "schema-dir", "", "CUE schemas dir (default: {docs-root}/core/schemas/)")
	cmd.Flags().BoolVar(&flagIncludeLegacy, "include-legacy", false, "Include pre-2026-04-21 artifacts in orphan detection")
	cmd.Flags().BoolVar(&flagQuiet, "quiet", false, "Suppress non-fatal warnings")

	return cmd
}

type indexRunConfig struct {
	docsRoot      string
	output        string
	schemaDir     string
	includeLegacy bool
	quiet         bool
}

func runIndex(cmd *cobra.Command, cfg indexRunConfig) int {
	panic("not implemented")
}

// resolveDocsRoot returns the docs root using the priority order from the TDD:
//  1. --docs-root flag
//  2. CHAIN_DOCS_ROOT env var
//  3. ./docs/ relative fallback
func resolveDocsRoot(flag string) string {
	if flag != "" {
		return flag
	}
	if env := os.Getenv("CHAIN_DOCS_ROOT"); env != "" {
		return env
	}
	return "./docs/"
}

// writeIndexAtomic writes data to a temp file in the same directory as outputPath
// and renames it to outputPath atomically. Uses renameFunc for testability.
func writeIndexAtomic(outputPath string, data []byte) error {
	dir := outputPath[:max(0, lastSlash(outputPath))]
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, ".chain-index-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()          //nolint:errcheck
		os.Remove(tmpName)   //nolint:errcheck
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName) //nolint:errcheck
		return fmt.Errorf("close temp: %w", err)
	}
	if err := renameFunc(tmpName, outputPath); err != nil {
		os.Remove(tmpName) //nolint:errcheck
		return err
	}
	return nil
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
