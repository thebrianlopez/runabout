package main

// EPIC-044 M3 — Layer 3: `linkari profile lint`.
//
// Validates one or more profile YAML manifests against the v1 schema:
//
//   - rubric weights sum to 100 (5 axes)
//   - schema_version == triage_verdict_v1
//   - referenced fixtures directory exists (best-effort warning, not fatal)
//   - {{ outside the persona_body raw block (would be re-templated)
//   - fixtures-per-profile cap (warns when > 8 to keep pre-commit < 30s)
//
// Wired into the pre-commit hook at ~/code/personal/docs/.git/hooks/pre-commit
// by personal-docs-agent (M3, their share). The hook calls:
//
//     linkari profile lint docs/prompts/profiles/*.yaml
//
// and bails the commit on any non-zero exit.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const (
	fixtureCapPerProfile = 8
	fixtureWarnThreshold = 5
)

func profileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Profile manifest tooling (EPIC-044 M3)",
	}
	cmd.AddCommand(profileLintCmd())
	cmd.AddCommand(profileTestCmd())
	return cmd
}

// profileTestCmd implements `linkari profile test <profile.yaml> [--fixtures <dir>] [--tolerance N]`
func profileTestCmd() *cobra.Command {
	var (
		fixturesDir string
		tolerance   int
	)
	cmd := &cobra.Command{
		Use:   "test <profile.yaml>",
		Short: "Compare HEAD vs working-tree profile scoring on fixture corpus (EPIC-112 F4)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profilePath := args[0]
			if fixturesDir == "" {
				fixturesDir = defaultFixturesDir()
			}
			scorer := registeredScorerFn()
			result, err := RunProfileTest(profilePath, fixturesDir, tolerance, scorer)
			if err != nil {
				return err
			}
			if len(result.Fixtures) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "profile test: no fixtures found for profile (check --fixtures path)\n")
				return nil
			}

			// Table output
			fmt.Fprintf(cmd.OutOrStdout(), "%-36s %6s %6s %6s %s\n", "Fixture", "Before", "After", "Delta", "Status")
			fmt.Fprintf(cmd.OutOrStdout(), "%-36s %6s %6s %6s %s\n", "-------", "------", "-----", "-----", "------")
			for _, f := range result.Fixtures {
				deltaStr := fmt.Sprintf("%+d", f.Delta)
				fmt.Fprintf(cmd.OutOrStdout(), "%-36s %6d %6d %6s %s\n",
					f.ID, f.BeforeScore, f.AfterScore, deltaStr, f.Status)
			}

			if result.HasFailure {
				return fmt.Errorf("profile test: one or more fixtures exceed tolerance (%d)", tolerance)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fixturesDir, "fixtures", "", "fixtures directory (default: testdata/triage)")
	cmd.Flags().IntVar(&tolerance, "tolerance", 10, "score delta tolerance threshold")
	return cmd
}

func profileLintCmd() *cobra.Command {
	var (
		fixturesDir string
		strict      bool
		minFixtures int
	)
	cmd := &cobra.Command{
		Use:   "lint [manifest.yaml ...]",
		Short: "Validate profile YAML manifests against the v1 schema",
		Long: `Lint one or more profile YAML manifests.

Checks (fatal):
  - Schema validation (rubric weights sum to 100, all required fields)
  - schema_version == triage_verdict_v1

Checks (warn unless --strict):
  - Fixtures directory exists for the profile
  - Fixture count <= 8 per profile (pre-commit budget cap)
  - persona_body contains no {{ ... }} actions outside literal {{FILL: ...}}
    markers (would be re-templated by text/template).

Exits non-zero on any fatal violation. With --strict, warnings are
also fatal.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fixturesDir == "" {
				fixturesDir = defaultFixturesDir()
			}
			var errs, warns []string
			for _, path := range args {
				m, err := LoadProfileManifest(path)
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", path, err))
					continue
				}
				// Render once to surface text/template errors early.
				if _, rerr := m.Render(); rerr != nil {
					errs = append(errs, fmt.Sprintf("%s: render: %v", path, rerr))
					continue
				}
				// Soft checks.
				for _, line := range strings.Split(m.PersonaBody, "\n") {
					// Allow literal {{FILL: ...}} markers.
					if strings.Contains(line, "{{") && !strings.Contains(line, "{{FILL") {
						warns = append(warns, fmt.Sprintf("%s: persona_body contains %q — text/template would re-process this if not for the raw-render guard", path, strings.TrimSpace(line)))
					}
				}
				if fixturesDir != "" {
					n, ok := countFixturesForProfile(fixturesDir, m.ID)
					if !ok {
						warns = append(warns, fmt.Sprintf("%s: fixtures dir %s missing or unreadable", path, fixturesDir))
					} else {
						if n > fixtureCapPerProfile {
							warns = append(warns, fmt.Sprintf("%s: profile %q has %d fixtures > cap %d (pre-commit hook will exceed 30s budget)", path, m.ID, n, fixtureCapPerProfile))
						}
						if n < fixtureWarnThreshold {
							fmt.Fprintf(os.Stderr, "warn: profile %s has %d fixtures (<%d)\n", m.ID, n, fixtureWarnThreshold)
						}
						if minFixtures > 0 && n < minFixtures {
							errs = append(errs, fmt.Sprintf("%s: profile %q has %d fixtures, below --min-fixtures=%d", path, m.ID, n, minFixtures))
						}
					}
				}
				fmt.Fprintf(os.Stderr, "OK   %s (id=%s rubric=%d axes)\n", path, m.ID, len(m.Rubric))
			}
			for _, w := range warns {
				fmt.Fprintf(os.Stderr, "WARN %s\n", w)
			}
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "FAIL %s\n", e)
			}
			if len(errs) > 0 {
				return fmt.Errorf("%d manifest(s) failed lint", len(errs))
			}
			if strict && len(warns) > 0 {
				return fmt.Errorf("%d warning(s) under --strict", len(warns))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fixturesDir, "fixtures", "", "fixtures directory (default: same as `linkari eval run`)")
	cmd.Flags().BoolVar(&strict, "strict", false, "treat warnings as fatal")
	cmd.Flags().IntVar(&minFixtures, "min-fixtures", 0, "hard-fail any profile with fewer than N fixtures (0 = disabled)")
	return cmd
}

// countFixturesForProfile is a cheap directory scan that does not load
// fixture content. Returns (count, true) on success, (0, false) on
// missing/unreadable directory.
func countFixturesForProfile(fixturesDir, profileID string) (int, bool) {
	entries, err := os.ReadDir(fixturesDir)
	if err != nil {
		return 0, false
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		fp := filepath.Join(fixturesDir, e.Name())
		b, rerr := os.ReadFile(fp)
		if rerr != nil {
			continue
		}
		// Cheap substring sniff to avoid pulling the full Fixture struct
		// dependency chain. Schema is stable enough for this.
		if strings.Contains(string(b), "\"profile\":\""+profileID+"\"") ||
			strings.Contains(string(b), "\"profile\": \""+profileID+"\"") {
			count++
		}
	}
	return count, true
}
