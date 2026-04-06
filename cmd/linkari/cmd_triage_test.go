package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real-fixture markdown shape captured from M1 corpus
// (20260404_103238_gh_teamchong-turboquant-wasm.json). Verdict heading is
// inline with the verdict body — `## Verdict <text>` rather than
// `## Verdict\n<text>` — and the score line stands alone.
const fixtureCleanMarkdown = `## Verdict TurboQuant WASM—mature 3-bit/dim vector quantization with relaxed SIMD dot products. Niche relevance to RAG and semantic agent memory compression.

## Score: 52/100

| Component | Weight | Score | Rationale |
|-----------|--------|-------|-----------|
| Novelty | 0–20 | 14 | TurboQuant paper is recent (ICLR 2026). |
| Operational Relevance | 0–25 | 8 | No fit to active tools. |
| Strategic Stack Fit | 0–25 | 15 | Moderate fit to inference serving. |
| Learnability | 0–15 | 10 | README clear, theory paywalled. |
| Career Leverage | 0–15 | 5 | 1 star, new repo. |
| **Total** | **0–100** | **52** | |

## Action Items
- Read TurboQuant paper section 3
- Prototype WASM bindings in runabout
- Add to EPIC backlog
`

func TestParseTriageMarkdown_Clean(t *testing.T) {
	res, err := parseTriageMarkdown(fixtureCleanMarkdown)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Score != 52 {
		t.Errorf("score = %d, want 52", res.Score)
	}
	if !strings.HasPrefix(res.Verdict, "TurboQuant WASM") {
		t.Errorf("verdict = %q, want prefix 'TurboQuant WASM'", res.Verdict)
	}
	if len(res.ActionItems) != 3 {
		t.Errorf("action items = %d (%v), want 3", len(res.ActionItems), res.ActionItems)
	}
	if len(res.ActionItems) >= 1 && !strings.Contains(res.ActionItems[0], "TurboQuant") {
		t.Errorf("action item[0] = %q", res.ActionItems[0])
	}
}

func TestParseTriageMarkdown_FlatLine(t *testing.T) {
	// Pathological flat-line variant — this is what fish's python normalizer
	// was added to handle. Score and Verdict still need to come out clean.
	flat := `## Verdict A flat-line verdict that runs straight into the score.  ## Score: 71/100  | Component | Weight | Score | Rationale |  |---|---|---|---|`
	res, err := parseTriageMarkdown(flat)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Score != 71 {
		t.Errorf("score = %d, want 71", res.Score)
	}
	if res.Verdict == "" {
		t.Error("verdict empty after normalization")
	}
}

func TestParseTriageMarkdown_NoScore(t *testing.T) {
	_, err := parseTriageMarkdown("## Verdict no score here")
	if err == nil {
		t.Fatal("expected error on missing score")
	}
}

func TestExtractTagsLine(t *testing.T) {
	md := "## Verdict body\n\nTags: ai, agents, eval\n\n## Score: 80/100\n"
	if got := extractTagsLine(md); got != "ai, agents, eval" {
		t.Errorf("tags = %q", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	// 5 multi-byte runes; truncating to 3 must give 3 runes, not 3 bytes.
	s := "αβγδε"
	got := truncateRunes(s, 3)
	if got != "αβγ" {
		t.Errorf("got %q, want αβγ", got)
	}
}

func TestLoadProfileTemplate_Fallback(t *testing.T) {
	// Force ORG_PATH to a nonexistent dir; expect fallback to
	// ~/code/personal/docs/prompts/profiles/eng.md if it exists, otherwise
	// skip the test (clean checkout / CI).
	t.Setenv("ORG_PATH", "/tmp/nonexistent-org-path-EPIC043M2")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	expected := filepath.Join(home, "code", "personal", "docs", "prompts", "profiles", "eng.md")
	if _, err := os.Stat(expected); err != nil {
		t.Skipf("personal eng.md not present: %v", err)
	}
	path, content, err := loadProfileTemplate("eng")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if path != expected {
		t.Errorf("path = %q, want %q", path, expected)
	}
	if !strings.Contains(content, "Score") {
		t.Errorf("content missing 'Score' marker; got %q", content[:min(200, len(content))])
	}
}

func TestLoadProfileTemplate_Missing(t *testing.T) {
	t.Setenv("ORG_PATH", "/tmp/nonexistent-org-path-EPIC043M2")
	t.Setenv("HOME", t.TempDir())
	_, _, err := loadProfileTemplate("does-not-exist-profile")
	if err == nil {
		t.Fatal("expected error for missing template")
	}
}

func TestWriteScoreSidecar(t *testing.T) {
	ws := t.TempDir()
	// Pin nowRFC3339UTC for byte-level determinism.
	orig := nowRFC3339UTC
	defer func() { nowRFC3339UTC = orig }()
	nowRFC3339UTC = func() string { return "2026-04-06T12:00:00Z" }

	if err := writeScoreSidecar(ws, 73, "looks fine", "my-slug", "eng", "https://example.com"); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(ws, "_score.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("parse sidecar: %v", err)
	}
	if got["score"].(float64) != 73 {
		t.Errorf("score = %v", got["score"])
	}
	if got["verdict"] != "looks fine" {
		t.Errorf("verdict = %v", got["verdict"])
	}
	if got["slug"] != "my-slug" {
		t.Errorf("slug = %v", got["slug"])
	}
	if got["profile"] != "eng" {
		t.Errorf("profile = %v", got["profile"])
	}
	if got["url"] != "https://example.com" {
		t.Errorf("url = %v", got["url"])
	}
	if got["scored_at"] != "2026-04-06T12:00:00Z" {
		t.Errorf("scored_at = %v", got["scored_at"])
	}
}

func TestAppendTriageToReadme(t *testing.T) {
	ws := t.TempDir()
	readme := filepath.Join(ws, "README.md")
	if err := os.WriteFile(readme, []byte("# Title\n\nbody"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := appendTriageToReadme(ws, "## Score: 50/100"); err != nil {
		t.Fatalf("append: %v", err)
	}
	b, _ := os.ReadFile(readme)
	if !strings.HasSuffix(string(b), "\n---\n\n## Score: 50/100\n") {
		t.Errorf("readme suffix wrong: %q", string(b))
	}
}

// TestTriageCmd_DryRun exercises the full Cobra command path with --dry-run
// so we never call out to the real claude CLI in tests.
func TestTriageScorer_FakeHaiku(t *testing.T) {
	// Stub execHaiku with a deterministic fake that returns a known triage.
	orig := execHaiku
	defer func() { execHaiku = orig }()
	execHaiku = func(_ context.Context, _, _ string) (string, error) {
		return fixtureCleanMarkdown, nil
	}
	// Skip if eng template isn't on disk (CI / clean checkout).
	if _, _, err := loadProfileTemplate("eng"); err != nil {
		t.Skipf("eng template missing: %v", err)
	}
	fix := Fixture{ID: "test", Profile: "eng", Content: "some content"}
	got, err := triageScorer{}.Score(fix)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if got.Score != 52 {
		t.Errorf("score = %d, want 52", got.Score)
	}
	if got.RawMarkdown == "" {
		t.Error("raw markdown empty")
	}
}

