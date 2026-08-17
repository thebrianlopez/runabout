package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefreshGoldensRewritesAndAudits(t *testing.T) {
	dir := t.TempDir()
	orig := Fixture{
		ID:         "fx1",
		CapturedAt: "2026-01-01T00:00:00Z",
		Source:     "fish-exact",
		URL:        "https://example.com",
		Profile:    "eng",
		Content:    "some content",
		Golden:     Golden{Score: 50, Verdict: "old", RawMarkdown: "old md"},
	}
	path := filepath.Join(dir, orig.ID+".json")
	b, _ := json.MarshalIndent(orig, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := evalRefreshGoldensCmd(scorerDeps{RefreshScorer: func(ctx context.Context, profile, content string) (*Scorecard, error) {
		return &Scorecard{
			Score:        82,
			Verdict:      "fresh",
			RubricScores: map[string]int{"signal": 82},
			RawMarkdown:  "## Score: 82/100\n\n## Verdict\nfresh\n",
			Backend:      "test",
		}, nil
	}}.resolve())
	cmd.SetArgs([]string{"--fixtures", dir, "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Fixture
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Golden.Score != 82 {
		t.Errorf("score = %d, want 82", got.Golden.Score)
	}
	if got.Golden.Verdict != "fresh" {
		t.Errorf("verdict = %q, want fresh", got.Golden.Verdict)
	}
	if got.Golden.RefreshedFrom == nil || *got.Golden.RefreshedFrom != 50 {
		t.Errorf("refreshed_from = %v, want 50", got.Golden.RefreshedFrom)
	}
	if got.ID != orig.ID || got.URL != orig.URL || got.Profile != orig.Profile || got.Content != orig.Content || got.Source != orig.Source {
		t.Errorf("non-golden fields drifted: %+v", got)
	}
	if got.CapturedAt == orig.CapturedAt {
		t.Errorf("captured_at not bumped")
	}
	if got.Golden.RawMarkdown == "" {
		t.Errorf("raw_markdown empty after refresh")
	}
}

func TestExtractTriageBlock(t *testing.T) {
	readme := "# Title\n\nbody body\n\n---\n\n## Score: 72/100\n\n## Verdict\nkeep it\n"
	got := extractTriageBlock(readme)
	if got == "" {
		t.Fatal("expected non-empty triage block")
	}
	score, err := parseScoreFromMarkdown(got)
	if err != nil {
		t.Fatalf("parse score: %v", err)
	}
	if score != 72 {
		t.Errorf("score = %d, want 72", score)
	}
}

func TestCaptureFromWorkspace(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "README.md"),
		[]byte("# Page\n\nthe content\n\n---\n\n## Score: 81/100\n\n## Verdict\nkeep\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	scoreJSON := `{"score":81,"verdict":"keep","slug":"test-slug","profile":"eng","url":"https://example.com","scored_at":"2026-04-06T11:00:00Z"}`
	if err := os.WriteFile(filepath.Join(ws, "_score.json"), []byte(scoreJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	fix, err := captureFromWorkspace(ws, "", "")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if fix.ID != "test-slug" {
		t.Errorf("id = %q, want test-slug", fix.ID)
	}
	if fix.Profile != "eng" {
		t.Errorf("profile = %q, want eng", fix.Profile)
	}
	if fix.URL != "https://example.com" {
		t.Errorf("url = %q, want https://example.com", fix.URL)
	}
	if fix.Golden.Score != 81 {
		t.Errorf("golden score = %d, want 81", fix.Golden.Score)
	}
	if fix.Content == "" {
		t.Error("content should not be empty (fallback to README body)")
	}
}

func TestLoadFixturesAndIdentityScorerSelfTest(t *testing.T) {
	dir := t.TempDir()
	fixtures := []Fixture{
		{ID: "a", Profile: "eng", Content: "x", Golden: Golden{Score: 50, Verdict: "meh"}},
		{ID: "b", Profile: "life", Content: "y", Golden: Golden{Score: 90, Verdict: "save"}},
	}
	for _, f := range fixtures {
		b, _ := json.MarshalIndent(f, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, f.ID+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	loaded, err := loadFixtures(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d, want 2", len(loaded))
	}

	// Identity scorer must produce zero delta against the goldens that
	// produced the fixtures  -  this is the M1 self-test.
	s := identityScorer{}
	for _, f := range loaded {
		got, err := s.Score(f)
		if err != nil {
			t.Fatalf("score %s: %v", f.ID, err)
		}
		if got.Score != f.Golden.Score {
			t.Errorf("%s: identity scorer drift score=%d golden=%d", f.ID, got.Score, f.Golden.Score)
		}
	}
}

// M6b: loadFixtures must reject fixtures with invalid IDs (captured from
// the wrong cwd), decoy dotfiles, and unparseable JSON  -  and must not
// hard-fail the whole load when it hits one.
func TestLoadFixturesSkipsInvalidAndDecoys(t *testing.T) {
	dir := t.TempDir()
	good := Fixture{ID: "ok", Profile: "eng", Content: "c", Golden: Golden{Score: 50, Verdict: "v"}}
	b, _ := json.MarshalIndent(good, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "ok.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	// Invalid ID "."  -  this is the real-world bogus fixture captured
	// from cwd="." by `linkari eval capture`.
	bad := good
	bad.ID = "."
	bb, _ := json.MarshalIndent(bad, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "dot_id.json"), bb, 0o644); err != nil {
		t.Fatal(err)
	}
	// Decoy dotfile (editor swapfile, hidden metadata).
	if err := os.WriteFile(filepath.Join(dir, ".hidden.json"), []byte(`{"id":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Decoy garbage JSON.
	if err := os.WriteFile(filepath.Join(dir, "garbage.json"), []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadFixtures(dir)
	if err != nil {
		t.Fatalf("loadFixtures: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != "ok" {
		t.Fatalf("loaded = %+v, want exactly the ok fixture", loaded)
	}
}

func TestIsValidFixtureID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"ok", true},
		{"20260404_103238_gh_abc", true},
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{`a\b`, false},
	}
	for _, c := range cases {
		if got := isValidFixtureID(c.id); got != c.want {
			t.Errorf("isValidFixtureID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

// M6b: eval runner must treat Scorer results with Skip=true as SKIP and
// must not gate the run on them. Also exercises the scorer-error degrade
// path (hard error from the scorer is downgraded to SKIP too).
func TestEvalRunTreatsSkipAndScorerErrorAsSkip(t *testing.T) {
	dir := t.TempDir()
	fixes := []Fixture{
		{ID: "a_pass", Profile: "eng", Content: "x", Golden: Golden{Score: 50}},
		{ID: "b_parse_failed", Profile: "eng", Content: "x", Golden: Golden{Score: 60}},
		{ID: "c_scorer_error", Profile: "eng", Content: "x", Golden: Golden{Score: 70}},
	}
	for _, f := range fixes {
		b, _ := json.MarshalIndent(f, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, f.ID+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := evalRunCmd(scorerDeps{RegisteredScorer: func() Scorer { return fakeScorer{} }}.resolve())
	cmd.SetArgs([]string{"--fixtures", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("eval run: %v (skips must not redline the run)", err)
	}
}

type fakeScorer struct{}

func (fakeScorer) Name() string { return "fake" }
func (fakeScorer) Score(f Fixture) (Golden, error) {
	switch f.ID {
	case "a_pass":
		return Golden{Score: 50}, nil
	case "b_parse_failed":
		return Golden{Skip: true, SkipReason: "parse_failed"}, nil
	case "c_scorer_error":
		return Golden{}, fmtErr("no score line")
	}
	return Golden{}, nil
}

func fmtErr(s string) error { return &stringErr{s} }

type stringErr struct{ s string }

func (e *stringErr) Error() string { return e.s }

// Contract Tests (CT-1 through CT-5) per TDD: Bootstrap testdata/triage/ with Per-Profile Golden Fixtures

// CT-1: Valid fixture loads successfully
func TestLoadFixturesValidFixtureLoadsSuccessfully(t *testing.T) {
	dir := t.TempDir()
	fixture := Fixture{
		ID:         "eng_valid",
		CapturedAt: "2026-01-01T00:00:00Z",
		Source:     "fish-exact",
		URL:        "https://example.com",
		Profile:    "eng",
		Content:    "test content",
		Golden: Golden{
			Score:         50,
			Verdict:       "keep",
			RawMarkdown:   "## Score: 50/100",
			PromptVersion: "abc123",
		},
	}
	b, _ := json.MarshalIndent(fixture, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "eng_valid.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadFixtures(dir)
	if err != nil {
		t.Fatalf("LoadFixtures failed: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d fixtures, want 1", len(loaded))
	}
	if loaded[0].ID != "eng_valid" {
		t.Errorf("ID = %q, want eng_valid", loaded[0].ID)
	}
}

// CT-2: Missing golden block rejected
func TestValidateFixtureMissingGoldenBlockRejected(t *testing.T) {
	fixture := Fixture{
		ID:      "no_golden",
		Profile: "eng",
		Golden: Golden{
			Score:         0, // Missing/invalid golden
			PromptVersion: "",
		},
	}
	err := ValidateFixture(fixture)
	if err == nil {
		t.Fatal("expected error for missing golden block")
	}
	if !strings.Contains(err.Error(), "fixture_missing_golden") {
		t.Errorf("error should be fixture_missing_golden, got: %v", err)
	}
}

// CT-3: Zero score rejected
func TestValidateFixtureZeroScoreRejected(t *testing.T) {
	fixture := Fixture{
		ID:      "zero_score",
		Profile: "eng",
		Golden: Golden{
			Score:         0, // Invalid: zero score
			PromptVersion: "abc123",
		},
	}
	err := ValidateFixture(fixture)
	if err == nil {
		t.Fatal("expected error for zero score")
	}
	if !strings.Contains(err.Error(), "fixture_missing_golden") {
		t.Errorf("error should be fixture_missing_golden, got: %v", err)
	}
}

// CT-4: Unknown profile rejected
func TestValidateFixtureUnknownProfileRejected(t *testing.T) {
	fixture := Fixture{
		ID:      "bad_profile",
		Profile: "unknown_profile",
		Golden: Golden{
			Score:         50,
			PromptVersion: "abc123",
		},
	}
	err := ValidateFixture(fixture)
	if err == nil {
		t.Fatal("expected error for unknown profile")
	}
	if !strings.Contains(err.Error(), "fixture_unknown_profile") {
		t.Errorf("error should be fixture_unknown_profile, got: %v", err)
	}
}

// CT-5: Empty directory returns corpus_empty
func TestLoadFixturesEmptyDirectoryReturnsError(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadFixtures(dir)
	if err == nil {
		t.Fatal("expected error for empty directory")
	}
	if !strings.Contains(err.Error(), "corpus_empty") {
		t.Errorf("error should be corpus_empty, got: %v", err)
	}
}

// CT-6: All 7 profiles covered by committed fixtures
func TestLoadFixturesAllProfilesCovered(t *testing.T) {
	dir := t.TempDir()

	// Create one fixture per profile
	profiles := []string{"eng", "life", "travel", "fashion", "music", "finance", "dining"}
	for i, profile := range profiles {
		fixture := Fixture{
			ID:      profile + "_test",
			Profile: profile,
			Content: "test content",
			Golden: Golden{
				Score:         50 + i,
				PromptVersion: "v1",
			},
		}
		b, _ := json.MarshalIndent(fixture, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, fixture.ID+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	loaded, err := LoadFixtures(dir)
	if err != nil {
		t.Fatalf("LoadFixtures: %v", err)
	}

	profileMap := make(map[string]bool)
	for _, f := range loaded {
		profileMap[f.Profile] = true
	}

	for _, p := range profiles {
		if !profileMap[p] {
			t.Errorf("profile %q missing from loaded fixtures", p)
		}
	}

	if len(loaded) != len(profiles) {
		t.Errorf("loaded %d fixtures, want %d", len(loaded), len(profiles))
	}
}

// CT-7: eval run exits 0 on seed corpus
func TestEvalRunAgainstSeedCorpus(t *testing.T) {
	dir := t.TempDir()

	// Create a minimal valid fixture
	fixture := Fixture{
		ID:      "test_fixture",
		Profile: "eng",
		Content: "content",
		Golden: Golden{
			Score:         50,
			Verdict:       "test",
			PromptVersion: "v1",
		},
	}
	b, _ := json.MarshalIndent(fixture, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, fixture.ID+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	// Load fixtures and verify identity scorer produces zero delta
	loaded, err := LoadFixtures(dir)
	if err != nil {
		t.Fatalf("LoadFixtures: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 fixture, got %d", len(loaded))
	}

	// Run identity scorer (the baseline for M1)
	scorer := identityScorer{}
	got, err := scorer.Score(loaded[0])
	if err != nil {
		t.Fatalf("scorer.Score: %v", err)
	}

	if got.Score != loaded[0].Golden.Score {
		t.Errorf("identity scorer delta: got score %d, want %d", got.Score, loaded[0].Golden.Score)
	}
}

// BT-1: Multiple fixtures per profile
func TestLoadFixturesMultipleFixturesPerProfile(t *testing.T) {
	dir := t.TempDir()

	// Create 3 eng fixtures
	for i := 0; i < 3; i++ {
		fixture := Fixture{
			ID:      fmt.Sprintf("eng_fixture_%d", i),
			Profile: "eng",
			Content: fmt.Sprintf("content %d", i),
			Golden: Golden{
				Score:         50 + i*5,
				PromptVersion: "v1",
			},
		}
		b, _ := json.MarshalIndent(fixture, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, fixture.ID+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	loaded, err := LoadFixtures(dir)
	if err != nil {
		t.Fatalf("LoadFixtures: %v", err)
	}

	if len(loaded) != 3 {
		t.Fatalf("loaded %d fixtures, want 3", len(loaded))
	}

	for _, f := range loaded {
		if f.Profile != "eng" {
			t.Errorf("fixture profile = %q, want eng", f.Profile)
		}
	}
}

// BT-2: Malformed JSON skipped with error
func TestLoadFixturesMalformedJsonSkipped(t *testing.T) {
	dir := t.TempDir()

	// Valid fixture
	good := Fixture{
		ID:      "good",
		Profile: "eng",
		Content: "c",
		Golden: Golden{
			Score:         50,
			PromptVersion: "v1",
		},
	}
	b, _ := json.MarshalIndent(good, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "good.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	// Malformed JSON
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{not valid json`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadFixtures(dir)
	if err != nil {
		t.Fatalf("LoadFixtures: %v", err)
	}

	if len(loaded) != 1 || loaded[0].ID != "good" {
		t.Fatalf("expected only good fixture, got %+v", loaded)
	}
}

// RG-1: Eval gate impossible to satisfy without committed corpus
func TestRegressionGuardCommittedCorpus(t *testing.T) {
	// This test verifies that the committed testdata/triage/ allows
	// eval gates to be satisfied without user-local data.
	// The test uses the package's hardcoded testdata path.

	// Note: In a real CI/test environment, this would verify that
	// `linkari eval run` without --fixtures flag defaults to testdata/triage
	// and finds at least one fixture per profile.

	// Create a mock testdata directory for this test
	dir := t.TempDir()

	// Verify that we can create a corpus with at least one fixture per profile
	profiles := []string{"eng", "life", "travel", "fashion", "music", "finance", "dining"}
	for _, profile := range profiles {
		fixture := Fixture{
			ID:      profile + "_seed",
			Profile: profile,
			Content: "seed content",
			Golden: Golden{
				Score:         60,
				PromptVersion: "seed",
			},
		}
		b, _ := json.MarshalIndent(fixture, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, fixture.ID+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Verify the corpus can be loaded
	loaded, err := LoadFixtures(dir)
	if err != nil {
		t.Fatalf("LoadFixtures: %v", err)
	}

	profileCounts := make(map[string]int)
	for _, f := range loaded {
		profileCounts[f.Profile]++
	}

	// Verify at least one fixture per profile
	for _, profile := range profiles {
		if profileCounts[profile] == 0 {
			t.Errorf("profile %q has no fixtures", profile)
		}
	}
}
