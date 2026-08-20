package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	cmd.Flags().StringVar(&flagSchemaDir, "schema-dir", "", "CUE schemas dir (default: $CHAIN_SCHEMA_DIR, then $WS_ORG_CORE/schemas/cue, then {docs-root}/core/schemas[/cue], then ~/core/schemas/cue)")
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
	schemaDir, schemaResolved := resolveSchemaDir(cfg.schemaDir, docsRoot)
	if !schemaResolved && !cfg.quiet {
		fmt.Fprintf(os.Stderr, "chain-eval index: WARN: schema_not_found - no chain_gate.cue under any candidate dir (tried: %s); output validation will be skipped\n",
			strings.Join(schemaDirCandidates(cfg.schemaDir, docsRoot), ", "))
	}

	fmt.Fprintf(os.Stderr, "chain-eval index: scanning %s\n", docsRoot)
	if schemaResolved && !cfg.quiet {
		fmt.Fprintf(os.Stderr, "chain-eval index: schemas %s\n", schemaDir)
	}

	// Scan artifacts.
	records, err := chainindex.Scan(docsRoot, time.Now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "chain-eval index: scan failed: %v\n", err)
		return 1
	}

	// Build chain index. One clock read covers indexed_at and gate satisfied_at.
	now := time.Now().UTC()
	idx := chainindex.Build(records, docsRoot, cfg.includeLegacy, chainindex.WithNow(now))
	idx.IndexedAt = now.Format(time.RFC3339)

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
	if !cfg.quiet {
		satisfied, unsatisfied := countGateStatuses(idx.GateRecords)
		fmt.Fprintf(os.Stderr, "chain-eval index: gate records: %d satisfied, %d unsatisfied\n", satisfied, unsatisfied)
	}
	return 0
}

// countGateStatuses summarizes emitted gate records for the build report (F6 §9).
func countGateStatuses(records []chainindex.ChainGateRecord) (satisfied, unsatisfied int) {
	for _, r := range records {
		if r.Status == chainindex.GateStatusSatisfied {
			satisfied++
			continue
		}
		unsatisfied++
	}
	return satisfied, unsatisfied
}

// resolveSchemaDir returns the first candidate directory that actually contains
// chain_gate.cue, plus whether such a directory was found.
//
// The previous default was {docs-root}/core/schemas unconditionally. On a
// machine where the docs repo carries no embedded core/ copy, that path does not
// exist, so validation degraded to schema_not_found without saying so - the same
// silent-skip shape as the F6 regression (EPIC-266 release checklist section 3).
// An explicit --schema-dir is always honored verbatim so an operator can point at
// a schema set on purpose and get a real error if it is wrong.
func resolveSchemaDir(flag, docsRoot string) (string, bool) {
	if flag != "" {
		return flag, hasGateSchema(flag)
	}
	candidates := schemaDirCandidates(flag, docsRoot)
	for _, c := range candidates {
		if hasGateSchema(c) {
			return c, true
		}
	}
	// Nothing resolved: keep the historical default so behavior stays fail-open.
	return filepath.Join(docsRoot, "core/schemas"), false
}

// schemaDirCandidates lists schema dirs in priority order.
func schemaDirCandidates(flag, docsRoot string) []string {
	if flag != "" {
		return []string{flag}
	}
	candidates := []string{}
	if env := os.Getenv("CHAIN_SCHEMA_DIR"); env != "" {
		candidates = append(candidates, env)
	}
	if core := os.Getenv("WS_ORG_CORE"); core != "" {
		candidates = append(candidates, filepath.Join(core, "schemas", "cue"))
	}
	candidates = append(
		candidates,
		filepath.Join(docsRoot, "core", "schemas", "cue"),
		filepath.Join(docsRoot, "core", "schemas"),
	)
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "core", "schemas", "cue"))
	}
	return candidates
}

func hasGateSchema(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "chain_gate.cue"))
	return err == nil
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
