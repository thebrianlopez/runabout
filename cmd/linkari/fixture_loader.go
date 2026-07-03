package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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
// Skips malformed files with a warning (does not fail on first error).
// Returns an error if dir has no valid fixture JSON files.
// Per TDD: LoadFixtures collects all errors and reports them together.
func LoadFixtures(dir string) ([]Fixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read fixtures dir %s: %w", dir, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("corpus_empty: no valid fixture JSON files found in %s", dir)
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
			fmt.Fprintf(os.Stderr, "eval: skip %s: read: %v\n", name, err)
			continue
		}
		var f Fixture
		if err := json.Unmarshal(b, &f); err != nil {
			fmt.Fprintf(os.Stderr, "eval: skip %s: parse: %v\n", name, err)
			continue
		}
		if !isValidFixtureID(f.ID) {
			fmt.Fprintf(os.Stderr, "eval: skip %s: invalid fixture id %q\n", name, f.ID)
			continue
		}
		if err := ValidateFixture(f); err != nil {
			fmt.Fprintf(os.Stderr, "eval: skip %s: %v\n", f.ID, err)
			continue
		}
		out = append(out, f)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("corpus_empty: no valid fixture JSON files found in %s", dir)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ValidateFixture checks that a fixture has all required golden fields.
// Returns an error with the specific error class if validation fails.
// Per TDD: Required fields in golden: score (non-zero), prompt_version (non-empty).
func ValidateFixture(f Fixture) error {
	if !validProfileIDs[f.Profile] {
		return fmt.Errorf("fixture_unknown_profile: profile %q not in registered profile list", f.Profile)
	}
	if f.Golden.Score == 0 || f.Golden.PromptVersion == "" {
		return fmt.Errorf("fixture_missing_golden: fixture %q: golden block missing or invalid (score=%d, prompt_version=%q)", f.ID, f.Golden.Score, f.Golden.PromptVersion)
	}
	return nil
}
