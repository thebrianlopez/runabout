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
//     scorer that just re-reads the golden — proves the harness loads cleanly)
//   - Self-test: `eval run` against captured fixtures must report zero failures
//
// M2 will register a real `triageScorer` implementation in cmd_triage.go.

import (
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
	// Stable identifier — derived from workspace slug at capture time.
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
	// Raw triage markdown — preserved so prompt-format regressions are
	// debuggable even if the score happens to land within tolerance.
	RawMarkdown string `json:"raw_markdown"`
}

// Scorer is the pluggable contract M2 will satisfy with the real Go triage
// path. In M1 we ship `identityScorer` which just echoes the golden — that
// proves the harness loads, parses, and computes deltas correctly.
type Scorer interface {
	Score(fixture Fixture) (Golden, error)
	Name() string
}

// identityScorer re-reads the golden field. Used for harness self-test in M1
// and as a sanity baseline ("the harness itself never reports a delta against
// goldens it just read").
type identityScorer struct{}

func (identityScorer) Name() string                           { return "identity" }
func (identityScorer) Score(f Fixture) (Golden, error)        { return f.Golden, nil }

// --- Subcommand wiring ---

func evalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Triage eval harness — capture fixtures and replay them (EPIC-043 M1)",
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
  3. ~/.config/linkari/fixtures   (canonical user corpus — auto-created on capture)
  4. ./testdata/triage            (final fallback for clean checkouts / CI seed corpus)`,
	}
	cmd.AddCommand(evalCaptureCmd())
	cmd.AddCommand(evalRunCmd())
	return cmd
}

func evalCaptureCmd() *cobra.Command {
	var (
		workspace string
		outDir    string
		profile   string
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
			if err := os.MkdirAll(outDir, 0755); err != nil {
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
			if len(fixtures) == 0 {
				return fmt.Errorf("no fixtures in %s — capture some first", fixturesDir)
			}

			scorer := registeredScorer()
			fmt.Fprintf(os.Stderr, "eval: scorer=%s tolerance=±%d fixtures=%d\n",
				scorer.Name(), tolerance, len(fixtures))

			var failures int
			for _, fix := range fixtures {
				got, err := scorer.Score(fix)
				if err != nil {
					fmt.Printf("FAIL %s: scorer error: %v\n", fix.ID, err)
					failures++
					continue
				}
				delta := got.Score - fix.Golden.Score
				if delta < 0 {
					delta = -delta
				}
				if delta > tolerance {
					fmt.Printf("FAIL %s: score=%d golden=%d delta=±%d (tolerance=±%d)\n",
						fix.ID, got.Score, fix.Golden.Score, delta, tolerance)
					failures++
					continue
				}
				if verbose {
					fmt.Printf("OK   %s: score=%d golden=%d delta=±%d\n",
						fix.ID, got.Score, fix.Golden.Score, delta)
				}
			}

			fmt.Fprintf(os.Stderr, "eval: %d/%d passed (%d failed)\n",
				len(fixtures)-failures, len(fixtures), failures)
			if failures > 0 {
				return fmt.Errorf("%d fixture(s) failed", failures)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fixturesDir, "fixtures", "", "fixtures directory (default: $LINKARI_EVAL_FIXTURES, then ~/.config/linkari/fixtures, then ./testdata/triage)")
	cmd.Flags().IntVar(&tolerance, "tolerance", 5, "max absolute score delta before a fixture fails")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "print every fixture result, not just failures")
	return cmd
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
// Preferred source: `_haiku_input.txt` — fish dumps this verbatim immediately
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
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var f Fixture
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
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
// Same precedence but never falls back to testdata silently — capture should
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

// registeredScorer returns the active Scorer. EPIC-043 M2 swapped the
// identity scorer for the real Go triage path (cmd_triage.go). identityScorer
// is still exercised directly by the harness self-test in cmd_eval_test.go.
func registeredScorer() Scorer {
	return triageScorer{}
}

// scoreLineRE extracts the score from one of two forms:
//
//	## Score: NN/100               (normal triage output)
//	**Score: NN/100 — Skip ...**   (noise-gate output from the profile templates)
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
