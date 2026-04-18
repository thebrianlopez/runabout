package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validEngManifest() *ProfileManifest {
	return &ProfileManifest{
		ID:            "eng",
		Version:       1,
		SchemaVersion: "triage_verdict_v1",
		PersonaIntro:  "You are a **technical triage assistant** for an AI/ML Architect.",
		NoiseGate: NoiseGate{
			MinChars:  200,
			SkipLabel: "no extractable technical content",
		},
		PersonaBody: "## My Context\n\n**Role:** AI/ML Architect.\n",
		VerdictPrompt: "what is this, and does it move the needle for an ML Architect at this stage?",
		Rubric: []RubricAxis{
			{Name: "Novelty", Weight: 20, Rationale: "How unexplored is this?"},
			{Name: "Operational Relevance", Weight: 25, Rationale: "Does this address active tools?"},
			{Name: "Strategic Stack Fit", Weight: 25, Rationale: "Alignment with stack."},
			{Name: "Learnability", Weight: 15, Rationale: "Reproducible examples?"},
			{Name: "Career Leverage", Weight: 15, Rationale: "Builds visible expertise?"},
		},
		ActionItems: ActionItems{
			Count:       "2-3",
			HorizonDays: 7,
			Examples:    []string{"read section X", "prototype Y"},
		},
		KeyFacts: KeyFacts{
			Count: "3-5",
			Focus: []string{"Architecture patterns", "Performance characteristics"},
		},
	}
}

func TestProfileManifestValidate(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		m := validEngManifest()
		if err := m.Validate(); err != nil {
			t.Fatalf("expected valid, got %v", err)
		}
	})

	cases := []struct {
		name  string
		mut   func(m *ProfileManifest)
		wants string
	}{
		{"missing id", func(m *ProfileManifest) { m.ID = "" }, "id required"},
		{"bad version", func(m *ProfileManifest) { m.Version = 0 }, "version"},
		{"bad schema", func(m *ProfileManifest) { m.SchemaVersion = "v2" }, "schema_version"},
		{"missing persona_intro", func(m *ProfileManifest) { m.PersonaIntro = "" }, "persona_intro"},
		{"missing persona_body", func(m *ProfileManifest) { m.PersonaBody = "" }, "persona_body"},
		{"bad min_chars", func(m *ProfileManifest) { m.NoiseGate.MinChars = 0 }, "min_chars"},
		{"missing skip_label", func(m *ProfileManifest) { m.NoiseGate.SkipLabel = "" }, "skip_label"},
		{"wrong rubric count", func(m *ProfileManifest) { m.Rubric = m.Rubric[:4] }, "5 axes"},
		{"weights wrong sum", func(m *ProfileManifest) { m.Rubric[0].Weight = 21 }, "sum to 100"},
		{"missing rubric name", func(m *ProfileManifest) { m.Rubric[0].Name = "" }, "name required"},
		{"zero weight", func(m *ProfileManifest) { m.Rubric[0].Weight = 0 }, "weight"},
		{"missing rationale", func(m *ProfileManifest) { m.Rubric[0].Rationale = "" }, "rationale"},
		{"missing action count", func(m *ProfileManifest) { m.ActionItems.Count = "" }, "action_items.count"},
		{"bad horizon", func(m *ProfileManifest) { m.ActionItems.HorizonDays = 0 }, "horizon_days"},
		{"missing key_facts count", func(m *ProfileManifest) { m.KeyFacts.Count = "" }, "key_facts.count"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validEngManifest()
			tc.mut(m)
			err := m.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wants)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("expected error containing %q, got %q", tc.wants, err.Error())
			}
		})
	}
}

func TestProfileManifestRender(t *testing.T) {
	m := validEngManifest()
	out, err := m.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// Spot-check load-bearing chunks the legacy .md emits.
	want := []string{
		"You are a **technical triage assistant**",
		"## Noise Gate",
		"If the content is < 200 characters",
		"Verdict: Skip (no extractable technical content)",
		"## My Context",
		"## Output Format",
		"## Verdict",
		"One-line: what is this, and does it move the needle",
		"## Score: X/100",
		"| Component | Weight | Score | Rationale |",
		"| Novelty | 0–20 | | How unexplored is this? |",
		"| **Total** | **0–100** | | |",
		"## Action Items",
		"2-3 concrete next steps within 7 days.",
		`Examples: "read section X", "prototype Y".`,
		"## Key Facts",
		"3-5 bullet points",
		"- Architecture patterns",
		"Be concise. Output markdown. No preamble.",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("rendered output missing %q\n--- output ---\n%s", w, out)
		}
	}
}

func TestProfileManifestRenderPreservesFillLiterals(t *testing.T) {
	// Discovery Section B risk #2: travel/life have literal {{FILL: ...}}
	// markers that must NOT be re-parsed by text/template.
	m := validEngManifest()
	m.PersonaBody = "## My Context\n\nLocation: {{FILL: city}}\nGoals: {{FILL: 5-year goals}}\n"
	out, err := m.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "{{FILL: city}}") {
		t.Errorf("FILL marker stripped: %s", out)
	}
	if !strings.Contains(out, "{{FILL: 5-year goals}}") {
		t.Errorf("FILL marker stripped: %s", out)
	}
}

func TestProfileManifestRenderWithHistory(t *testing.T) {
	// Dining-only escape hatch.
	m := validEngManifest()
	m.History = &HistoryBlock{
		Entries:         []string{"Honeybear", "Kasama"},
		ScoringOverride: "Matching a known favorite boosts Local Relevance to near-max.",
	}
	out, err := m.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "## My History") {
		t.Errorf("history section missing")
	}
	if !strings.Contains(out, "- Honeybear") {
		t.Errorf("history entry missing")
	}
	if !strings.Contains(out, "boosts Local Relevance") {
		t.Errorf("scoring override missing")
	}
}

func validVisionManifest() *ProfileManifest {
	return &ProfileManifest{
		ID:            "image_triage",
		Version:       1,
		SchemaVersion: "triage_verdict_v1",
		ContentModes:  []string{"vision"},
		PersonaIntro:  "You are a **visual content triage assistant**.",
		NoiseGate: NoiseGate{
			MinChars:  0,
			SkipLabel: "image could not be decoded or is blank",
		},
		PersonaBody:   "## Task\n\nTriage shared images.\n",
		VerdictPrompt: "what is this image?",
		Rubric: []RubricAxis{
			{Name: "Visual Clarity", Weight: 40, Rationale: "Can the content be identified?"},
			{Name: "Profile Signal", Weight: 35, Rationale: "How confidently can this be routed?"},
			{Name: "Actionability", Weight: 25, Rationale: "Does it contain actionable info?"},
		},
		ActionItems: ActionItems{Count: "1-2", HorizonDays: 7},
		KeyFacts:    KeyFacts{Count: "2-3"},
	}
}

func TestProfileManifestValidateVisionProfile(t *testing.T) {
	t.Run("valid vision-only profile", func(t *testing.T) {
		m := validVisionManifest()
		if err := m.Validate(); err != nil {
			t.Fatalf("expected valid, got %v", err)
		}
	})

	t.Run("vision profile allows min_chars=0", func(t *testing.T) {
		m := validVisionManifest()
		m.NoiseGate.MinChars = 0
		if err := m.Validate(); err != nil {
			t.Fatalf("vision profile should allow min_chars=0, got %v", err)
		}
	})

	t.Run("vision profile allows flexible axis count", func(t *testing.T) {
		m := validVisionManifest()
		if len(m.Rubric) == 5 {
			t.Fatal("test fixture should have != 5 axes to test flexibility")
		}
		if err := m.Validate(); err != nil {
			t.Fatalf("vision profile should allow non-5 rubric axes, got %v", err)
		}
	})

	t.Run("text profile still requires 5 axes", func(t *testing.T) {
		m := validEngManifest()
		m.Rubric = m.Rubric[:3]
		err := m.Validate()
		if err == nil || !strings.Contains(err.Error(), "5 axes") {
			t.Fatalf("expected 5 axes error, got %v", err)
		}
	})

	t.Run("non-text profile requires some rubric", func(t *testing.T) {
		m := validVisionManifest()
		m.Rubric = nil
		err := m.Validate()
		if err == nil || !strings.Contains(err.Error(), "non-text profile must have") {
			t.Fatalf("expected rubric required error, got %v", err)
		}
	})
}

func TestProfileManifestValidateVisionRubric(t *testing.T) {
	m := validEngManifest()
	m.VisionRubric = []RubricAxis{
		{Name: "Clarity", Weight: 50, Rationale: "clear?"},
		{Name: "Signal", Weight: 50, Rationale: "signal?"},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("expected valid with vision_rubric, got %v", err)
	}

	t.Run("bad vision_rubric weights", func(t *testing.T) {
		m := validEngManifest()
		m.VisionRubric = []RubricAxis{
			{Name: "A", Weight: 30, Rationale: "a"},
			{Name: "B", Weight: 30, Rationale: "b"},
		}
		err := m.Validate()
		if err == nil || !strings.Contains(err.Error(), "vision_rubric weights must sum to 100") {
			t.Fatalf("expected sum error, got %v", err)
		}
	})
}

func TestProfileManifestValidateAudioRubric(t *testing.T) {
	m := validEngManifest()
	m.AudioRubric = []RubricAxis{
		{Name: "Clarity", Weight: 20, Rationale: "clear?"},
		{Name: "Actionability", Weight: 20, Rationale: "actionable?"},
		{Name: "Novelty", Weight: 20, Rationale: "new?"},
		{Name: "Urgency", Weight: 20, Rationale: "urgent?"},
		{Name: "Topic Match", Weight: 20, Rationale: "relevant?"},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("expected valid with audio_rubric, got %v", err)
	}
}

func TestRenderForModeVision(t *testing.T) {
	m := validEngManifest()
	m.VisionRubric = []RubricAxis{
		{Name: "Visual Clarity", Weight: 60, Rationale: "clear image?"},
		{Name: "Actionability", Weight: 40, Rationale: "actionable?"},
	}
	m.VisionPersonaIntro = "You are a **visual triage assistant**."

	out, err := m.RenderForMode("vision")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "Visual Clarity") {
		t.Error("vision rubric axis missing")
	}
	if !strings.Contains(out, "visual triage assistant") {
		t.Error("vision persona intro missing")
	}
	if strings.Contains(out, "Novelty") {
		t.Error("text rubric axis should not appear in vision mode")
	}
}

func TestRenderForModeAudio(t *testing.T) {
	m := validEngManifest()
	m.AudioRubric = []RubricAxis{
		{Name: "Clarity", Weight: 50, Rationale: "clear?"},
		{Name: "Urgency", Weight: 50, Rationale: "urgent?"},
	}

	out, err := m.RenderForMode("audio")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "Clarity") || !strings.Contains(out, "Urgency") {
		t.Error("audio rubric axes missing")
	}
	// PersonaIntro should NOT change for audio mode (no VisionPersonaIntro used)
	if !strings.Contains(out, "technical triage assistant") {
		t.Error("standard persona intro should be used for audio mode")
	}
}

func TestRenderForModeFallback(t *testing.T) {
	m := validEngManifest()
	// No vision/audio rubrics — should fall back to primary rubric
	out, err := m.RenderForMode("vision")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "Novelty") {
		t.Error("should fall back to primary rubric when vision_rubric absent")
	}
}

func TestRenderForModeText(t *testing.T) {
	m := validEngManifest()
	m.VisionRubric = []RubricAxis{
		{Name: "Visual Clarity", Weight: 100, Rationale: "clear?"},
	}
	out, err := m.RenderForMode("text")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "Novelty") {
		t.Error("text mode should use primary rubric")
	}
	if strings.Contains(out, "Visual Clarity") {
		t.Error("text mode should not use vision rubric")
	}
}

func TestLoadProfileManifestYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eng.yaml")
	yaml := `id: eng
version: 1
schema_version: triage_verdict_v1
persona_intro: "You are a triage assistant."
noise_gate:
  min_chars: 200
  skip_label: "no extractable content"
persona_body: |
  ## My Context

  Test body.
verdict_prompt: "what is this?"
rubric:
  - name: A
    weight: 20
    rationale: "a"
  - name: B
    weight: 20
    rationale: "b"
  - name: C
    weight: 20
    rationale: "c"
  - name: D
    weight: 20
    rationale: "d"
  - name: E
    weight: 20
    rationale: "e"
action_items:
  count: "2-3"
  horizon_days: 7
key_facts:
  count: "3-5"
`
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadProfileManifest(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m.ID != "eng" || m.Version != 1 {
		t.Errorf("unexpected: %+v", m)
	}
	if _, err := m.Render(); err != nil {
		t.Errorf("render: %v", err)
	}
}

