package main

import "fmt"

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
func LoadFixtures(dir string) ([]Fixture, error) {
	fixtures, err := loadFixtures(dir) // delegates to existing internal implementation
	if err != nil {
		return nil, err
	}
	if len(fixtures) == 0 {
		return nil, fmt.Errorf("corpus_empty: no valid fixture JSON files found in %s", dir)
	}
	return fixtures, nil
}

// ValidateFixture checks that a fixture has all required golden fields.
// Returns an error with the specific error class if validation fails.
func ValidateFixture(f Fixture) error {
	if !validProfileIDs[f.Profile] {
		return fmt.Errorf("fixture_unknown_profile: profile %q not in registered profile list", f.Profile)
	}
	if f.Golden.Score == 0 || f.Golden.PromptVersion == "" {
		return fmt.Errorf("fixture_missing_golden: fixture %q: golden block missing or invalid (score=%d, prompt_version=%q)", f.ID, f.Golden.Score, f.Golden.PromptVersion)
	}
	return nil
}
