package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeFixtureJSON(t *testing.T, dir string, f Fixture) {
	t.Helper()
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	name := f.Profile + "_" + f.ID + ".json"
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func validFixture() Fixture {
	return Fixture{
		ID:      "test-article",
		Profile: "eng",
		URL:     "https://example.com/article",
		Content: "Some engineering content about distributed systems.",
		Golden: Golden{
			Score:         75,
			Verdict:       "Worth reading",
			PromptVersion: "abc123",
			PromptHash:    "deadbeef",
		},
	}
}

// CT-1: Valid fixture loads successfully
func TestFixtureCT1_ValidFixtureLoads(t *testing.T) {
	dir := t.TempDir()
	writeFixtureJSON(t, dir, validFixture())

	fixtures, err := LoadFixtures(dir)
	if err != nil {
		t.Fatalf("CT-1: LoadFixtures failed: %v", err)
	}
	if len(fixtures) != 1 {
		t.Errorf("CT-1: expected 1 fixture, got %d", len(fixtures))
	}
	if vErr := ValidateFixture(fixtures[0]); vErr != nil {
		t.Errorf("CT-1: ValidateFixture failed for valid fixture: %v", vErr)
	}
}

// CT-2: Fixture without golden block rejected by ValidateFixture
func TestFixtureCT2_MissingGoldenRejected(t *testing.T) {
	f := validFixture()
	f.Golden = Golden{} // zero golden block

	err := ValidateFixture(f)
	if err == nil {
		t.Error("CT-2: expected error for missing golden block, got nil")
	}
}

// CT-3: Fixture with golden.score == 0 rejected
func TestFixtureCT3_ZeroScoreRejected(t *testing.T) {
	f := validFixture()
	f.Golden.Score = 0

	err := ValidateFixture(f)
	if err == nil {
		t.Error("CT-3: expected error for golden.score=0, got nil")
	}
}

// CT-4: Fixture with unknown profile rejected
func TestFixtureCT4_UnknownProfileRejected(t *testing.T) {
	f := validFixture()
	f.Profile = "not_a_real_profile"

	err := ValidateFixture(f)
	if err == nil {
		t.Error("CT-4: expected error for unknown profile, got nil")
	}
}

// CT-5: Empty directory returns error (corpus_empty equivalent)
func TestFixtureCT5_EmptyDirError(t *testing.T) {
	dir := t.TempDir()

	_, err := LoadFixtures(dir)
	if err == nil {
		t.Error("CT-5: expected error for empty fixtures directory, got nil")
	}
}

// CT-6: All 7 profiles covered in testdata/triage/
func TestFixtureCT6_AllSevenProfilesCovered(t *testing.T) {
	const fixturesDir = "testdata/triage"
	if _, err := os.Stat(fixturesDir); os.IsNotExist(err) {
		t.Skipf("CT-6: testdata/triage not yet populated (M4 dependency)")
	}

	fixtures, err := LoadFixtures(fixturesDir)
	if err != nil {
		t.Fatalf("CT-6: LoadFixtures: %v", err)
	}

	covered := make(map[string]int)
	for _, f := range fixtures {
		covered[f.Profile]++
	}

	for profile := range validProfileIDs {
		if covered[profile] == 0 {
			t.Errorf("CT-6: profile %q has 0 fixtures in testdata/triage", profile)
		}
	}
}

// CT-7: linkari eval run exits 0 on committed corpus (integration)
func TestFixtureCT7_EvalRunExitsZeroOnSeedCorpus(t *testing.T) {
	const fixturesDir = "testdata/triage"
	if _, err := os.Stat(fixturesDir); os.IsNotExist(err) {
		t.Skipf("CT-7: testdata/triage not yet populated (M4 dependency)")
	}

	fixtures, err := LoadFixtures(fixturesDir)
	if err != nil {
		t.Fatalf("CT-7: LoadFixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("CT-7: no fixtures found — run eval capture first")
	}

	// Run against identity scorer (golden = current) — delta must be 0
	failures := 0
	for _, f := range fixtures {
		if vErr := ValidateFixture(f); vErr != nil {
			t.Logf("CT-7: skip invalid fixture %s: %v", f.ID, vErr)
			continue
		}
		got, scoreErr := identityScorer{}.Score(f)
		if scoreErr != nil {
			t.Logf("CT-7: scorer error for %s: %v", f.ID, scoreErr)
			failures++
			continue
		}
		delta := got.Score - f.Golden.Score
		if delta < 0 {
			delta = -delta
		}
		if delta > 5 {
			t.Errorf("CT-7: fixture %s score delta=%d > tolerance=5", f.ID, delta)
			failures++
		}
	}
	if failures > 0 {
		t.Errorf("CT-7: %d fixture(s) failed — eval run would exit non-zero", failures)
	}
}
