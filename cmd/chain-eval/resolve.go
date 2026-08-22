package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/thebrianlopez/runabout/cmd/chain-eval/internal/chainindex"
)

// resolveCmd wires V2 (EPIC-268 chain-root sentinel epic): the
// referent-resolves-to-nothing resolver over the already-classified FDD/TDD
// upstream_state population. It is deliberately a separate subcommand from
// `index`, not folded into it: V2 needs a second resolution root
// ($WS_ORG_CORE) that `index` has no reason to know about, and its report
// shape (declared_none_excluded / resolved / unresolved / severed) has no
// analogue in the schema-violation report `index` already prints.
func resolveCmd() *cobra.Command {
	var (
		flagDocsRoot string
		flagCoreRoot string
		flagOutput   string
		flagQuiet    bool
	)

	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Resolve FDD/TDD upstream referents against disk (V2, EPIC-268)",
		Long: `Scans docs/ for FDD/TDD artifacts and, for every one whose upstream_field is
non-empty and whose upstream_state is "extracted" (declared_none/NO-UPSTREAM
records are excluded before resolution, not resolved), normalizes and resolves
the referent against the docs root and, when set, the core schema root.

Reports resolved, unresolved, severed (resolves under an archive/ path) and
declared_none_excluded counts. Report-only: exit code reflects run success,
never resolution outcome (ADVISORY-ONLY ruling, EPIC-268).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := resolveRunConfig{
				docsRoot: flagDocsRoot,
				coreRoot: flagCoreRoot,
				output:   flagOutput,
				quiet:    flagQuiet,
			}
			os.Exit(runResolve(cmd, cfg))
			return nil
		},
	}

	cmd.Flags().StringVar(&flagDocsRoot, "docs-root", "", "Absolute path to docs root (overrides $CHAIN_DOCS_ROOT)")
	cmd.Flags().StringVar(&flagCoreRoot, "core-root", "", "Absolute path to the core/ resolution root (overrides $WS_ORG_CORE; empty disables the second root)")
	cmd.Flags().StringVar(&flagOutput, "output", "", "Optional path to write the full per-record resolution report as JSON")
	cmd.Flags().BoolVar(&flagQuiet, "quiet", false, "Suppress non-fatal warnings")

	return cmd
}

type resolveRunConfig struct {
	docsRoot string
	coreRoot string
	output   string
	quiet    bool
}

// resolveOutput is the optional --output JSON shape: the full per-record
// results plus the summary report, so a reviewer does not have to re-derive
// which records fell into which bucket from stderr alone.
type resolveOutput struct {
	GeneratedAt string                          `json:"generated_at"`
	Report      chainindex.ResolutionReport     `json:"report"`
	Results     []chainindex.UpstreamResolution `json:"results"`
}

func runResolve(_ *cobra.Command, cfg resolveRunConfig) int {
	docsRoot := resolveDocsRoot(cfg.docsRoot)
	if _, err := os.Stat(docsRoot); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "chain-eval resolve: docs root not found at %s\n", docsRoot)
		return 1
	}

	coreRoot := resolveCoreRoot(cfg.coreRoot)
	if coreRoot == "" && !cfg.quiet {
		fmt.Fprintln(os.Stderr, "chain-eval resolve: WARN: no core root resolved (set --core-root or $WS_ORG_CORE) - resolving against docs root only")
	}

	fmt.Fprintf(os.Stderr, "chain-eval resolve: scanning %s\n", docsRoot)
	if coreRoot != "" && !cfg.quiet {
		fmt.Fprintf(os.Stderr, "chain-eval resolve: second root %s\n", coreRoot)
	}

	records, err := chainindex.Scan(docsRoot, time.Now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "chain-eval resolve: scan failed: %v\n", err)
		return 1
	}

	results, report := chainindex.ResolveUpstreamReferents(records, docsRoot, coreRoot)

	if emitErr := chainindex.EmitUpstreamReferentEvents(results, "chain-eval resolve"); emitErr != nil && !cfg.quiet {
		fmt.Fprintf(os.Stderr, "chain-eval resolve: WARN: event emission failed: %v\n", emitErr)
	}

	fmt.Fprintf(os.Stderr,
		"chain-eval resolve: %d resolved, %d unresolved, %d severed (archive), %d declared_none excluded\n",
		report.Resolved, report.Unresolved, report.Severed, report.DeclaredNoneExcluded)

	if !cfg.quiet {
		for _, r := range results {
			if r.Outcome == chainindex.ResolutionResolved {
				continue
			}
			fmt.Fprintf(os.Stderr, "chain-eval resolve: %s: %s referent %q (normalized %q)\n",
				r.Outcome, r.ArtifactPath, r.UpstreamField, r.Normalized)
		}
	}

	if cfg.output != "" {
		out := resolveOutput{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Report:      report,
			Results:     results,
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "chain-eval resolve: marshal failed: %v\n", err)
			return 1
		}
		if err := os.MkdirAll(filepath.Dir(cfg.output), 0o755); err != nil && filepath.Dir(cfg.output) != "." {
			fmt.Fprintf(os.Stderr, "chain-eval resolve: failed to create output dir: %v\n", err)
			return 1
		}
		if err := os.WriteFile(cfg.output, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "chain-eval resolve: failed to write %s: %v\n", cfg.output, err)
			return 1
		}
	}

	return 0
}

// resolveCoreRoot mirrors resolveDocsRoot's priority order for V2's second
// resolution root: --core-root flag, then $WS_ORG_CORE, then ~/core as the
// documented Alpine/Termux default topology fallback (see resolvePromptsDir's
// same fallback for precedent). Returns "" when nothing resolves - the second
// root is optional, unlike docs root.
func resolveCoreRoot(flag string) string {
	if flag != "" {
		return flag
	}
	if env := os.Getenv("WS_ORG_CORE"); env != "" {
		return env
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, "core")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
	}
	return ""
}
