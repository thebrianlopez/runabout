package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	// EPIC-044 M2: YAML manifest is preferred over the legacy .md when
	// present (loadProfileTemplate tries yaml first, falls back to md).
	expected := filepath.Join(home, "code", "personal", "docs", "prompts", "profiles", "eng.yaml")
	if _, err := os.Stat(expected); err != nil {
		// Fall back to the legacy .md path for clean checkouts that
		// haven't migrated yet.
		expected = filepath.Join(home, "code", "personal", "docs", "prompts", "profiles", "eng.md")
		if _, err := os.Stat(expected); err != nil {
			t.Skipf("personal eng manifest not present: %v", err)
		}
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

// EPIC-044 M4 — Loader fallback verification.
//
// Hermetic test: create a profile dir containing BOTH `<profile>.yaml`
// and `<profile>.md` and assert loadProfileTemplate returns the YAML
// path (Layer 1 wins). Then delete the YAML and assert it falls back
// to the .md. This locks in the load order so the eventual .md
// deletion (M6) is a runtime no-op.
func TestLoadProfileTemplate_M4_YAMLPreferredOverMarkdown(t *testing.T) {
	dir := t.TempDir()
	profilesDir := filepath.Join(dir, "docs", "prompts", "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yamlPath := filepath.Join(profilesDir, "testprof.yaml")
	mdPath := filepath.Join(profilesDir, "testprof.md")

	manifest := `id: testprof
version: 1
schema_version: triage_verdict_v1
persona_intro: Test persona intro.
noise_gate:
  min_chars: 100
  skip_label: Test
persona_body: |
  Test persona body content.
verdict_prompt: test verdict
rubric:
  - {name: A, weight: 20, rationale: a}
  - {name: B, weight: 20, rationale: b}
  - {name: C, weight: 20, rationale: c}
  - {name: D, weight: 20, rationale: d}
  - {name: E, weight: 20, rationale: e}
action_items:
  count: "3"
  horizon_days: 7
key_facts:
  count: "5"
`
	if err := os.WriteFile(yamlPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdPath, []byte("LEGACY MARKDOWN BODY\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ORG_PATH", dir)
	t.Setenv("HOME", t.TempDir()) // isolate from real ~/code/personal

	path, content, err := loadProfileTemplate("testprof")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if path != yamlPath {
		t.Errorf("loader returned %q, want yaml path %q (yaml must win over md)", path, yamlPath)
	}
	if strings.Contains(content, "LEGACY MARKDOWN BODY") {
		t.Errorf("content came from .md fallback; expected rendered yaml")
	}
	if !strings.Contains(content, "Test persona intro.") {
		t.Errorf("rendered yaml missing persona_intro; got %q", content[:min(200, len(content))])
	}

	// Now remove the yaml and confirm fallback to .md.
	if err := os.Remove(yamlPath); err != nil {
		t.Fatal(err)
	}
	path, content, err = loadProfileTemplate("testprof")
	if err != nil {
		t.Fatalf("load after yaml removed: %v", err)
	}
	if path != mdPath {
		t.Errorf("after yaml removed, loader returned %q, want md path %q", path, mdPath)
	}
	if !strings.Contains(content, "LEGACY MARKDOWN BODY") {
		t.Errorf("md fallback content unexpected: %q", content)
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
	scoredAt := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)

	if err := writeScoreSidecarAt(ws, 73, "looks fine", "my-slug", "eng", "https://example.com", nil, scoredAt); err != nil {
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
	if err := os.WriteFile(readme, []byte("# Title\n\nbody"), 0o644); err != nil {
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

// TestTriageScorer_FakeHaiku exercises the triageScorer with a stubbed
// execHaikuJSON so we never call out to the real claude CLI in tests.
// TestHaikuEnv_PersonaIsolation verifies that all claude subprocess calls strip
// CLAUDECODE= from the environment and inject CLAUDE_CODE_DISABLE_CLAUDE_MDS=1
// to prevent the subprocess from discovering workspace CLAUDE.md files. EPIC-088 M1.
func TestHaikuEnv_PersonaIsolation(t *testing.T) {
	// Seed API key vars so we can assert they are stripped. EPIC-089 M5.
	t.Setenv("ANTHROPIC_API_KEY", "test-should-be-stripped")
	t.Setenv("CLAUDE_API_KEY", "test-should-be-stripped")

	env := haikuEnv()
	hasDisableMDs := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDECODE=") {
			t.Errorf("CLAUDECODE should be stripped from haikuEnv(), got: %s", kv)
		}
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			t.Errorf("ANTHROPIC_API_KEY should be stripped from haikuEnv() — CLI-only auth invariant violated")
		}
		if strings.HasPrefix(kv, "CLAUDE_API_KEY=") {
			t.Errorf("CLAUDE_API_KEY should be stripped from haikuEnv() — CLI-only auth invariant violated")
		}
		if kv == "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1" {
			hasDisableMDs = true
		}
	}
	if !hasDisableMDs {
		t.Error("CLAUDE_CODE_DISABLE_CLAUDE_MDS=1 not found in haikuEnv() — persona isolation broken")
	}
}

func TestTriageScorer_FakeHaiku(t *testing.T) {
	backend := &funcScoringBackend{completeJSON: func(_ context.Context, _, _, _ string) ([]byte, error) {
		v := TriageVerdict{
			Score:        52,
			Verdict:      "TurboQuant WASM test",
			Tags:         "ai, quantization",
			RubricScores: map[string]int{"test": 52},
		}
		return json.Marshal(v)
	}}
	// Skip if eng template isn't on disk (CI / clean checkout).
	if _, _, err := loadProfileTemplate("eng"); err != nil {
		t.Skipf("eng template missing: %v", err)
	}
	fix := Fixture{ID: "test", Profile: "eng", Content: "some content"}
	got, err := triageScorer{backend: backend}.Score(fix)
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

// TestInitClaudeConfig_FallbackToAudio verifies that initClaudeConfig sets
// ytFallbackToAudio from the config. Absent YAML (Go zero-value false) must
// preserve the package default (true), not override it to false. POMO fix.
func TestInitClaudeConfig_FallbackToAudio(t *testing.T) {
	prev := ytFallbackToAudio
	t.Cleanup(func() { ytFallbackToAudio = prev })

	// Absent/zero config must NOT override the package default (true).
	// This was the POMO bug: Go bool zero-value killed the default.
	ytFallbackToAudio = true
	cfg := &ServerConfig{}
	cfg.YouTube.FallbackToAudio = false // simulates absent YAML field
	initClaudeConfig(cfg)
	if !ytFallbackToAudio {
		t.Error("ytFallbackToAudio should remain true when fallback_to_audio is absent/false in config (package default preserved)")
	}

	// Explicit true in config: sets ytFallbackToAudio = true.
	ytFallbackToAudio = false
	cfg.YouTube.FallbackToAudio = true
	initClaudeConfig(cfg)
	if !ytFallbackToAudio {
		t.Error("ytFallbackToAudio should be true when fallback_to_audio: true in config")
	}
}

// TestBuildClaudeArgs verifies the flag assembly for claude --print invocations.
// Regression: the Claude CLI removed --max-tokens (2026-04); buildClaudeArgs must
// never emit it. See score_async_eval_error in server.log.
func TestBuildClaudeArgs(t *testing.T) {
	tests := []struct {
		name string
		opts claudeExecOpts
		want []string // substrings that MUST appear
		deny []string // substrings that MUST NOT appear
	}{
		{
			name: "json_scoring_path",
			opts: claudeExecOpts{
				Model:        "claude-haiku-4-5-20251001",
				MaxTurns:     "3",
				Tools:        "",
				OutputFormat: "json",
				JSONSchema:   `{"type":"object"}`,
				SystemPrompt: "/tmp/sp.txt",
			},
			want: []string{
				"--print",
				"--model", "claude-haiku-4-5-20251001",
				"--max-turns", "3",
				"--tools", "",
				"--output-format", "json",
				"--json-schema",
				"--system-prompt-file", "/tmp/sp.txt",
				"--effort", "low",
				"--no-session-persistence",
			},
			deny: []string{"--max-tokens"},
		},
		{
			name: "vision_path_with_allowedTools",
			opts: claudeExecOpts{
				Model:        "claude-haiku-4-5-20251001",
				MaxTurns:     "3",
				AllowedTools: "Read",
				OutputFormat: "json",
				JSONSchema:   `{"type":"object"}`,
				SystemPrompt: "/tmp/sp.txt",
			},
			want: []string{
				"--allowedTools", "Read",
				"--output-format", "json",
			},
			deny: []string{"--max-tokens"},
		},
		{
			name: "minimal_plain_text",
			opts: claudeExecOpts{
				Model:        "claude-haiku-4-5-20251001",
				MaxTurns:     "1",
				Tools:        "",
				SystemPrompt: "/tmp/sp.txt",
			},
			want: []string{"--print", "--model", "--max-turns", "1"},
			deny: []string{"--max-tokens", "--output-format", "--json-schema"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildClaudeArgs(tt.opts)
			joined := strings.Join(args, " ")

			for _, w := range tt.want {
				found := false
				for _, a := range args {
					if a == w {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected %q in args: %s", w, joined)
				}
			}

			for _, d := range tt.deny {
				for _, a := range args {
					if a == d {
						t.Errorf("deprecated flag %q must not appear in args: %s", d, joined)
					}
				}
			}
		})
	}
}
