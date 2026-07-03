package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// piBinaryPath is the resolved path to the pi binary. Tests replace it via
// t.Cleanup to inject a stub without live subprocess calls. EPIC-217 F3.
var piBinaryPath = "pi"

// PiScoringBackend implements ScoringBackend by delegating single-turn LLM
// calls to the `pi` binary in --print mode. EPIC-217 F3.
//
// Subprocess invocation:
//
//	pi --print --no-session --no-builtin-tools \
//	   --provider <b.provider> --model <b.model> \
//	   --system-prompt <systemPrompt>
//
// stdin:  content string
// stdout: trimmed text response
// stderr: captured; included in error on non-zero exit
// Dir:    os.TempDir() - prevents pi from discovering workspace .pi/ config
// Env:    piEnv() - strips CLAUDE_* and PI_* vars; sets neutral HOME
type PiScoringBackend struct {
	provider string // "anthropic" (default), "google", "openai", "ollama"
	model    string // e.g. "claude-haiku-4-5-20251001"
}

// Complete sends systemPrompt + content to pi and returns the trimmed text
// response. Errors are classified as PE-001 (exec failure), PE-002 (empty
// output), or PE-003 (context timeout/cancel).
func (b PiScoringBackend) Complete(ctx context.Context, systemPrompt, content string) (string, error) {
	cmd := exec.CommandContext(
		ctx, piBinaryPath,
		"--print",
		"--no-session",
		"--no-builtin-tools",
		"--provider", b.provider,
		"--model", b.model,
		"--system-prompt", systemPrompt,
	)
	cmd.Stdin = strings.NewReader(content)
	cmd.Dir = os.TempDir()
	cmd.Env = piEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pi exec: %w (stderr=%s)", err, strings.TrimSpace(stderr.String()))
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return "", fmt.Errorf("pi returned empty output")
	}
	return out, nil
}

// CompleteJSON sends systemPrompt + content to pi in text mode and returns
// the raw response bytes. The scoring prompt instructs the model to respond
// in JSON; pi --print mode emits only the final text block (no JSONL events).
func (b PiScoringBackend) CompleteJSON(ctx context.Context, systemPrompt, content, schema string) ([]byte, error) {
	cmd := exec.CommandContext(
		ctx, piBinaryPath,
		"--print",
		"--no-session",
		"--no-builtin-tools",
		"--provider", b.provider,
		"--model", b.model,
		"--system-prompt", systemPrompt,
	)
	cmd.Stdin = strings.NewReader(content)
	cmd.Dir = os.TempDir()
	cmd.Env = piEnv()

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

// piEnv returns a filtered copy of os.Environ() safe for pi subprocess use.
// It strips CLAUDE_* and PI_* vars that could affect pi behavior or leak
// credentials, then overrides HOME to a neutral path so pi cannot discover
// workspace .pi/ config. Mirrors haikuEnv() intent. EPIC-217 F3 (RG-2).
func piEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_") || strings.HasPrefix(kv, "PI_") {
			continue
		}
		filtered = append(filtered, kv)
	}
	// Keep real HOME so pi can find ~/.config/pi/agent/auth.json.
	// Unlike Claude CLI, pi needs HOME for auth resolution.
	return filtered
}
