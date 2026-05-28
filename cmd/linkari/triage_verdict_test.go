package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canonicalVerdict is the snapshot fixture used by RenderMarkdown +
// envelope-parse tests. Keep field order stable so the schema-validated
// JSON form serializes deterministically.
var canonicalVerdict = TriageVerdict{
	Score:   52,
	Verdict: "TurboQuant WASM—mature 3-bit/dim vector quantization. Niche relevance to RAG.",
	ActionItems: []string{
		"Read TurboQuant paper section 3",
		"Prototype WASM bindings in runabout",
	},
	Tags: "ai, quantization",
	RubricScores: map[string]int{
		"Novelty":               14,
		"Operational Relevance": 8,
		"Strategic Stack Fit":   15,
		"Learnability":          10,
		"Career Leverage":       5,
	},
	Profile:        "eng",
	ProfileVersion: 1,
}

func TestTriageVerdict_Validate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*TriageVerdict)
		want bool // true => expect error
	}{
		{"clean", func(v *TriageVerdict) {}, false},
		{"score-low", func(v *TriageVerdict) { v.Score = -1 }, true},
		{"score-high", func(v *TriageVerdict) { v.Score = 101 }, true},
		{"empty-verdict", func(v *TriageVerdict) { v.Verdict = "  " }, true},
		{"no-rubric", func(v *TriageVerdict) { v.RubricScores = nil }, true},
		{"rubric-out-of-range", func(v *TriageVerdict) { v.RubricScores["x"] = 200 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := canonicalVerdict
			// deep-copy the rubric map to avoid cross-test mutation
			v.RubricScores = map[string]int{}
			for k, val := range canonicalVerdict.RubricScores {
				v.RubricScores[k] = val
			}
			tc.mut(&v)
			err := v.validate()
			if tc.want && err == nil {
				t.Fatalf("expected error")
			}
			if !tc.want && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestRenderMarkdown_Snapshot locks the README-append shape so any future
// rubric-table or action-item churn is a deliberate diff, not silent drift.
// Update the expected block intentionally; do not auto-format.
func TestRenderMarkdown_Snapshot(t *testing.T) {
	got := canonicalVerdict.RenderMarkdown()
	const want = "## Verdict TurboQuant WASM—mature 3-bit/dim vector quantization. Niche relevance to RAG.\n\n" +
		"## Score: 52/100\n" +
		"\n| Component | Score |\n|---|---|\n" +
		"| Career Leverage | 5 |\n" +
		"| Learnability | 10 |\n" +
		"| Novelty | 14 |\n" +
		"| Operational Relevance | 8 |\n" +
		"| Strategic Stack Fit | 15 |\n" +
		"\n## Action Items\n" +
		"- Read TurboQuant paper section 3\n" +
		"- Prototype WASM bindings in runabout\n" +
		"\nTags: ai, quantization\n"
	if got != want {
		t.Fatalf("RenderMarkdown drift:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestTriageVerdict_UnmarshalJSON_NestedObjects(t *testing.T) {
	// Haiku sometimes returns rubric_scores as {"Novelty": {"score": 15, "rationale": "..."}}
	// instead of flat integers. The custom UnmarshalJSON must coerce both forms.
	raw := `{
		"score": 52,
		"verdict": "test",
		"rubric_scores": {
			"Novelty": {"score": 15, "rationale": "interesting"},
			"Learnability": 10,
			"Career Leverage": {"score": 5}
		}
	}`
	var v TriageVerdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v.RubricScores["Novelty"] != 15 {
		t.Errorf("Novelty = %d, want 15", v.RubricScores["Novelty"])
	}
	if v.RubricScores["Learnability"] != 10 {
		t.Errorf("Learnability = %d, want 10", v.RubricScores["Learnability"])
	}
	if v.RubricScores["Career Leverage"] != 5 {
		t.Errorf("Career Leverage = %d, want 5", v.RubricScores["Career Leverage"])
	}
}

func TestParseHaikuEnvelope_Bare(t *testing.T) {
	b, err := json.Marshal(canonicalVerdict)
	if err != nil {
		t.Fatal(err)
	}
	v, _, err := parseHaikuEnvelope(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.Score != 52 || v.Profile != "eng" {
		t.Errorf("got %+v", v)
	}
}

func TestParseHaikuEnvelope_StringResult(t *testing.T) {
	inner, _ := json.Marshal(canonicalVerdict)
	env := map[string]any{
		"type":     "result",
		"subtype":  "success",
		"is_error": false,
		"result":   string(inner),
	}
	b, _ := json.Marshal(env)
	v, _, err := parseHaikuEnvelope(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.Score != 52 {
		t.Errorf("score = %d", v.Score)
	}
}

func TestParseHaikuEnvelope_ObjectResult(t *testing.T) {
	inner, _ := json.Marshal(canonicalVerdict)
	env := map[string]json.RawMessage{
		"type":     json.RawMessage(`"result"`),
		"is_error": json.RawMessage(`false`),
		"result":   inner,
	}
	b, _ := json.Marshal(env)
	v, _, err := parseHaikuEnvelope(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.Score != 52 {
		t.Errorf("score = %d", v.Score)
	}
}

func TestParseHaikuEnvelope_StripsCodeFence(t *testing.T) {
	inner, _ := json.Marshal(canonicalVerdict)
	fenced := "```json\n" + string(inner) + "\n```"
	env := map[string]any{
		"type":     "result",
		"is_error": false,
		"result":   fenced,
	}
	b, _ := json.Marshal(env)
	v, _, err := parseHaikuEnvelope(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.Score != 52 {
		t.Errorf("score = %d", v.Score)
	}
}

func TestParseHaikuEnvelope_ExtractsMeta(t *testing.T) {
	inner, _ := json.Marshal(canonicalVerdict)
	env := map[string]any{
		"type":           "result",
		"is_error":       false,
		"result":         string(inner),
		"total_cost_usd": 0.00123,
		"usage": map[string]int{
			"input_tokens":  500,
			"output_tokens": 100,
		},
	}
	b, _ := json.Marshal(env)
	v, meta, err := parseHaikuEnvelope(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.Score != 52 {
		t.Errorf("score = %d", v.Score)
	}
	if meta == nil {
		t.Fatal("meta is nil")
	}
	if meta.CostUSD != 0.00123 {
		t.Errorf("cost = %f, want 0.00123", meta.CostUSD)
	}
	if meta.Usage == nil {
		t.Fatal("usage is nil")
	}
	if meta.Usage.InputTokens != 500 {
		t.Errorf("input_tokens = %d, want 500", meta.Usage.InputTokens)
	}
	if meta.Usage.OutputTokens != 100 {
		t.Errorf("output_tokens = %d, want 100", meta.Usage.OutputTokens)
	}
}

func TestParseHaikuEnvelope_ErrorEnvelope(t *testing.T) {
	b := []byte(`{"type":"result","subtype":"error","is_error":true,"result":""}`)
	if _, _, err := parseHaikuEnvelope(b); err == nil {
		t.Fatal("expected error")
	}
}

func TestHaikuVerdictWithRepair_FirstSucceeds(t *testing.T) {
	orig := execHaikuJSON
	defer func() { execHaikuJSON = orig }()
	calls := 0
	execHaikuJSON = func(_ context.Context, _, _, _ string) ([]byte, error) {
		calls++
		return json.Marshal(canonicalVerdict)
	}
	v, _, err := haikuVerdictWithRepair(context.Background(), "sys", "content")
	if err != nil {
		t.Fatalf("verdict: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if v.Score != 52 {
		t.Errorf("score = %d", v.Score)
	}
}

func TestHaikuVerdictWithRepair_RepairsOnce(t *testing.T) {
	orig := execHaikuJSON
	defer func() { execHaikuJSON = orig }()
	calls := 0
	execHaikuJSON = func(_ context.Context, sys, _, _ string) ([]byte, error) {
		calls++
		if calls == 1 {
			// Return a malformed envelope (empty verdict triggers validate fail).
			bad := canonicalVerdict
			bad.Verdict = ""
			return json.Marshal(bad)
		}
		// Repair turn must include the prior error in the system prompt.
		if !strings.Contains(sys, "failed schema validation") {
			t.Errorf("repair sys prompt missing context: %q", sys)
		}
		return json.Marshal(canonicalVerdict)
	}
	v, _, err := haikuVerdictWithRepair(context.Background(), "sys", "content")
	if err != nil {
		t.Fatalf("verdict: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if v.Verdict == "" {
		t.Error("verdict empty after repair")
	}
}

func TestHaikuVerdictWithRepair_GivesUpAfterRepair(t *testing.T) {
	orig := execHaikuJSON
	defer func() { execHaikuJSON = orig }()
	execHaikuJSON = func(_ context.Context, _, _, _ string) ([]byte, error) {
		bad := canonicalVerdict
		bad.Score = 999
		return json.Marshal(bad)
	}
	if _, _, err := haikuVerdictWithRepair(context.Background(), "sys", "content"); err == nil {
		t.Fatal("expected repair-give-up error")
	}
}

func TestHaikuVerdictWithRepair_ExecError(t *testing.T) {
	orig := execHaikuJSON
	defer func() { execHaikuJSON = orig }()
	execHaikuJSON = func(_ context.Context, _, _, _ string) ([]byte, error) {
		return nil, errors.New("boom")
	}
	if _, _, err := haikuVerdictWithRepair(context.Background(), "sys", "content"); err == nil {
		t.Fatal("expected exec error to propagate")
	}
}

// TestWriteScoreSidecar_Additive locks the EPIC-044 invariant: when extras
// is non-nil the v1 fields stay byte-stable and the new fields appear
// alongside them. cmd_eval.go captureFromWorkspace decodes by named field,
// so unknown keys are silently ignored on read — this test guarantees we
// never accidentally rename or drop a v1 key.
func TestWriteScoreSidecar_Additive(t *testing.T) {
	ws := t.TempDir()
	orig := nowRFC3339UTC
	defer func() { nowRFC3339UTC = orig }()
	nowRFC3339UTC = func() string { return "2026-04-07T10:14:45Z" }

	extras := &sidecarExtras{
		SchemaVersion:  "triage_verdict_v1",
		ProfileVersion: 1,
		RubricScores: map[string]int{
			"Novelty": 14,
		},
	}
	if err := writeScoreSidecar(ws, 52, "looks fine", "slug-x", "eng", "https://example.com", extras); err != nil {
		t.Fatalf("write: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(ws, "_score.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	// v1 invariants
	for k, want := range map[string]any{
		"score":     float64(52),
		"verdict":   "looks fine",
		"slug":      "slug-x",
		"profile":   "eng",
		"url":       "https://example.com",
		"scored_at": "2026-04-07T10:14:45Z",
	} {
		if got[k] != want {
			t.Errorf("v1 field %q = %v, want %v", k, got[k], want)
		}
	}
	// v2 additive
	if got["schema_version"] != "triage_verdict_v1" {
		t.Errorf("schema_version = %v", got["schema_version"])
	}
	if got["profile_version"].(float64) != 1 {
		t.Errorf("profile_version = %v", got["profile_version"])
	}
	rs, ok := got["rubric_scores"].(map[string]any)
	if !ok {
		t.Fatalf("rubric_scores wrong type: %T", got["rubric_scores"])
	}
	if rs["Novelty"].(float64) != 14 {
		t.Errorf("rubric_scores[Novelty] = %v", rs["Novelty"])
	}
}

// TestWriteScoreSidecar_NilExtras_NoLeakage ensures that when extras is nil
// none of the additive keys appear at all (cmd_eval.go ignores unknown keys
// but downstream consumers might be stricter).
func TestWriteScoreSidecar_NilExtras_NoLeakage(t *testing.T) {
	ws := t.TempDir()
	orig := nowRFC3339UTC
	defer func() { nowRFC3339UTC = orig }()
	nowRFC3339UTC = func() string { return "2026-04-07T10:14:45Z" }

	if err := writeScoreSidecar(ws, 10, "v", "s", "p", "u", nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(ws, "_score.json"))
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	for _, k := range []string{"schema_version", "profile_version", "rubric_scores"} {
		if _, present := got[k]; present {
			t.Errorf("nil-extras leaked %q", k)
		}
	}
	if len(got) != 6 {
		t.Errorf("v1 sidecar key count = %d, want 6: %v", len(got), got)
	}
}
