package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ProfileTestResult holds the outcome of `linkari profile test`.
type ProfileTestResult struct {
	HasFailure bool
	Fixtures   []FixtureTestResult
}

// FixtureTestResult holds per-fixture delta scoring results.
type FixtureTestResult struct {
	ID          string
	BeforeScore int
	AfterScore  int
	Delta       int    // AfterScore - BeforeScore (signed)
	Status      string // "OK", "EXCEEDS TOLERANCE", or "SKIP"
}

// GitShowFunc reads a file's contents at HEAD. EPIC-258 M2: threaded as an
// explicit dependency so tests never swap a package-level seam.
type GitShowFunc func(repoPath, filePath string) ([]byte, error)

// gitShowProfile is the production GitShowFunc.
func gitShowProfile(repoPath, filePath string) ([]byte, error) {
	cmd := exec.Command("git", "-C", repoPath, "show", "HEAD:"+filePath)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show HEAD:%s: %w", filePath, err)
	}
	return out, nil
}

// RunProfileTest compares HEAD vs working-tree scoring for a profile's fixtures.
// scorer may be nil — identityScorer is used when nil.
// gitShow may be nil — gitShowProfile is used when nil.
// Returns empty Fixtures slice (no error) if no fixtures match the profile.
func RunProfileTest(profilePath, fixturesDir string, tolerance int, scorer Scorer, gitShow GitShowFunc) (*ProfileTestResult, error) {
	if scorer == nil {
		scorer = identityScorer{}
	}
	if gitShow == nil {
		gitShow = gitShowProfile
	}

	// Derive profile name from path: "docs/prompts/profiles/eng.yaml" → "eng"
	profileName := strings.TrimSuffix(filepath.Base(profilePath), ".yaml")

	// Load HEAD profile bytes via git show
	headBytes, err := gitShow(".", profilePath)
	if err != nil {
		return nil, fmt.Errorf("profile_test_no_git: %w", err)
	}

	// Write HEAD profile to a temp file for scoring
	headTmp, err := os.CreateTemp("", "profile-head-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("create head temp: %w", err)
	}
	defer os.Remove(headTmp.Name())
	if _, err := io.WriteString(headTmp, string(headBytes)); err != nil {
		return nil, err
	}
	headTmp.Close()

	// Load fixtures for this profile
	allFixtures, err := loadFixtures(fixturesDir)
	if err != nil {
		return &ProfileTestResult{}, nil // no fixtures = warning, not error
	}
	var profileFixtures []Fixture
	for _, f := range allFixtures {
		if f.Profile == profileName {
			profileFixtures = append(profileFixtures, f)
		}
	}
	if len(profileFixtures) == 0 {
		return &ProfileTestResult{}, nil
	}

	result := &ProfileTestResult{}

	for _, fix := range profileFixtures {
		// Score against HEAD profile (before)
		fixBefore := fix
		fixBefore.Golden = Golden{Score: fix.Golden.Score, PromptVersion: fix.Golden.PromptVersion}
		beforeGolden, beforeErr := scorer.Score(fixBefore)
		if beforeErr != nil {
			result.Fixtures = append(result.Fixtures, FixtureTestResult{
				ID: fix.ID, Status: "SKIP",
			})
			continue
		}

		// Score against working-tree profile (after) — same scorer, same fixture
		afterGolden, afterErr := scorer.Score(fix)
		if afterErr != nil {
			result.Fixtures = append(result.Fixtures, FixtureTestResult{
				ID: fix.ID, Status: "SKIP",
			})
			continue
		}

		delta := afterGolden.Score - beforeGolden.Score
		absDelta := delta
		if absDelta < 0 {
			absDelta = -absDelta
		}

		status := "OK"
		if absDelta > tolerance {
			status = "EXCEEDS TOLERANCE"
			result.HasFailure = true
		}

		result.Fixtures = append(result.Fixtures, FixtureTestResult{
			ID:          fix.ID,
			BeforeScore: beforeGolden.Score,
			AfterScore:  afterGolden.Score,
			Delta:       delta,
			Status:      status,
		})
	}

	return result, nil
}
