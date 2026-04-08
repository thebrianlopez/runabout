package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatal(err)
	}

	prev := refreshScorerFn
	t.Cleanup(func() { refreshScorerFn = prev })
	refreshScorerFn = func(ctx context.Context, profile, content string) (TriageVerdict, error) {
		return TriageVerdict{
			Score:        82,
			Verdict:      "fresh",
			RubricScores: map[string]int{"signal": 82},
		}, nil
	}

	cmd := evalRefreshGoldensCmd()
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
		0644); err != nil {
		t.Fatal(err)
	}
	scoreJSON := `{"score":81,"verdict":"keep","slug":"test-slug","profile":"eng","url":"https://example.com","scored_at":"2026-04-06T11:00:00Z"}`
	if err := os.WriteFile(filepath.Join(ws, "_score.json"), []byte(scoreJSON), 0644); err != nil {
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
		if err := os.WriteFile(filepath.Join(dir, f.ID+".json"), b, 0644); err != nil {
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
	// produced the fixtures — this is the M1 self-test.
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
// the wrong cwd), decoy dotfiles, and unparseable JSON — and must not
// hard-fail the whole load when it hits one.
func TestLoadFixturesSkipsInvalidAndDecoys(t *testing.T) {
	dir := t.TempDir()
	good := Fixture{ID: "ok", Profile: "eng", Content: "c", Golden: Golden{Score: 50, Verdict: "v"}}
	b, _ := json.MarshalIndent(good, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "ok.json"), b, 0644); err != nil {
		t.Fatal(err)
	}
	// Invalid ID "." — this is the real-world bogus fixture captured
	// from cwd="." by `linkari eval capture`.
	bad := good
	bad.ID = "."
	bb, _ := json.MarshalIndent(bad, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "dot_id.json"), bb, 0644); err != nil {
		t.Fatal(err)
	}
	// Decoy dotfile (editor swapfile, hidden metadata).
	if err := os.WriteFile(filepath.Join(dir, ".hidden.json"), []byte(`{"id":"x"}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Decoy garbage JSON.
	if err := os.WriteFile(filepath.Join(dir, "garbage.json"), []byte(`{not json`), 0644); err != nil {
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
		if err := os.WriteFile(filepath.Join(dir, f.ID+".json"), b, 0644); err != nil {
			t.Fatal(err)
		}
	}

	prev := registeredScorerFn
	t.Cleanup(func() { registeredScorerFn = prev })
	registeredScorerFn = func() Scorer { return fakeScorer{} }

	cmd := evalRunCmd()
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
