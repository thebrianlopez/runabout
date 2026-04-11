package main

// EPIC-044 M2 — Layer 1: typed YAML manifest contract for triage profiles.
//
// Replaces 7 hand-authored markdown profile prompts with one schema +
// one shared text/template. Each profile becomes a ~35 LOC YAML manifest;
// the renderer produces a system prompt that is near-byte-equivalent to
// the legacy .md so the EPIC-043 fixture corpus stays valid.
//
// Co-owned with personal-docs-agent: this file ships the schema and
// renderer; personal-docs-agent migrates docs/prompts/profiles/*.md →
// *.yaml against this contract.
//
// Render contract:
//   - persona_body and history blocks are inserted as raw strings via
//     {{.PersonaBody}} / {{.History}} — text/template does NOT re-parse
//     data values, so literal {{FILL: ...}} placeholders in travel/life
//     survive verbatim (Discovery Section B risk #2).
//   - Only the template *source* below contains template actions.
//   - rubric_scores axis names are profile-local; the schema enforces
//     5 entries summing to 100, but does not constrain the names.

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// ProfileManifest is the v1 schema for docs/prompts/profiles/*.yaml.
type ProfileManifest struct {
	ID             string         `yaml:"id"`
	Version        int            `yaml:"version"`
	SchemaVersion  string         `yaml:"schema_version"`
	PersonaIntro   string         `yaml:"persona_intro"`
	NoiseGate      NoiseGate      `yaml:"noise_gate"`
	PersonaBody    string         `yaml:"persona_body"`
	VerdictPrompt  string         `yaml:"verdict_prompt"`
	Rubric         []RubricAxis   `yaml:"rubric"`
	ActionItems    ActionItems    `yaml:"action_items"`
	KeyFacts       KeyFacts       `yaml:"key_facts"`
	History        *HistoryBlock  `yaml:"history,omitempty"`
}

type NoiseGate struct {
	MinChars  int    `yaml:"min_chars"`
	SkipLabel string `yaml:"skip_label"`
	// Condition is the descriptor list that follows
	// "If the content is < N characters," in the rendered prompt.
	// Optional override; defaults to the canonical phrasing.
	Condition string `yaml:"condition,omitempty"`
}

type RubricAxis struct {
	Name      string `yaml:"name"`
	Weight    int    `yaml:"weight"`
	Rationale string `yaml:"rationale"`
}

type ActionItems struct {
	Count       string   `yaml:"count"`
	HorizonDays int      `yaml:"horizon_days"`
	Examples    []string `yaml:"examples,omitempty"`
	// Lead optionally overrides the templated "{count} concrete next
	// steps within {horizon_days} days." sentence. When set, it is used
	// verbatim (the Examples block is still appended).
	Lead string `yaml:"lead,omitempty"`
}

type KeyFacts struct {
	Count string   `yaml:"count"`
	Focus []string `yaml:"focus,omitempty"`
	// Intro optionally overrides the line that introduces the bullet
	// list. Defaults to "{count} bullet points:".
	Intro string `yaml:"intro,omitempty"`
}

// HistoryBlock is the optional dining-only escape hatch (Discovery
// Section B divergence #1). Rendered as raw markdown between
// persona_body and Output Format.
type HistoryBlock struct {
	Entries         []string `yaml:"entries,omitempty"`
	ScoringOverride string   `yaml:"scoring_override,omitempty"`
	// Intro is an optional lead-in line rendered between the
	// "## My History" heading and the first bullet (e.g. dining's
	// "Places I've been and loved — score these 90+ on revisit ...").
	Intro string `yaml:"intro,omitempty"`
}

// Validate enforces the schema invariants `linkari profile lint` (M3)
// will also check at hook time. Called from LoadProfileManifest.
func (m *ProfileManifest) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("id required")
	}
	if m.Version <= 0 {
		return fmt.Errorf("version must be > 0")
	}
	if m.SchemaVersion != "triage_verdict_v1" {
		return fmt.Errorf("schema_version must be triage_verdict_v1, got %q", m.SchemaVersion)
	}
	if strings.TrimSpace(m.PersonaIntro) == "" {
		return fmt.Errorf("persona_intro required")
	}
	if strings.TrimSpace(m.PersonaBody) == "" {
		return fmt.Errorf("persona_body required")
	}
	if m.NoiseGate.MinChars <= 0 {
		return fmt.Errorf("noise_gate.min_chars must be > 0")
	}
	if strings.TrimSpace(m.NoiseGate.SkipLabel) == "" {
		return fmt.Errorf("noise_gate.skip_label required")
	}
	if len(m.Rubric) != 5 {
		return fmt.Errorf("rubric must have exactly 5 axes, got %d", len(m.Rubric))
	}
	sum := 0
	for i, ax := range m.Rubric {
		if strings.TrimSpace(ax.Name) == "" {
			return fmt.Errorf("rubric[%d].name required", i)
		}
		if ax.Weight <= 0 {
			return fmt.Errorf("rubric[%d].weight (%s) must be > 0", i, ax.Name)
		}
		if strings.TrimSpace(ax.Rationale) == "" {
			return fmt.Errorf("rubric[%d].rationale (%s) required", i, ax.Name)
		}
		sum += ax.Weight
	}
	if sum != 100 {
		return fmt.Errorf("rubric weights must sum to 100, got %d", sum)
	}
	if strings.TrimSpace(m.ActionItems.Count) == "" {
		return fmt.Errorf("action_items.count required")
	}
	if m.ActionItems.HorizonDays <= 0 {
		return fmt.Errorf("action_items.horizon_days must be > 0")
	}
	if strings.TrimSpace(m.KeyFacts.Count) == "" {
		return fmt.Errorf("key_facts.count required")
	}
	return nil
}

// LoadProfileManifest reads a YAML manifest file and validates it.
func LoadProfileManifest(path string) (*ProfileManifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m ProfileManifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("yaml decode %s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &m, nil
}

// systemPromptTemplate is the shared text/template body that renders
// every profile manifest into a near-byte-equivalent of the legacy .md
// system prompt. Whitespace is intentionally lined up to match the
// existing files within 0–2 chars of drift (Discovery Section B point 5).
//
// CRITICAL: persona_body and history are inserted via {{.PersonaBody}} /
// {{.History}} — those data values are NOT re-parsed by text/template,
// so any literal {{FILL: ...}} markers in travel/life survive intact.
const systemPromptTemplate = `{{.PersonaIntro}}

## Noise Gate

If the content is < {{.NoiseGate.MinChars}} characters, {{.NoiseGate.Condition}}:
- Score: 0/100
- Verdict: Skip ({{.NoiseGate.SkipLabel}})
Do not generate a full evaluation.

{{.PersonaBody}}
{{- if .History}}

## My History
{{- if .History.Intro}}

{{.History.Intro}}
{{- end}}
{{- range .History.Entries}}
- {{.}}
{{- end}}
{{- if .History.ScoringOverride}}

{{.History.ScoringOverride}}
{{- end}}
{{- end}}

## Output Format

## Verdict
One-line: {{.VerdictPrompt}}

## Score: X/100

| Component | Weight | Score | Rationale |
|-----------|--------|-------|-----------|
{{- range .Rubric}}
| {{.Name}} | 0–{{.Weight}} | | {{.Rationale}} |
{{- end}}
| **Total** | **0–100** | | |

## Action Items
{{.ActionItems.Lead}}{{if .ActionItems.Examples}} Examples: {{joinExamples .ActionItems.Examples}}.{{end}}

## Key Facts
{{.KeyFacts.Intro}}
{{- range .KeyFacts.Focus}}
- {{.}}
{{- end}}

Be concise. Output markdown. No preamble.
`

var systemPromptTmpl = template.Must(
	template.New("profile").
		Funcs(template.FuncMap{
			"joinExamples": joinExamplesQuoted,
		}).
		Parse(systemPromptTemplate),
)

// joinExamplesQuoted formats action-item examples to match the legacy
// .md form: `"a", "b", "c"`.
func joinExamplesQuoted(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%q", x)
	}
	return strings.Join(parts, ", ")
}

// Render produces the system prompt for this manifest. The output is
// fed verbatim to `claude --print --system-prompt`.
func (m *ProfileManifest) Render() (string, error) {
	// Trim trailing newline on PersonaBody so the template's blank-line
	// gap above "## Output Format" stays consistent regardless of how
	// the YAML author terminated the block scalar.
	view := *m
	view.PersonaBody = strings.TrimRight(m.PersonaBody, "\n")
	view.PersonaIntro = strings.TrimRight(m.PersonaIntro, "\n")

	// Apply defaults for the optional override fields so YAMLs that
	// omit them still produce sensible output. Profile authors override
	// these to match their legacy .md phrasing exactly.
	if view.NoiseGate.Condition == "" {
		view.NoiseGate.Condition = "a login wall, a 404, a checkout/booking page, or a placeholder domain (e.g., example.com)"
	}
	if view.ActionItems.Lead == "" {
		view.ActionItems.Lead = fmt.Sprintf("%s concrete next steps within %d days.",
			view.ActionItems.Count, view.ActionItems.HorizonDays)
	}
	if view.KeyFacts.Intro == "" {
		view.KeyFacts.Intro = fmt.Sprintf("%s bullet points:", view.KeyFacts.Count)
	}

	var buf bytes.Buffer
	if err := systemPromptTmpl.Execute(&buf, &view); err != nil {
		return "", fmt.Errorf("render manifest %s: %w", m.ID, err)
	}
	return buf.String(), nil
}
