package main

// EPIC-043 M1: Eval harness for triage scoring.
//
// The eval harness has two modes:
//
//   linkari eval capture  --workspace <ws> --profile <p> --out <fixtures-dir>
//       Run the *current fish path* (_uinit_profile_prompt) against a workspace,
//       then snapshot the inputs (url, profile, content) and outputs
//       (score, verdict, raw triage markdown) into a fixture JSON. These
//       fixtures become the goldens that M2's Go port must match.
//
//   linkari eval run --fixtures <dir> [--tolerance N]
//       Replay every fixture against whatever scorer is wired in (initially:
//       a stub that re-reads the golden and reports zero delta; in M2 it
//       gets swapped for the real Go triage path). Exits non-zero if any
//       fixture's score deviates by more than `tolerance` (default 5).
//
// In M1 we land:
//   - Fixture struct + JSON (de)serialization
//   - `capture` subcommand that snapshots an existing scored workspace
//     (reads README.md + _score.json that fish already produced)
//   - `run` subcommand with a pluggable Scorer interface (default: identity
//     scorer that just re-reads the golden  -  proves the harness loads cleanly)
//   - Self-test: `eval run` against captured fixtures must report zero failures
//
// M2 will register a real `triageScorer` implementation in cmd_triage.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Fixture is the on-disk representation of one triage eval case.
//
// Inputs are everything the scorer needs to reproduce the call. Goldens are
// what the *current* (fish) path produced when the fixture was captured.
type Fixture struct {
	// Stable identifier  -  derived from workspace slug at capture time.
	ID string `json:"id"`

	// Capture metadata (informational; not asserted).
	CapturedAt string `json:"captured_at"`
	Source     string `json:"source"` // "fish" in M1; "linkari-triage" once M2 lands.

	// --- Inputs (must be sufficient to re-run the scorer) ---
	URL     string `json:"url"`
	Profile string `json:"profile"`
	// Content is the workspace text the scorer sees (first 2000 chars to match
	// the fish truncation in _uinit_profile_prompt.fish).
	Content string `json:"content"`

	// --- Goldens (what fish produced for this input) ---
	Golden Golden `json:"golden"`
}

// Golden is the captured output of one fixture run.
type Golden struct {
	Score   int    `json:"score"`
	Verdict string `json:"verdict"`
	// Raw triage markdown  -  preserved so prompt-format regressions are
	// debuggable even if the score happens to land within tolerance.
	RawMarkdown string `json:"raw_markdown"`
	// EPIC-058 M8: extended golden fields for regression coverage.
	Gaps          []string `json:"gaps,omitempty"`
	SourceType    string   `json:"source_type,omitempty"`
	PromptVersion string   `json:"prompt_version,omitempty"`
	PromptHash    string   `json:"prompt_hash,omitempty"` // EPIC-082 M4: sha256 prefix of rendered prompt
	// Skip signals that the scorer could not produce a comparable score
	// this run (parse failure after repair, malformed verdict, noise-gate
	// hit). The eval runner treats Skip as neither pass nor fail  -  it is
	// reported separately and does not gate the run. Never persisted.
	Skip bool `json:"-"`
	// SkipReason is a short human-readable tag ("parse_failed",
	// "noise_gate", "scorer_error") used in the SKIP log line.
	SkipReason string `json:"-"`
	// RefreshedFrom is the prior score before a `linkari eval refresh-goldens`
	// rewrite (EPIC-044 M2). Audit trail so future drift can be measured
	// against the score the previous manifest produced. Pointer to keep
	// older fixtures byte-stable (omitempty when unset).
	RefreshedFrom *int `json:"refreshed_from,omitempty"`
}

// Scorer is the pluggable contract M2 will satisfy with the real Go triage
// path. In M1 we ship `identityScorer` which just echoes the golden  -  that
// proves the harness loads, parses, and computes deltas correctly.
type Scorer interface {
	Score(fixture Fixture) (Golden, error)
	Name() string
}

// identityScorer re-reads the golden field. Used for harness self-test in M1
// and as a sanity baseline ("the harness itself never reports a delta against
// goldens it just read").
type identityScorer struct{}

func (identityScorer) Name() string                    { return "identity" }
func (identityScorer) Score(f Fixture) (Golden, error) { return f.Golden, nil }

// --- Subcommand wiring ---

func evalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Triage eval harness  -  capture fixtures and replay them (EPIC-043 M1)",
		Long: `Triage eval harness for the Linkari scoring pipeline.

Two modes:

  linkari eval capture --workspace <dir> [--out <fixtures-dir>]
      Snapshot a workspace that has already been triaged by the fish path
      (README.md + _score.json present) into a fixture JSON file.

  linkari eval run --fixtures <dir> [--tolerance 5]
      Replay every fixture in <dir> against the registered scorer and exit
      non-zero on any failure.

Default fixtures directory (both subcommands), in priority order:
  1. explicit --out / --fixtures flag
  2. $LINKARI_EVAL_FIXTURES env var
  3. ~/.config/linkari/fixtures   (canonical user corpus  -  auto-created on capture)
  4. ./testdata/triage            (final fallback for clean checkouts / CI seed corpus)`,
	}
	cmd.AddCommand(evalCaptureCmd())
	cmd.AddCommand(evalRunCmd())
	cmd.AddCommand(evalRefreshGoldensCmd())
	cmd.AddCommand(evalStatsCmd())
	return cmd
}

// evalStatsCmd implements `linkari eval stats --fixtures <dir> [--min-fixtures N] [--json]`
func evalStatsCmd() *cobra.Command {
	var (
		fixturesDir string
		minFixtures int
		jsonOutput  bool
	)
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Report fixture corpus coverage per profile (EPIC-112 F3)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if fixturesDir == "" {
				fixturesDir = defaultFixturesDir()
			}
			result, err := RunEvalStats(fixturesDir)
			if err != nil {
				return err
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			// Human table output
			fmt.Fprintf(cmd.OutOrStdout(), "%-12s %6s %s\n", "Profile", "Count", "Status")
			fmt.Fprintf(cmd.OutOrStdout(), "%-12s %6s %s\n", "-------", "-----", "------")
			for _, profile := range sortedProfileIDs() {
				count := result.Profiles[profile]
				status := "✓"
				if count < minFixtures {
					status = "✗"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-12s %6d %s\n", profile, count, status)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\nTotal: %d fixtures across %d profiles\n",
				result.Total, len(result.Profiles))
			if len(result.Missing) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Missing: %v\n", result.Missing)
			}

			if minFixtures > 0 {
				for profile, count := range result.Profiles {
					if count < minFixtures {
						return fmt.Errorf("profile %q has %d fixtures, below --min-fixtures=%d", profile, count, minFixtures)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fixturesDir, "fixtures", "", "fixtures directory (default: testdata/triage)")
	cmd.Flags().IntVar(&minFixtures, "min-fixtures", 0, "exit 1 if any profile has fewer than N fixtures")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output valid JSON (StatsResult schema)")
	return cmd
}

// refreshScorerFn is the indirection point tests stub for
// `linkari eval refresh-goldens`. Production path loads the profile
// manifest and calls the JSON Haiku contract via Evaluator (EPIC-058 M2).
// Tests swap in a deterministic fake.
var refreshScorerFn = func(ctx context.Context, profile, content string) (*Scorecard, error) {
	tmplPath, sysPrompt, err := loadProfileTemplateJSON(profile)
	if err != nil {
		return nil, fmt.Errorf("load template: %w", err)
	}
	eval := HaikuJSONEvaluator{}
	sc, err := eval.Evaluate(ctx, truncateRunes(content, contentTruncationRunes), sysPrompt)
	if err != nil {
		return nil, err
	}
	sc.SourceType = "eval-refresh"
	// EPIC-082 M4: populate prompt traceability for refresh-goldens.
	sc.PromptVersion = promptVersionFromPath(tmplPath)
	sc.PromptHash = promptHash(sysPrompt)
	return sc, nil
}

func evalRefreshGoldensCmd() *cobra.Command {
	var (
		fixturesDir string
		profileFlag string
		dryRun      bool
		yes         bool
	)
	cmd := &cobra.Command{
		Use:   "refresh-goldens",
		Short: "Re-score fixtures via Haiku and rewrite golden.score/verdict/raw_markdown in place (EPIC-044 M2)",
		Long: `Refresh the golden outputs on every fixture in --fixtures by re-running
the current profile manifest through Haiku (JSON contract). Used after a
profile manifest change makes the existing goldens stale.

DESTRUCTIVE: rewriting goldens nukes the regression baseline. Without
--yes the command prints a summary of old→new score deltas and asks for
confirmation. Use --dry-run to preview without writing.

Each refreshed fixture preserves id/captured_at(source)/url/profile/content
unchanged; only golden.{score,verdict,raw_markdown} are rewritten and
golden.refreshed_from is set to the prior score for audit. captured_at
is bumped to the refresh timestamp.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fixturesDir == "" {
				fixturesDir = defaultFixturesDir()
			}
			fixtures, err := loadFixtures(fixturesDir)
			if err != nil {
				return err
			}
			if profileFlag != "" {
				fixtures = filterFixturesByProfile(fixtures, profileFlag)
			}
			if len(fixtures) == 0 {
				return fmt.Errorf("no fixtures in %s (profile=%q)", fixturesDir, profileFlag)
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			type pending struct {
				path      string
				prior     int
				fixture   Fixture
				scorecard *Scorecard
			}
			var refreshed []pending
			for _, fix := range fixtures {
				sc, err := refreshScorerFn(ctx, fix.Profile, fix.Content)
				if err != nil {
					return fmt.Errorf("rescore %s: %w", fix.ID, err)
				}
				refreshed = append(refreshed, pending{
					path:      filepath.Join(fixturesDir, fix.ID+".json"),
					prior:     fix.Golden.Score,
					fixture:   fix,
					scorecard: sc,
				})
			}

			fmt.Fprintf(os.Stderr, "refresh-goldens: %d fixture(s) in %s\n", len(refreshed), fixturesDir)
			for _, p := range refreshed {
				delta := p.scorecard.Score - p.prior
				fmt.Fprintf(os.Stderr, "  %s  %d → %d  (Δ%+d)\n", p.fixture.ID, p.prior, p.scorecard.Score, delta)
			}

			if dryRun {
				fmt.Fprintln(os.Stderr, "refresh-goldens: --dry-run, not writing")
				return nil
			}
			if !yes {
				fmt.Fprint(os.Stderr, "\nrewrite goldens? [y/N]: ")
				var resp string
				fmt.Fscanln(os.Stdin, &resp)
				resp = strings.TrimSpace(strings.ToLower(resp))
				if resp != "y" && resp != "yes" {
					return fmt.Errorf("aborted")
				}
			}

			now := time.Now().UTC().Format(time.RFC3339)
			for _, p := range refreshed {
				prior := p.prior
				out := p.fixture
				out.CapturedAt = now
				out.Golden = Golden{
					Score:         p.scorecard.Score,
					Verdict:       p.scorecard.Verdict,
					RawMarkdown:   p.scorecard.RawMarkdown,
					Gaps:          p.scorecard.Gaps,          // EPIC-082 M4
					SourceType:    "eval-refresh",            // EPIC-082 M4
					PromptVersion: p.scorecard.PromptVersion, // EPIC-082 M4
					PromptHash:    p.scorecard.PromptHash,    // EPIC-082 M4
					RefreshedFrom: &prior,
				}
				f, err := os.Create(p.path)
				if err != nil {
					return fmt.Errorf("create %s: %w", p.path, err)
				}
				enc := json.NewEncoder(f)
				enc.SetIndent("", "  ")
				if err := enc.Encode(out); err != nil {
					f.Close()
					return fmt.Errorf("encode %s: %w", p.path, err)
				}
				f.Close()
			}
			fmt.Fprintf(os.Stderr, "refresh-goldens: rewrote %d fixture(s)\n", len(refreshed))
			return nil
		},
	}
	cmd.Flags().StringVar(&fixturesDir, "fixtures", "", "fixtures directory (default: $LINKARI_EVAL_FIXTURES, then ~/.config/linkari/fixtures, then ./testdata/triage)")
	cmd.Flags().StringVar(&profileFlag, "profile", "", "only refresh fixtures matching this profile id")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print old→new deltas, do not write")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip interactive confirmation (destructive  -  refreshes the regression baseline)")
	return cmd
}

func evalCaptureCmd() *cobra.Command {
	var (
		workspace   string
		outDir      string
		profile     string
		urlOverride string
	)
	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Snapshot a fish-scored workspace into a fixture",
		RunE: func(cmd *cobra.Command, args []string) error {
			if workspace == "" {
				return fmt.Errorf("--workspace required")
			}
			if outDir == "" {
				outDir = defaultFixturesDirForWrite()
			}
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return fmt.Errorf("mkdir fixtures: %w", err)
			}

			fix, err := captureFromWorkspace(workspace, profile, urlOverride)
			if err != nil {
				return err
			}

			outPath := filepath.Join(outDir, fix.ID+".json")
			f, err := os.Create(outPath)
			if err != nil {
				return fmt.Errorf("create fixture: %w", err)
			}
			defer f.Close()
			enc := json.NewEncoder(f)
			enc.SetIndent("", "  ")
			if err := enc.Encode(fix); err != nil {
				return fmt.Errorf("encode fixture: %w", err)
			}
			fmt.Fprintf(os.Stderr, "captured fixture %s → %s (score=%d profile=%s)\n",
				fix.ID, outPath, fix.Golden.Score, fix.Profile)
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "path to a fish-scored workspace (must contain README.md and _score.json)")
	cmd.Flags().StringVar(&outDir, "out", "", "fixtures output directory (default: $LINKARI_EVAL_FIXTURES or ~/.config/linkari/fixtures)")
	cmd.Flags().StringVar(&profile, "profile", "", "override profile (default: read from _score.json)")
	cmd.Flags().StringVar(&urlOverride, "url", "", "override URL (default: read from _score.json)")
	return cmd
}

func evalRunCmd() *cobra.Command {
	var (
		fixturesDir string
		tolerance   int
		verbose     bool
		profileFlag string
		changedOnly bool
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Replay fixtures against the registered scorer",
		RunE: func(cmd *cobra.Command, args []string) error {
			if fixturesDir == "" {
				fixturesDir = defaultFixturesDir()
			}
			fixtures, err := loadFixtures(fixturesDir)
			if err != nil {
				return err
			}
			// EPIC-044 M3: --profile / --changed-only narrow the corpus
			// for the pre-commit hook path so per-profile gates stay <30s.
			if profileFlag != "" || changedOnly {
				wantProfile := profileFlag
				if wantProfile == "" && changedOnly {
					return fmt.Errorf("--changed-only requires --profile")
				}
				fixtures = filterFixturesByProfile(fixtures, wantProfile)
			}
			if len(fixtures) == 0 {
				return fmt.Errorf("no fixtures in %s  -  capture some first", fixturesDir)
			}

			scorer := registeredScorerFn()
			fmt.Fprintf(os.Stderr, "eval: scorer=%s tolerance=±%d fixtures=%d\n",
				scorer.Name(), tolerance, len(fixtures))

			var failures, skips, stales int
			for _, fix := range fixtures {
				got, err := scorer.Score(fix)
				if err != nil {
					// M6b: scorer errors degrade to skip instead of FAIL
					// so a single malformed response can't redline the
					// whole run. The error is surfaced in the SKIP line.
					fmt.Printf("SKIP %s: scorer_error: %v\n", fix.ID, err)
					skips++
					continue
				}
				if got.Skip {
					reason := got.SkipReason
					if reason == "" {
						reason = "unknown"
					}
					fmt.Printf("SKIP %s: %s\n", fix.ID, reason)
					skips++
					continue
				}
				delta := got.Score - fix.Golden.Score
				if delta < 0 {
					delta = -delta
				}
				// EPIC-082 M4: STALE  -  prompt version changed since golden was captured.
				// Non-gating: logged but does not increment failures.
				if delta > tolerance && fix.Golden.PromptVersion != "" && got.PromptVersion != fix.Golden.PromptVersion {
					fmt.Printf("STALE %s: score=%d golden=%d delta=±%d (prompt changed: %s→%s)\n",
						fix.ID, got.Score, fix.Golden.Score, delta,
						fix.Golden.PromptVersion[:min(8, len(fix.Golden.PromptVersion))],
						got.PromptVersion[:min(8, len(got.PromptVersion))])
					stales++
					continue
				}
				if delta > tolerance {
					fmt.Printf("FAIL %s: score=%d golden=%d delta=±%d (tolerance=±%d)\n",
						fix.ID, got.Score, fix.Golden.Score, delta, tolerance)
					failures++
					continue
				}
				// EPIC-058 M8: shape warnings for scorecard completeness.
				if len(got.Gaps) == 0 && got.Score > 0 && !got.Skip {
					fmt.Printf("WARN %s: no gaps in scorecard (score=%d)\n", fix.ID, got.Score)
				}
				if verbose {
					fmt.Printf("OK   %s: score=%d golden=%d delta=±%d\n",
						fix.ID, got.Score, fix.Golden.Score, delta)
				}
			}

			fmt.Fprintf(os.Stderr, "eval: %d/%d passed (%d failed, %d skipped, %d stale)\n",
				len(fixtures)-failures-skips-stales, len(fixtures), failures, skips, stales)
			if failures > 0 {
				return fmt.Errorf("%d fixture(s) failed", failures)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fixturesDir, "fixtures", "", "fixtures directory (default: $LINKARI_EVAL_FIXTURES, then ~/.config/linkari/fixtures, then ./testdata/triage)")
	cmd.Flags().IntVar(&tolerance, "tolerance", 10, "max absolute score delta before a fixture fails (M6b: bumped from 5 to absorb normal Haiku-as-judge variance)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "print every fixture result, not just failures")
	cmd.Flags().StringVar(&profileFlag, "profile", "", "EPIC-044 M3: only run fixtures matching this profile id")
	cmd.Flags().BoolVar(&changedOnly, "changed-only", false, "EPIC-044 M3: only run fixtures for --profile (pre-commit hook path)")
	return cmd
}

// filterFixturesByProfile returns the subset of fixtures whose Profile
// matches the given id. EPIC-044 M3  -  used by `--profile` and
// `--changed-only` to scope the eval gate per profile.
func filterFixturesByProfile(fixtures []Fixture, profileID string) []Fixture {
	if profileID == "" {
		return fixtures
	}
	out := fixtures[:0:0]
	for _, f := range fixtures {
		if f.Profile == profileID {
			out = append(out, f)
		}
	}
	return out
}

// --- Capture: read fish-produced workspace into a Fixture ---

func captureFromWorkspace(workspace, profileOverride, urlOverride string) (Fixture, error) {
	scoreJSON := filepath.Join(workspace, "_score.json")
	scoreBytes, err := os.ReadFile(scoreJSON)
	if err != nil {
		return Fixture{}, fmt.Errorf("read _score.json: %w (workspace must be already-triaged)", err)
	}
	var sc struct {
		Score    int    `json:"score"`
		Verdict  string `json:"verdict"`
		Slug     string `json:"slug"`
		Profile  string `json:"profile"`
		URL      string `json:"url"`
		ScoredAt string `json:"scored_at"`
	}
	if err := json.Unmarshal(scoreBytes, &sc); err != nil {
		return Fixture{}, fmt.Errorf("parse _score.json: %w", err)
	}

	readme, err := os.ReadFile(filepath.Join(workspace, "README.md"))
	if err != nil {
		return Fixture{}, fmt.Errorf("read README.md: %w", err)
	}
	rawMarkdown := extractTriageBlock(string(readme))

	// The "content" the scorer saw is what fish handed to Haiku.
	// Prefer the byte-exact `_haiku_input.txt` dump if present (system+user
	// bytes, no truncation). Older workspaces fall back to the workspace
	// content files; in that case we replicate fish's 2000-char truncation
	// from _uinit_profile_prompt.fish so the recorded `content` matches
	// what Haiku actually saw.
	exactInput := false
	if b, err := os.ReadFile(filepath.Join(workspace, "_haiku_input.txt")); err == nil && len(b) > 0 {
		exactInput = true
		_ = b
	}
	content, err := readScoredContent(workspace, string(readme))
	if err != nil {
		return Fixture{}, err
	}
	if !exactInput && len(content) > 2000 {
		content = content[:2000]
	}

	profile := sc.Profile
	if profileOverride != "" {
		profile = profileOverride
	}
	url := sc.URL
	if urlOverride != "" {
		url = urlOverride
	}

	id := sc.Slug
	if id == "" {
		id = filepath.Base(workspace)
	}

	source := "fish-reconstructed"
	if exactInput {
		source = "fish-exact"
	}

	return Fixture{
		ID:         id,
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		Source:     source,
		URL:        url,
		Profile:    profile,
		Content:    content,
		Golden: Golden{
			Score:       sc.Score,
			Verdict:     sc.Verdict,
			RawMarkdown: rawMarkdown,
		},
	}, nil
}

// readScoredContent returns the content the fish scorer piped into Haiku.
//
// Preferred source: `_haiku_input.txt`  -  fish dumps this verbatim immediately
// before the Haiku call (added by fish-config-agent 2026-04-06 for EPIC-043
// M1). Returning these bytes gives M2 byte-exact parity.
//
// Fallback chain (older workspaces that pre-date the fish patch):
// content.md → page.md → index.md → article.md → README body above the
// first '---' triage separator.
func readScoredContent(workspace, readme string) (string, error) {
	if b, err := os.ReadFile(filepath.Join(workspace, "_haiku_input.txt")); err == nil && len(b) > 0 {
		return string(b), nil
	}
	candidates := []string{"content.md", "page.md", "index.md", "article.md"}
	for _, name := range candidates {
		b, err := os.ReadFile(filepath.Join(workspace, name))
		if err == nil && len(b) > 0 {
			return string(b), nil
		}
	}
	// Fallback: README body above the first '---' triage separator.
	if idx := strings.Index(readme, "\n---\n"); idx > 0 {
		return readme[:idx], nil
	}
	return readme, nil
}

// extractTriageBlock returns the markdown after the last `\n---\n` separator
// in README.md, which is where _uinit_profile_prompt appends the triage.
func extractTriageBlock(readme string) string {
	idx := strings.LastIndex(readme, "\n---\n")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(readme[idx+len("\n---\n"):])
}

// --- Replay: load fixtures from a directory ---

func loadFixtures(dir string) ([]Fixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read fixtures dir %s: %w", dir, err)
	}
	var out []Fixture
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := e.Name()
		// M6b: skip dotfiles / hidden entries and decoy non-fixture files
		// that happen to end in .json.
		if strings.HasPrefix(name, ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		var f Fixture
		if err := json.Unmarshal(b, &f); err != nil {
			// M6b: don't hard-fail on one bad file  -  log and skip.
			fmt.Fprintf(os.Stderr, "eval: skip %s: parse: %v\n", name, err)
			continue
		}
		// M6b: skip fixtures with invalid IDs (`.`, `..`, empty, or path
		// separators). These get captured when `eval capture` runs from
		// the wrong cwd and falls back to filepath.Base(".").
		if !isValidFixtureID(f.ID) {
			fmt.Fprintf(os.Stderr, "eval: skip %s: invalid fixture id %q\n", name, f.ID)
			continue
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// isValidFixtureID rejects IDs that would corrupt eval output or indicate
// a capture-from-wrong-cwd bug. A valid id is a non-empty slug with no
// path separators and no standalone `.` / `..` entries.
func isValidFixtureID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, "/\\") {
		return false
	}
	return true
}

// defaultFixturesDir is the read-side resolver for `eval run`. Order:
// $LINKARI_EVAL_FIXTURES > ~/.config/linkari/fixtures (if it exists) >
// ./testdata/triage (final fallback for clean checkouts).
func defaultFixturesDir() string {
	if v := os.Getenv("LINKARI_EVAL_FIXTURES"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".config", "linkari", "fixtures")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return filepath.Join("testdata", "triage")
}

// defaultFixturesDirForWrite is the write-side resolver for `eval capture`.
// Same precedence but never falls back to testdata silently  -  capture should
// always land in the canonical user dir unless explicitly overridden.
func defaultFixturesDirForWrite() string {
	if v := os.Getenv("LINKARI_EVAL_FIXTURES"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "linkari", "fixtures")
	}
	return filepath.Join("testdata", "triage")
}

// registeredScorerFn returns the active Scorer. EPIC-043 M2 swapped the
// identity scorer for the real Go triage path (cmd_triage.go). identityScorer
// is still exercised directly by the harness self-test in cmd_eval_test.go.
// Indirection (var not func) lets cmd_eval_test.go stub it with a fake.
var registeredScorerFn = func() Scorer {
	return triageScorer{}
}

// scoreLineRE extracts the score from one of two forms:
//
//	## Score: NN/100               (normal triage output)
//	**Score: NN/100  -  Skip ...**   (noise-gate output from the profile templates)
//
// Both forms appear in the M1 fixture corpus and in live Haiku responses,
// so the parser must accept either. Exposed (lowercase but package-internal)
// for reuse by cmd_triage.go.
var scoreLineRE = regexp.MustCompile(`(?m)(?:^##\s*Score:|\*\*Score:)\s*(\d+)\s*/\s*100`)

func parseScoreFromMarkdown(md string) (int, error) {
	m := scoreLineRE.FindStringSubmatch(md)
	if len(m) < 2 {
		return 0, fmt.Errorf("no '## Score: N/100' line found")
	}
	return strconv.Atoi(m[1])
}

// validProfileIDs is the canonical list of triage profiles for fixture validation.
var validProfileIDs = map[string]bool{
	"eng":     true,
	"life":    true,
	"travel":  true,
	"fashion": true,
	"music":   true,
	"finance": true,
	"dining":  true,
}

// LoadFixtures reads all golden fixture JSON files from dir.
// Skips malformed files silently (does not fail on first error).
// Returns an error only if dir has no valid fixture JSON files.
func LoadFixtures(dir string) ([]Fixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read fixtures dir %s: %w", dir, err)
	}
	var out []Fixture
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var f Fixture
		if err := json.Unmarshal(b, &f); err != nil {
			continue
		}
		if !isValidFixtureID(f.ID) {
			continue
		}
		if err := ValidateFixture(f); err != nil {
			continue
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("corpus_empty: no valid fixtures found  -  run eval capture first")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ValidateFixture checks that a fixture has all required golden fields.
// Per TDD: Required fields in golden: score (non-zero), prompt_version (non-empty).
func ValidateFixture(f Fixture) error {
	if f.Golden.Score == 0 {
		return fmt.Errorf("fixture_missing_golden: fixture %s: golden block missing or invalid (score=0)", f.ID)
	}
	if f.Golden.PromptVersion == "" {
		return fmt.Errorf("fixture_missing_golden: fixture %s: golden block missing or invalid (prompt_version empty)", f.ID)
	}
	if !validProfileIDs[f.Profile] {
		return fmt.Errorf("fixture_unknown_profile: fixture %s: unknown profile %q  -  check testdata/profiles/", f.ID, f.Profile)
	}
	return nil
}
