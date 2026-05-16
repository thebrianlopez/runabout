package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func seedFixtures(t *testing.T, dir string, counts map[string]int) {
	t.Helper()
	for profile, n := range counts {
		for i := 0; i < n; i++ {
			f := Fixture{
				ID:      fmt.Sprintf("%s-fixture-%d", profile, i),
				Profile: profile,
				URL:     "https://example.com/" + profile,
				Content: "test content",
				Golden: Golden{
					Score:         75,
					Verdict:       "Worth reading",
					PromptVersion: "seed-v1.0",
				},
			}
			name := fmt.Sprintf("seed-%s-fixture-%d.json", profile, i)
			b, _ := json.MarshalIndent(f, "", "  ")
			os.WriteFile(filepath.Join(dir, name), b, 0o644)
		}
	}
}

// CT-1: Counts fixtures per profile correctly
func TestEvalStatsCT1_CountsPerProfile(t *testing.T) {
	dir := t.TempDir()
	seedFixtures(t, dir, map[string]int{"eng": 3, "life": 1})

	result, err := RunEvalStats(dir)
	if err != nil {
		t.Fatalf("CT-1: RunEvalStats failed: %v", err)
	}
	if result.Profiles["eng"] != 3 {
		t.Errorf("CT-1: profiles[eng]=%d, want 3", result.Profiles["eng"])
	}
	if result.Profiles["life"] != 1 {
		t.Errorf("CT-1: profiles[life]=%d, want 1", result.Profiles["life"])
	}
}

// CT-2: Identifies missing profiles
func TestEvalStatsCT2_IdentifiesMissingProfiles(t *testing.T) {
	dir := t.TempDir()
	seedFixtures(t, dir, map[string]int{"eng": 1}) // fashion has 0

	result, err := RunEvalStats(dir)
	if err != nil {
		t.Fatalf("CT-2: RunEvalStats failed: %v", err)
	}
	hasFashion := false
	for _, m := range result.Missing {
		if m == "fashion" {
			hasFashion = true
		}
	}
	if !hasFashion {
		t.Errorf("CT-2: fashion not in Missing list: %v", result.Missing)
	}
}

// CT-3: --min-fixtures gates exit code (tested via StatsResult)
func TestEvalStatsCT3_MinFixturesGating(t *testing.T) {
	dir := t.TempDir()
	seedFixtures(t, dir, map[string]int{"eng": 1}) // fashion has 0

	result, err := RunEvalStats(dir)
	if err != nil {
		t.Fatalf("CT-3: RunEvalStats failed: %v", err)
	}

	// Simulate --min-fixtures 1: any profile with < 1 fixture → gate fails
	minFixtures := 1
	gateViolation := false
	for _, count := range result.Profiles {
		if count < minFixtures {
			gateViolation = true
			break
		}
	}
	if !gateViolation {
		t.Error("CT-3: expected gate violation (fashion has 0 fixtures, min=1), got none")
	}
}

// CT-4: --json produces valid JSON matching StatsResult schema
func TestEvalStatsCT4_JSONOutput(t *testing.T) {
	dir := t.TempDir()
	seedFixtures(t, dir, map[string]int{"eng": 2, "life": 1})

	result, err := RunEvalStats(dir)
	if err != nil {
		t.Fatalf("CT-4: RunEvalStats failed: %v", err)
	}

	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("CT-4: marshal StatsResult: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("CT-4: output is not valid JSON: %v", err)
	}
	if _, ok := parsed["profiles"]; !ok {
		t.Error("CT-4: JSON missing 'profiles' key")
	}
	if _, ok := parsed["total"]; !ok {
		t.Error("CT-4: JSON missing 'total' key")
	}
}

// CT-5: Empty directory returns stats_no_fixtures error
func TestEvalStatsCT5_EmptyDirError(t *testing.T) {
	dir := t.TempDir()

	_, err := RunEvalStats(dir)
	if err == nil {
		t.Error("CT-5: expected error for empty directory, got nil")
	}
}

// CT-6: All 7 profiles appear in output even with 0 fixtures for some
func TestEvalStatsCT6_AllSevenProfilesInOutput(t *testing.T) {
	dir := t.TempDir()
	seedFixtures(t, dir, map[string]int{"eng": 1, "life": 1, "travel": 1}) // 4 profiles missing

	result, err := RunEvalStats(dir)
	if err != nil {
		t.Fatalf("CT-6: RunEvalStats failed: %v", err)
	}

	for profile := range validProfileIDs {
		if _, ok := result.Profiles[profile]; !ok {
			t.Errorf("CT-6: profile %q missing from output (want all 7)", profile)
		}
	}
}
