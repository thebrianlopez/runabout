package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/thebrianlopez/runabout/cmd/chain-eval/internal/chainindex"
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

func runIndex(_ *cobra.Command, cfg indexRunConfig) int {
	docsRoot := resolveDocsRoot(cfg.docsRoot)

	// Validate docs root exists.
	if _, err := os.Stat(docsRoot); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "chain-eval index: docs root not found at %s\n", docsRoot)
		return 1
	}

	// Resolve output path.
	outputPath := cfg.output
	if outputPath == "" {
		outputPath = filepath.Join(docsRoot, ".chain-index.json")
	}

	// Resolve schema dir.
	schemaDir := cfg.schemaDir
	if schemaDir == "" {
		schemaDir = filepath.Join(docsRoot, "core/schemas")
	}

	fmt.Fprintf(os.Stderr, "chain-eval index: scanning %s\n", docsRoot)

	// Scan artifacts.
	records, err := chainindex.Scan(docsRoot, time.Now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "chain-eval index: scan failed: %v\n", err)
		return 1
	}

	// Build chain index.
	idx := chainindex.Build(records, docsRoot, cfg.includeLegacy)
	idx.IndexedAt = time.Now().UTC().Format(time.RFC3339)

	// Compute content hash.
	hash, err := chainindex.ComputeContentHash(docsRoot)
	if err != nil {
		if !cfg.quiet {
			fmt.Fprintf(os.Stderr, "chain-eval index: WARN: content hash failed: %v\n", err)
		}
	} else {
		idx.ContentHash = hash
	}

	// F2 validation: validate gate records and workspace links before write.
	if valErr := chainindex.ValidateGateRecords(idx.GateRecords, schemaDir); valErr != nil {
		if errors.Is(valErr, chainindex.ErrCUENotFound) {
			if !cfg.quiet {
				fmt.Fprintf(os.Stderr, "chain-eval index: WARN: cue not in PATH - skipping output validation (index written unvalidated)\n")
			}
		} else {
			fmt.Fprintf(os.Stderr, "chain-eval index: gate record CUE validation failed: %v\n", valErr)
			return 2
		}
	}
	if valErr := chainindex.ValidateWorkspaceLinks(idx.WorkspaceLinks, schemaDir); valErr != nil {
		if errors.Is(valErr, chainindex.ErrCUENotFound) {
			if !cfg.quiet {
				fmt.Fprintf(os.Stderr, "chain-eval index: WARN: cue not in PATH - skipping workspace link validation\n")
			}
		} else {
			fmt.Fprintf(os.Stderr, "chain-eval index: workspace link CUE validation failed: %v\n", valErr)
			return 2
		}
	}

	// Serialize.
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "chain-eval index: marshal failed: %v\n", err)
		return 1
	}

	// Atomic write.
	if err := writeIndexAtomic(outputPath, data); err != nil {
		fmt.Fprintf(os.Stderr, "chain-eval index: failed to write index to %s: %v\n", outputPath, err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "chain-eval index: wrote %d artifacts, %d chains, %d orphans → %s\n",
		len(idx.Artifacts), len(idx.Chains), len(idx.Orphans), outputPath)
	return 0
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
		tmp.Close()        //nolint:errcheck
		os.Remove(tmpName) //nolint:errcheck
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
