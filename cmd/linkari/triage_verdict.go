package main

// EPIC-044 M1 — Layer 2: typed JSON contract for Haiku triage output.
//
// This file owns the post-EPIC-043 successor to parseTriageMarkdown's regex
// path. The flow is:
//
//   claude --print --output-format json --json-schema triage_verdict_v1.json ...
//          → CLI grammar-validates against the schema
//          → Go unwraps the result envelope into TriageVerdict
//          → validate() defends against any best-effort validator slack
//          → one repair turn if parse/validate fails (error pasted into prompt)
//          → RenderMarkdown() emits the README append the existing pipeline
//            already knows how to consume
//
// Gated behind the `--use-json` flag on `linkari triage` so per-profile
// staged rollout can compare JSON-mode output against the regex baseline
// without bricking the queue. Once rollout is complete the regex parser
// goes away in a follow-up.

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

//go:embed triage_verdict_v1.json
var triageVerdictSchema string

// TriageVerdict is the typed contract Haiku is asked to emit. Field tags
// match the schema in triage_verdict_v1.json byte-for-byte.
type TriageVerdict struct {
	Score          int            `json:"score"`
	Verdict        string         `json:"verdict"`
	ActionItems    []string       `json:"action_items,omitempty"`
	Tags           string         `json:"tags,omitempty"`
	RubricScores   map[string]int `json:"rubric_scores"`
	Profile        string         `json:"profile,omitempty"`
	ProfileVersion int            `json:"profile_version,omitempty"`
}

// validate is a defense-in-depth pass on top of the CLI's --json-schema
// validator. The CLI reference describes --json-schema as "validated JSON
// output matching a JSON Schema" but does not specify whether the validator
// is grammar-constrained decoding or best-effort post-hoc check. Until M1's
// day-one smoke test confirms which, every field that the schema constrains
// gets re-checked here.
func (v TriageVerdict) validate() error {
	if v.Score < 0 || v.Score > 100 {
		return fmt.Errorf("score %d out of [0,100]", v.Score)
	}
	if strings.TrimSpace(v.Verdict) == "" {
		return fmt.Errorf("empty verdict")
	}
	if len(v.RubricScores) == 0 {
		return fmt.Errorf("rubric_scores missing")
	}
	for axis, score := range v.RubricScores {
		if score < 0 || score > 100 {
			return fmt.Errorf("rubric_scores[%q] = %d out of [0,100]", axis, score)
		}
	}
	return nil
}

// RenderMarkdown emits the README-append form for `appendTriageToReadme`.
// Snapshot-tested in triage_verdict_test.go — change with care.
func (v TriageVerdict) RenderMarkdown() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Verdict %s\n\n", strings.TrimSpace(v.Verdict))
	fmt.Fprintf(&sb, "## Score: %d/100\n", v.Score)
	if len(v.RubricScores) > 0 {
		sb.WriteString("\n| Component | Score |\n|---|---|\n")
		keys := make([]string, 0, len(v.RubricScores))
		for k := range v.RubricScores {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&sb, "| %s | %d |\n", k, v.RubricScores[k])
		}
	}
	if len(v.ActionItems) > 0 {
		sb.WriteString("\n## Action Items\n")
		for _, ai := range v.ActionItems {
			fmt.Fprintf(&sb, "- %s\n", ai)
		}
	}
	if strings.TrimSpace(v.Tags) != "" {
		fmt.Fprintf(&sb, "\nTags: %s\n", strings.TrimSpace(v.Tags))
	}
	return sb.String()
}

// execHaikuJSON is the indirection point tests stub. Production calls
// runClaudeHaikuJSON; tests can swap in a deterministic fake that returns
// raw envelope bytes.
var execHaikuJSON = runClaudeHaikuJSON

// runClaudeHaikuJSON shells the same `claude --print` invocation as
// runClaudeHaiku but adds `--output-format json --json-schema <schema>` so
// the CLI returns a result envelope containing schema-validated JSON.
//
// Returns the raw stdout bytes (the envelope) so the caller can decide
// whether to parse it directly or run a repair turn.
func runClaudeHaikuJSON(ctx context.Context, systemPrompt, content, schema string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "claude",
		"--print",
		"--model", claudeModel,
		"--max-turns", "1",
		"--tools", "",
		"--output-format", "json",
		"--json-schema", schema,
		"--system-prompt", systemPrompt,
	)
	cmd.Stdin = strings.NewReader(content)

	// Mirror fish `env -u CLAUDECODE` (cmd_triage.go runClaudeHaiku).
	env := haikuEnv()
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude exec: %w (stderr=%s)", err, strings.TrimSpace(stderr.String()))
	}
	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) == 0 {
		return nil, fmt.Errorf("claude returned empty output")
	}
	return out, nil
}

// parseHaikuEnvelope unwraps a `claude --output-format json` payload into
// a TriageVerdict. The envelope shape ships nested:
//
//	{"type":"result", "result":"<json-string-or-object>", "is_error":false, ...}
//
// Some claude versions return `result` as a JSON-encoded string, others as
// a raw object. Both forms are accepted. As a courtesy fallback the parser
// also accepts the bare TriageVerdict directly (so unit tests can pass a
// raw verdict without constructing the envelope).
func parseHaikuEnvelope(stdout []byte) (TriageVerdict, error) {
	stdout = bytes.TrimSpace(stdout)
	if len(stdout) == 0 {
		return TriageVerdict{}, fmt.Errorf("empty envelope")
	}

	// Bare verdict shortcut (test/dev path).
	var bare TriageVerdict
	if err := json.Unmarshal(stdout, &bare); err == nil && len(bare.RubricScores) > 0 {
		return bare, bare.validate()
	}

	var env struct {
		Type    string          `json:"type"`
		Subtype string          `json:"subtype"`
		Result  json.RawMessage `json:"result"`
		IsError bool            `json:"is_error"`
	}
	if err := json.Unmarshal(stdout, &env); err != nil {
		return TriageVerdict{}, fmt.Errorf("envelope decode: %w", err)
	}
	if env.IsError {
		return TriageVerdict{}, fmt.Errorf("claude error envelope (subtype=%s)", env.Subtype)
	}
	if len(env.Result) == 0 {
		return TriageVerdict{}, fmt.Errorf("envelope has empty result")
	}

	raw := bytes.TrimSpace(env.Result)
	// `result` may be a JSON string containing JSON. Unwrap once.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return TriageVerdict{}, fmt.Errorf("envelope result string: %w", err)
		}
		raw = bytes.TrimSpace([]byte(s))
		// Strip ```json fences if Haiku wrapped it.
		raw = stripCodeFence(raw)
	}

	var v TriageVerdict
	if err := json.Unmarshal(raw, &v); err != nil {
		return TriageVerdict{}, fmt.Errorf("verdict decode: %w (body=%s)", err, truncateForErr(raw))
	}
	if err := v.validate(); err != nil {
		return TriageVerdict{}, fmt.Errorf("verdict validate: %w", err)
	}
	return v, nil
}

// stripCodeFence trims a leading ```json / trailing ``` fence pair, if present.
func stripCodeFence(b []byte) []byte {
	b = bytes.TrimSpace(b)
	if !bytes.HasPrefix(b, []byte("```")) {
		return b
	}
	// drop first line (``` or ```json)
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[i+1:]
	}
	if i := bytes.LastIndex(b, []byte("```")); i >= 0 {
		b = b[:i]
	}
	return bytes.TrimSpace(b)
}

func truncateForErr(b []byte) string {
	const max = 200
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}

// haikuVerdictWithRepair calls the JSON Haiku path and, on parse/validate
// failure, retries exactly once with the prior error pasted into the system
// prompt. Returns the validated verdict plus the raw envelope bytes from
// whichever turn succeeded (the second turn overwrites the first on repair).
func haikuVerdictWithRepair(ctx context.Context, sysPrompt, content string) (TriageVerdict, []byte, error) {
	stdout, err := execHaikuJSON(ctx, sysPrompt, content, triageVerdictSchema)
	if err != nil {
		return TriageVerdict{}, nil, err
	}
	v, perr := parseHaikuEnvelope(stdout)
	if perr == nil {
		return v, stdout, nil
	}

	repairPrompt := sysPrompt +
		"\n\nIMPORTANT: your prior response failed schema validation: " +
		perr.Error() +
		". Return ONLY a JSON object matching the TriageVerdict schema. No markdown, no fences, no commentary."
	stdout2, err := execHaikuJSON(ctx, repairPrompt, content, triageVerdictSchema)
	if err != nil {
		return TriageVerdict{}, nil, fmt.Errorf("repair haiku: %w (orig parse: %v)", err, perr)
	}
	v2, perr2 := parseHaikuEnvelope(stdout2)
	if perr2 != nil {
		return TriageVerdict{}, nil, fmt.Errorf("repair parse: %w (orig: %v)", perr2, perr)
	}
	return v2, stdout2, nil
}
