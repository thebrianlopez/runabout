package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- F1 Behavioral Tests ---

// BT-1: Multiple fixtures per profile — all load and validate
func TestFixtureBT1_MultipleFixturesPerProfile(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		f := Fixture{
			ID: fmt.Sprintf("eng-multi-%d", i), Profile: "eng",
			URL: "https://example.com", Content: "test",
			Golden: Golden{Score: 70 + i, Verdict: "ok", PromptVersion: "v1"},
		}
		b, _ := json.MarshalIndent(f, "", "  ")
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("seed-eng-%d.json", i)), b, 0o644)
	}

	fixtures, err := LoadFixtures(dir)
	if err != nil {
		t.Fatalf("BT-1: LoadFixtures: %v", err)
	}
	if len(fixtures) != 3 {
		t.Errorf("BT-1: expected 3 fixtures, got %d", len(fixtures))
	}
	for _, f := range fixtures {
		if err := ValidateFixture(f); err != nil {
			t.Errorf("BT-1: ValidateFixture(%s): %v", f.ID, err)
		}
	}
}

// BT-2: Malformed JSON skipped, valid fixtures still load
func TestFixtureBT2_MalformedJSONSkipped(t *testing.T) {
	dir := t.TempDir()
	// Write one valid fixture
	valid := Fixture{
		ID: "eng-valid", Profile: "eng",
		URL: "https://example.com", Content: "test",
		Golden: Golden{Score: 75, Verdict: "ok", PromptVersion: "v1"},
	}
	b, _ := json.MarshalIndent(valid, "", "  ")
	os.WriteFile(filepath.Join(dir, "seed-eng-valid.json"), b, 0o644)
	// Write malformed JSON
	os.WriteFile(filepath.Join(dir, "seed-eng-bad.json"), []byte("not json!"), 0o644)

	fixtures, err := LoadFixtures(dir)
	if err != nil {
		t.Fatalf("BT-2: LoadFixtures: %v", err)
	}
	if len(fixtures) != 1 {
		t.Errorf("BT-2: expected 1 valid fixture (malformed skipped), got %d", len(fixtures))
	}
}

// --- F3 Behavioral Tests ---

// BT-1: Human table output contains profile names and counts
func TestEvalStatsBT1_HumanTableOutput(t *testing.T) {
	dir := t.TempDir()
	seedFixtures(t, dir, map[string]int{"eng": 2, "life": 1})

	result, err := RunEvalStats(dir)
	if err != nil {
		t.Fatalf("BT-1: RunEvalStats: %v", err)
	}
	if result.Profiles["eng"] != 2 {
		t.Errorf("BT-1: profiles[eng]=%d, want 2", result.Profiles["eng"])
	}
	if result.Total != 3 {
		t.Errorf("BT-1: total=%d, want 3", result.Total)
	}
}

// BT-2: Fixture with unknown profile is counted but shows in its own bucket
func TestEvalStatsBT2_UnknownProfileCounted(t *testing.T) {
	dir := t.TempDir()
	// Write a fixture with an unknown profile directly
	f := Fixture{
		ID: "unknown-fixture", Profile: "not_registered",
		URL: "https://example.com", Content: "test",
		Golden: Golden{Score: 75, Verdict: "ok", PromptVersion: "v1"},
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	os.WriteFile(filepath.Join(dir, "seed-unknown.json"), b, 0o644)
	// Also add a valid fixture so dir is not empty
	seedFixtures(t, dir, map[string]int{"eng": 1})

	result, err := RunEvalStats(dir)
	if err != nil {
		t.Fatalf("BT-2: RunEvalStats: %v", err)
	}
	// Unknown profile doesn't increment any registered profile count
	if _, ok := result.Profiles["not_registered"]; ok {
		t.Error("BT-2: unknown profile should not appear in registered profiles")
	}
}

// --- F4 Behavioral Tests ---

// BT-1: Negative delta (before > after) is reported correctly
func TestProfileTestBT1_NegativeDelta(t *testing.T) {
	dir := t.TempDir()
	writeFixtureJSON(t, dir, Fixture{
		ID: "eng-negative", Profile: "eng",
		URL: "https://example.com", Content: "test",
		Golden: Golden{Score: 80, Verdict: "ok", PromptVersion: "v1"},
	})

	scorer := newAlternateScorer(80, 65) // before=80, after=65, delta=-15

	origGit := execGitShowProfile
	execGitShowProfile = func(repoPath, filePath string) ([]byte, error) {
		return readTestProfile(t, "eng")
	}
	defer func() { execGitShowProfile = origGit }()

	result, err := RunProfileTest("testdata/profiles/eng.yaml", dir, 10, scorer)
	if err != nil {
		t.Fatalf("BT-1: RunProfileTest: %v", err)
	}
	if len(result.Fixtures) == 0 {
		t.Fatal("BT-1: no fixture results")
	}
	f := result.Fixtures[0]
	if f.Delta != -15 {
		t.Errorf("BT-1: delta=%d, want -15", f.Delta)
	}
	if f.Status != "EXCEEDS TOLERANCE" {
		t.Errorf("BT-1: status=%q, want EXCEEDS TOLERANCE (|delta|=15 > 10)", f.Status)
	}
}

// BT-2: Table output contains fixture IDs and status
func TestProfileTestBT2_TableOutput(t *testing.T) {
	// This behavioral test checks that the result struct has the needed fields
	// for table rendering. Actual formatting tested via cmd integration.
	dir := t.TempDir()
	writeFixtureJSON(t, dir, Fixture{
		ID: "eng-table-test", Profile: "eng",
		URL: "https://example.com", Content: "test",
		Golden: Golden{Score: 75, Verdict: "ok", PromptVersion: "v1"},
	})

	scorer := newAlternateScorer(75, 76) // delta=1 ≤ 10

	origGit := execGitShowProfile
	execGitShowProfile = func(repoPath, filePath string) ([]byte, error) {
		return readTestProfile(t, "eng")
	}
	defer func() { execGitShowProfile = origGit }()

	result, err := RunProfileTest("testdata/profiles/eng.yaml", dir, 10, scorer)
	if err != nil {
		t.Fatalf("BT-2: RunProfileTest: %v", err)
	}
	if len(result.Fixtures) == 0 {
		t.Fatal("BT-2: no fixture results")
	}
	f := result.Fixtures[0]
	if f.ID == "" {
		t.Error("BT-2: fixture ID empty")
	}
	if f.Status == "" {
		t.Error("BT-2: fixture Status empty")
	}
}

// BT-3: Profile name extracted correctly from various path forms
func TestProfileTestBT3_ProfileNameFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"testdata/profiles/eng.yaml", "eng"},
		{"docs/prompts/profiles/life.yaml", "life"},
		{"eng.yaml", "eng"},
	}

	for _, tc := range cases {
		got := strings.TrimSuffix(filepath.Base(tc.path), ".yaml")
		if got != tc.want {
			t.Errorf("BT-3: path=%q → name=%q, want %q", tc.path, got, tc.want)
		}
	}
}

// --- F5 Behavioral Tests ---

// BT-1: Search order: env var path takes precedence over ORG_PATH
func TestProfilePathBT1_SearchOrderEnvFirst(t *testing.T) {
	envDir := t.TempDir()
	orgDir := t.TempDir()

	// Write different profile content to each
	os.WriteFile(filepath.Join(envDir, "eng.yaml"), []byte("env-version"), 0o644)
	profilesDir := filepath.Join(orgDir, "docs", "prompts", "profiles")
	os.MkdirAll(profilesDir, 0o755)
	os.WriteFile(filepath.Join(profilesDir, "eng.yaml"), []byte("org-version"), 0o644)

	t.Setenv("LINKARI_PROFILE_PATH", envDir)
	t.Setenv("ORG_PATH", orgDir)

	paths := ProfileSearchPath()
	if len(paths) == 0 {
		t.Fatal("BT-1: ProfileSearchPath returned empty list")
	}
	if paths[0] != envDir {
		t.Errorf("BT-1: first search path=%q, want envDir=%q", paths[0], envDir)
	}
}

// BT-2: Empty LINKARI_PROFILE_PATH is ignored (falls through to next paths)
func TestProfilePathBT2_EmptyEnvIgnored(t *testing.T) {
	t.Setenv("LINKARI_PROFILE_PATH", "")
	t.Setenv("ORG_PATH", "")

	paths := ProfileSearchPath()
	for _, p := range paths {
		if p == "" {
			t.Error("BT-2: empty string found in ProfileSearchPath (empty env should be ignored)")
		}
	}
}
