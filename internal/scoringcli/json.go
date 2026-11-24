// Package scoringcli is the shared pi:json completion helper.
//
// cmd/linkari ScoringBackend and cmd/chain-eval both call CompleteJSON so
// there is one subprocess contract: pi --print, TempDir cwd, hermetic flags,
// stderr captured into the error. Backend attribution is "pi:json".
package scoringcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// BackendJSON is the evaluator label for this helper (scoring_backend.go:77).
const BackendJSON = "pi:json"

// DefaultModel is the combined provider/model string used when callers omit one.
const DefaultModel = "anthropic/claude-haiku-4-5-20251001"

// JSONOpts configures a single CompleteJSON call.
type JSONOpts struct {
	BinaryPath   string
	Model        string
	SystemPrompt string
	Content      string
	Schema       string
}

// CompleteJSON shells `pi --print` and returns trimmed stdout bytes.
// Schema, when set, is appended to the system prompt (pi has no --json-schema).
// cmd.Dir is os.TempDir() so workspace AGENTS.md is not discovered (EPIC-088).
func CompleteJSON(ctx context.Context, opts JSONOpts) ([]byte, error) {
	binary := opts.BinaryPath
	if binary == "" {
		binary = "pi"
	}
	model := opts.Model
	if model == "" {
		model = DefaultModel
	}
	systemPrompt := opts.SystemPrompt
	if opts.Schema != "" {
		systemPrompt += "\n\nIMPORTANT: You MUST respond ONLY with a JSON object matching this schema. " +
			"No markdown, no fences, no commentary. For skipped/noise content return {\"score\": 0, \"verdict\": \"<skip reason>\", \"rubric_scores\": {}, \"workspace\": \"none\", \"workspace_confidence\": 0}." +
			"\n\nSchema:\n" + opts.Schema
	}
	args := []string{
		"--print",
		"--no-session",
		"--no-builtin-tools",
	}
	args = append(args, HermeticFlags()...)
	args = append(args, "--model", model, "--system-prompt", systemPrompt)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = strings.NewReader(opts.Content)
	cmd.Dir = os.TempDir()
	cmd.Env = Env()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pi exec: %w (stderr=%s)", err, strings.TrimSpace(stderr.String()))
	}
	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) == 0 {
		return nil, fmt.Errorf("pi returned empty output")
	}
	return out, nil
}

// ParseEnvelopeJSON extracts the first JSON object or array from raw CLI output.
func ParseEnvelopeJSON(raw []byte) (json.RawMessage, error) {
	text := strings.TrimSpace(string(raw))
	for i, c := range text {
		if c == '{' || c == '[' {
			candidate := text[i:]
			b := []byte(candidate)
			if json.Valid(b) {
				return json.RawMessage(b), nil
			}
			for j := len(b); j > 0; j-- {
				if b[j-1] == '}' || b[j-1] == ']' {
					if json.Valid(b[:j]) {
						return json.RawMessage(b[:j]), nil
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("no JSON found in response")
}

// ValidateBinary runs `pi --version`. Failure names the CLI-only architecture.
func ValidateBinary(path string) error {
	if path == "" {
		path = "pi"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = Env()
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("pi --version failed: %w (is %q in PATH?). scoring uses pi CLI OAuth only, not provider API keys", err, path)
	}
	if strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("pi --version returned empty output for %q", path)
	}
	return nil
}

// MissingBinaryError is true when the pi binary cannot be found.
func MissingBinaryError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "executable file not found") ||
		strings.Contains(err.Error(), "no such file")
}

// HermeticFlags keeps scoring free of host extensions/skills/context files.
func HermeticFlags() []string {
	return []string{"--no-extensions", "--no-skills", "--no-context-files"}
}

// Env filters os.Environ for a pi subprocess. HOME is retained for auth.json.
func Env() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_") || (strings.HasPrefix(kv, "PI_") && !strings.HasPrefix(kv, "PI_CODING_AGENT_DIR=")) {
			continue
		}
		filtered = append(filtered, kv)
	}
	return filtered
}
