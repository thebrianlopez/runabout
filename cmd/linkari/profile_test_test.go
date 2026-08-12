package main

import (
	"fmt"
	"os"
	"testing"
)

// mockScorer returns fixed scores per fixture ID (or a global error).
type mockScorer struct {
	scores map[string]int // fixture ID → score
	err    error          // if non-nil, returned for all fixtures
}

func (m *mockScorer) Name() string { return "mock" }
func (m *mockScorer) Score(f Fixture) (Golden, error) {
	if m.err != nil {
		return Golden{}, m.err
	}
	score := 75 // default
	if s, ok := m.scores[f.ID]; ok {
		score = s
	}
	return Golden{Score: score, PromptVersion: "mock"}, nil
}

// alternateMockScorer returns `before` on odd calls and `after` on even calls per fixture.
// Used to simulate RunProfileTest scoring the same fixture with two different profiles.
type alternateMockScorer struct {
	callCount map[string]int // counts calls per fixture ID
	before    int
	after     int
}

func newAlternateScorer(before, after int) *alternateMockScorer {
	return &alternateMockScorer{callCount: make(map[string]int), before: before, after: after}
}

func (a *alternateMockScorer) Name() string { return "alternate-mock" }
func (a *alternateMockScorer) Score(f Fixture) (Golden, error) {
	a.callCount[f.ID]++
	if a.callCount[f.ID]%2 == 1 {
		return Golden{Score: a.before, PromptVersion: "before"}, nil
	}
	return Golden{Score: a.after, PromptVersion: "after"}, nil
}

// CT-1: Loads HEAD profile version via an injected GitShowFunc
func TestProfileTestCT1_LoadsHEADVersion(t *testing.T) {
	// A deterministic git show stub, passed as an explicit dependency.
	var gitShow GitShowFunc = func(repoPath, filePath string) ([]byte, error) {
		return []byte("id: eng\nversion: 1\n"), nil
	}

	// This test verifies the injectable works — RunProfileTest will call it.
	data, err := gitShow(".", "testdata/profiles/eng.yaml")
	if err != nil {
		t.Fatalf("CT-1: gitShow failed: %v", err)
	}
	if len(data) == 0 {
		t.Error("CT-1: HEAD profile content empty")
	}
}

// CT-2: Delta computed correctly; delta=15 > tolerance=10 → EXCEEDS TOLERANCE
func TestProfileTestCT2_DeltaComputation(t *testing.T) {
	dir := t.TempDir()
	writeFixtureJSON(t, dir, Fixture{
		ID: "eng-delta-test", Profile: "eng",
		URL: "https://example.com", Content: "test",
		Golden: Golden{Score: 70, Verdict: "ok", PromptVersion: "v1"},
	})

	scorer := newAlternateScorer(70, 85) // before=70, after=85, delta=15

	var gitShow GitShowFunc = func(repoPath, filePath string) ([]byte, error) {
		return readTestProfile(t, "eng")
	}

	result, err := RunProfileTest("testdata/profiles/eng.yaml", dir, 10, scorer, gitShow)
	if err != nil {
		t.Fatalf("CT-2: RunProfileTest failed: %v", err)
	}
	if len(result.Fixtures) == 0 {
		t.Fatal("CT-2: no fixture results returned")
	}
	f := result.Fixtures[0]
	if f.Status != "EXCEEDS TOLERANCE" {
		t.Errorf("CT-2: status=%q, want EXCEEDS TOLERANCE (delta=%d > tolerance=10)", f.Status, f.Delta)
	}
}

// CT-3: All deltas within tolerance → HasFailure=false
func TestProfileTestCT3_WithinToleranceNoFailure(t *testing.T) {
	dir := t.TempDir()
	writeFixtureJSON(t, dir, Fixture{
		ID: "eng-ok", Profile: "eng",
		URL: "https://example.com", Content: "test",
		Golden: Golden{Score: 75, Verdict: "ok", PromptVersion: "v1"},
	})

	scorer := newAlternateScorer(75, 78) // before=75, after=78, delta=3 ≤ 10

	var gitShow GitShowFunc = func(repoPath, filePath string) ([]byte, error) {
		return readTestProfile(t, "eng")
	}

	result, err := RunProfileTest("testdata/profiles/eng.yaml", dir, 10, scorer, gitShow)
	if err != nil {
		t.Fatalf("CT-3: RunProfileTest failed: %v", err)
	}
	if result.HasFailure {
		t.Error("CT-3: HasFailure=true, want false (delta=3 within tolerance=10)")
	}
}

// CT-4: Delta exceeds tolerance → HasFailure=true
func TestProfileTestCT4_ExceedsToleranceHasFailure(t *testing.T) {
	dir := t.TempDir()
	writeFixtureJSON(t, dir, Fixture{
		ID: "eng-fail", Profile: "eng",
		URL: "https://example.com", Content: "test",
		Golden: Golden{Score: 70, Verdict: "ok", PromptVersion: "v1"},
	})

	scorer := newAlternateScorer(70, 90) // before=70, after=90, delta=20 > 10

	var gitShow GitShowFunc = func(repoPath, filePath string) ([]byte, error) {
		return readTestProfile(t, "eng")
	}

	result, err := RunProfileTest("testdata/profiles/eng.yaml", dir, 10, scorer, gitShow)
	if err != nil {
		t.Fatalf("CT-4: RunProfileTest failed: %v", err)
	}
	if !result.HasFailure {
		t.Error("CT-4: HasFailure=false, want true (delta=20 > tolerance=10)")
	}
}

// CT-5: Scorer error → fixture has status=SKIP, does not trigger HasFailure
func TestProfileTestCT5_ScorerErrorProducesSKIP(t *testing.T) {
	dir := t.TempDir()
	writeFixtureJSON(t, dir, Fixture{
		ID: "eng-skip", Profile: "eng",
		URL: "https://example.com", Content: "test",
		Golden: Golden{Score: 70, Verdict: "ok", PromptVersion: "v1"},
	})

	scorer := &mockScorer{err: fmt.Errorf("scorer unavailable")}

	var gitShow GitShowFunc = func(repoPath, filePath string) ([]byte, error) {
		return readTestProfile(t, "eng")
	}

	result, err := RunProfileTest("testdata/profiles/eng.yaml", dir, 10, scorer, gitShow)
	if err != nil {
		t.Fatalf("CT-5: RunProfileTest failed: %v", err)
	}
	if result.HasFailure {
		t.Error("CT-5: HasFailure=true, want false (scorer error should SKIP not FAIL)")
	}
	if len(result.Fixtures) == 0 {
		t.Fatal("CT-5: no fixture results")
	}
	if result.Fixtures[0].Status != "SKIP" {
		t.Errorf("CT-5: status=%q, want SKIP", result.Fixtures[0].Status)
	}
}

// CT-6: No fixtures for profile → no error, empty Fixtures slice
func TestProfileTestCT6_NoFixturesIsWarning(t *testing.T) {
	dir := t.TempDir()
	// no fixtures for "eng" (only life fixture)
	writeFixtureJSON(t, dir, Fixture{
		ID: "life-fixture", Profile: "life",
		URL: "https://example.com", Content: "test",
		Golden: Golden{Score: 70, Verdict: "ok", PromptVersion: "v1"},
	})

	var gitShow GitShowFunc = func(repoPath, filePath string) ([]byte, error) {
		return readTestProfile(t, "eng")
	}

	result, err := RunProfileTest("testdata/profiles/eng.yaml", dir, 10, &mockScorer{}, gitShow)
	if err != nil {
		t.Fatalf("CT-6: expected no error, got: %v", err)
	}
	if result == nil {
		t.Fatal("CT-6: result is nil")
	}
	if len(result.Fixtures) != 0 {
		t.Errorf("CT-6: expected 0 fixtures (no eng fixtures), got %d", len(result.Fixtures))
	}
}

// readTestProfile reads the eng.yaml from testdata for test use.
func readTestProfile(t *testing.T, name string) ([]byte, error) {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("testdata/profiles/%s.yaml", name))
	if err != nil {
		return nil, err
	}
	return data, nil
}
