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
