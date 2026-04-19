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
	"errors"
	"fmt"
	"log/slog"
	"os"
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
	TopicTags      []string       `json:"topic_tags,omitempty"`
	ActionRoute    string         `json:"action_route,omitempty"`
	RubricScores   map[string]int `json:"rubric_scores"`
	Profile        string         `json:"profile,omitempty"`
	ProfileVersion int            `json:"profile_version,omitempty"`
}

// UnmarshalJSON handles model non-compliance where rubric_scores values may
// arrive as nested objects {"score": 15, "rationale": "..."} instead of flat
// integers. Coerces both forms into the canonical map[string]int.
func (v *TriageVerdict) UnmarshalJSON(data []byte) error {
	// Alias avoids infinite recursion on json.Unmarshal.
	type Alias TriageVerdict
	var raw struct {
		Alias
		RubricScoresRaw map[string]json.RawMessage `json:"rubric_scores"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*v = TriageVerdict(raw.Alias)
	v.RubricScores = make(map[string]int, len(raw.RubricScoresRaw))
	for axis, val := range raw.RubricScoresRaw {
		val = bytes.TrimSpace(val)
		// Try plain int first.
		var n int
		if err := json.Unmarshal(val, &n); err == nil {
			v.RubricScores[axis] = n
			continue
		}
		// Try object with "score" field.
		var obj struct {
			Score int `json:"score"`
		}
		if err := json.Unmarshal(val, &obj); err == nil {
			v.RubricScores[axis] = obj.Score
			continue
		}
		return fmt.Errorf("rubric_scores[%q]: cannot coerce %s to int", axis, truncateForErr(val))
	}
	return nil
}

// normalizeTopicTags lowercases, trims, and deduplicates topic tags (EPIC-072 M5).
func normalizeTopicTags(tags []string) []string {
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
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
	if len(v.RubricScores) == 0 && v.Score > 0 {
		return fmt.Errorf("rubric_scores missing")
	}
	for axis, score := range v.RubricScores {
		if score < 0 || score > 100 {
			return fmt.Errorf("rubric_scores[%q] = %d out of [0,100]", axis, score)
		}
	}
	// EPIC-072 M5: warn-log when score>0 has empty topic_tags, but don't reject.
	if v.Score > 0 && len(v.TopicTags) == 0 {
		slog.Warn("topic_tags empty for scored item", "score", v.Score)
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
// EPIC-062: --system-prompt-file replaces the entire default system prompt
// (including dynamic sections), so no additional flag is needed to suppress
// them. --effort low, --no-session-persistence.
//
// Returns the raw stdout bytes (the envelope) so the caller can decide
// whether to parse it directly or run a repair turn.
func runClaudeHaikuJSON(ctx context.Context, systemPrompt, content, schema string) ([]byte, error) {
	systemPrompt += "\n\nIMPORTANT: You MUST respond ONLY with a JSON object matching the provided schema." +
		" This applies to ALL cases including noise-gated/skip content." +
		" For skipped content, return {\"score\": 0, \"verdict\": \"<skip reason>\", \"rubric_scores\": {}}." +
		" Never output markdown formatting like **Score:** — always use the JSON schema."
	spFile, _, err := writeSystemPromptFile(systemPrompt)
	if err != nil {
		return nil, err
	}
	defer os.Remove(spFile)

	cmd := exec.CommandContext(ctx, claudeBinaryPath, buildClaudeArgs(claudeExecOpts{
		Model:        claudeModel,
		MaxTurns:     "3", // --output-format json + --json-schema uses internal tool-call turns to enforce schema; 1 is insufficient
		Tools:        "",
		OutputFormat: "json",
		JSONSchema:   schema,
		SystemPrompt: spFile,
	})...)
	cmd.Stdin = strings.NewReader(content)
	cmd.Env = haikuEnv()

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

// runClaudeHaikuVision calls the claude CLI with the Read tool enabled so it
// can read a local image file for multimodal scoring. The prompt instructs the
// model to read the image at imagePath and score it. EPIC-079 M3.
var runClaudeHaikuVision = func(ctx context.Context, systemPrompt, textContent, imagePath, schema string) ([]byte, error) {
	// EPIC-083 M2-1: score-first instruction — short-circuit personal photos to
	// avoid filling the full rubric when the image has no engineered content.
	systemPrompt += "\n\nIf the image is a personal photo (DCIM, camera roll, selfie, food, pet, scenery)" +
		" with no text, code, document, diagram, or engineered content visible," +
		` respond immediately with {"score": 0, "verdict": "personal photo", "rubric_scores": {}}` +
		" without filling the full rubric."
	systemPrompt += "\n\nIMPORTANT: You MUST respond ONLY with a JSON object matching the provided schema." +
		" This applies to ALL cases including noise-gated/skip content." +
		" For skipped content, return {\"score\": 0, \"verdict\": \"<skip reason>\", \"rubric_scores\": {}}." +
		" Never output markdown formatting like **Score:** — always use the JSON schema."
	spFile, _, err := writeSystemPromptFile(systemPrompt)
	if err != nil {
		return nil, err
	}
	defer os.Remove(spFile)

	// Prompt includes instruction to read the image plus any text metadata.
	prompt := fmt.Sprintf("Read the image file at %s and score it.\n\nMetadata:\n%s", imagePath, textContent)

	// EPIC-080 M6: os.Stat pre-check on image file before spawning subprocess.
	if _, statErr := os.Stat(imagePath); statErr != nil {
		return nil, fmt.Errorf("claude vision: image file not readable: %w", statErr)
	}

	cmd := exec.CommandContext(ctx, claudeBinaryPath, buildClaudeArgs(claudeExecOpts{
		Model:        visionModelName,
		MaxTurns:     "3",
		AllowedTools: "Read",
		OutputFormat: "json",
		JSONSchema:   schema,
		SystemPrompt: spFile,
	})...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = haikuEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		slog.Error("claude vision exec failed",
			"event_type", "claude_vision_exec_error",
			"image_path", imagePath,
			"exit_code", exitCode,
			"stdout", strings.TrimSpace(stdout.String()),
			"stderr", strings.TrimSpace(stderr.String()),
			"system_prompt_file", spFile,
		)
		return nil, fmt.Errorf("claude vision exec: %w (stderr=%s)", err, strings.TrimSpace(stderr.String()))
	}
	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) == 0 {
		return nil, fmt.Errorf("claude vision returned empty output")
	}
	return out, nil
}

// envelopeMeta holds the token usage and cost metadata extracted from the
// claude --output-format json envelope. EPIC-062 M2.
type envelopeMeta struct {
	CostUSD      float64    `json:"total_cost_usd"`
	Usage        *TokenUsage `json:"-"`
}

// parseHaikuEnvelope unwraps a `claude --output-format json` payload into
// a TriageVerdict and envelope metadata (token usage + cost).
//
// The envelope shape ships nested:
//
//	{"type":"result", "result":"<json-string-or-object>", "is_error":false,
//	 "total_cost_usd":0.001, "usage":{"input_tokens":500,"output_tokens":100}, ...}
//
// Some claude versions return `result` as a JSON-encoded string, others as
// a raw object. Both forms are accepted. As a courtesy fallback the parser
// also accepts the bare TriageVerdict directly (so unit tests can pass a
// raw verdict without constructing the envelope).
func parseHaikuEnvelope(stdout []byte) (TriageVerdict, *envelopeMeta, error) {
	stdout = bytes.TrimSpace(stdout)
	if len(stdout) == 0 {
		return TriageVerdict{}, nil, fmt.Errorf("empty envelope")
	}

	// Bare verdict shortcut (test/dev path).
	var bare TriageVerdict
	if err := json.Unmarshal(stdout, &bare); err == nil && len(bare.RubricScores) > 0 {
		bare.TopicTags = normalizeTopicTags(bare.TopicTags)
		return bare, nil, bare.validate()
	}

	var env struct {
		Type         string          `json:"type"`
		Subtype      string          `json:"subtype"`
		Result       json.RawMessage `json:"result"`
		IsError      bool            `json:"is_error"`
		TotalCostUSD float64         `json:"total_cost_usd"`
		Usage        *TokenUsage     `json:"usage"`
	}
	if err := json.Unmarshal(stdout, &env); err != nil {
		return TriageVerdict{}, nil, fmt.Errorf("envelope decode: %w", err)
	}
	if env.IsError {
		return TriageVerdict{}, nil, fmt.Errorf("claude error envelope (subtype=%s)", env.Subtype)
	}
	if len(env.Result) == 0 {
		return TriageVerdict{}, nil, fmt.Errorf("envelope has empty result")
	}

	meta := &envelopeMeta{
		CostUSD: env.TotalCostUSD,
		Usage:   env.Usage,
	}

	raw := bytes.TrimSpace(env.Result)
	// `result` may be a JSON string containing JSON. Unwrap once.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return TriageVerdict{}, meta, fmt.Errorf("envelope result string: %w", err)
		}
		raw = bytes.TrimSpace([]byte(s))
		// Strip ```json fences if Haiku wrapped it.
		raw = stripCodeFence(raw)
	}

	var v TriageVerdict
	if err := json.Unmarshal(raw, &v); err != nil {
		return TriageVerdict{}, meta, fmt.Errorf("verdict decode: %w (body=%s)", err, truncateForErr(raw))
	}
	v.TopicTags = normalizeTopicTags(v.TopicTags)
	if err := v.validate(); err != nil {
		return TriageVerdict{}, meta, fmt.Errorf("verdict validate: %w", err)
	}
	return v, meta, nil
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
// prompt. Returns the validated verdict, envelope metadata (token usage/cost),
// and the raw envelope bytes from whichever turn succeeded.
func haikuVerdictWithRepair(ctx context.Context, sysPrompt, content string) (TriageVerdict, *envelopeMeta, error) {
	stdout, err := execHaikuJSON(ctx, sysPrompt, content, triageVerdictSchema)
	if err != nil {
		return TriageVerdict{}, nil, err
	}
	v, meta, perr := parseHaikuEnvelope(stdout)
	if perr == nil {
		return v, meta, nil
	}

	repairPrompt := sysPrompt +
		"\n\nIMPORTANT: your prior response failed schema validation: " +
		perr.Error() +
		". Return ONLY a JSON object matching the TriageVerdict schema. No markdown, no fences, no commentary."
	stdout2, err := execHaikuJSON(ctx, repairPrompt, content, triageVerdictSchema)
	if err != nil {
		return TriageVerdict{}, nil, fmt.Errorf("repair haiku: %w (orig parse: %v)", err, perr)
	}
	v2, meta2, perr2 := parseHaikuEnvelope(stdout2)
	if perr2 != nil {
		return TriageVerdict{}, nil, fmt.Errorf("repair parse: %w (orig: %v)", perr2, perr)
	}
	return v2, meta2, nil
}
