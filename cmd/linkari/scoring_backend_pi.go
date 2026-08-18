package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// PiScoringBackend implements ScoringBackend by delegating single-turn LLM
// calls to the `pi` binary in --print mode. EPIC-217 F3.
//
// Subprocess invocation:
//
//	pi --print --no-session --no-builtin-tools \
//	   --no-extensions --no-skills --no-context-files \
//	   --model <provider/model> \
//	   --system-prompt <systemPrompt>
//
// stdin:  content string
// stdout: trimmed text response
// stderr: captured; included in error on non-zero exit
// Dir:    os.TempDir() - prevents pi from discovering workspace .pi/ config
// Env:    piEnv() - strips CLAUDE_* and PI_* vars; retains HOME for auth
//
// Hermeticity (see piHermeticFlags): --no-builtin-tools alone is NOT enough.
// It disables built-in tools only; extension tools stay enabled and pi
// discovers them via HOME, which piEnv deliberately retains for auth.
type PiScoringBackend struct {
	model      string // "provider/model" combined syntax, e.g. "openai-codex/gpt-5.4-mini"
	BinaryPath string // path to pi binary; "" defaults to "pi" (EPIC-258: injected instead of package global)
}

// Complete sends systemPrompt + content to pi and returns the trimmed text
// response. Errors are classified as PE-001 (exec failure), PE-002 (empty
// output), or PE-003 (context timeout/cancel).
func (b PiScoringBackend) Complete(ctx context.Context, systemPrompt, content string) (string, error) {
	binaryPath := b.BinaryPath
	if binaryPath == "" {
		binaryPath = "pi"
	}
	args := []string{
		"--print",
		"--no-session",
		"--no-builtin-tools",
	}
	args = append(args, piHermeticFlags()...)
	args = append(
		args,
		"--model", b.model,
		"--system-prompt", systemPrompt,
	)
	cmd := exec.CommandContext(ctx, binaryPath, args...)
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

func (b PiScoringBackend) Name() string { return "pi" }

// CompleteJSON sends systemPrompt + content to pi in text mode and returns
// the raw response bytes. The schema is injected into the system prompt so
// the model knows the required shape; pi --print emits only the final text
// block (no JSONL events). parseHaikuEnvelope handles bare JSON from Pi.
func (b PiScoringBackend) CompleteJSON(ctx context.Context, systemPrompt, content, schema string) ([]byte, error) {
	// Inject JSON schema constraint into the system prompt  -  mirrors the
	// IMPORTANT suffix that runClaudeHaikuJSON adds for the claude_cli path.
	// Without this, pi returns free prose and parseHaikuEnvelope rejects it.
	if schema != "" {
		systemPrompt += "\n\nIMPORTANT: You MUST respond ONLY with a JSON object matching this schema. " +
			"No markdown, no fences, no commentary. For skipped/noise content return {\"score\": 0, \"verdict\": \"<skip reason>\", \"rubric_scores\": {}}." +
			"\n\nSchema:\n" + schema
	}
	binaryPath := b.BinaryPath
	if binaryPath == "" {
		binaryPath = "pi"
	}
	args := []string{
		"--print",
		"--no-session",
		"--no-builtin-tools",
	}
	args = append(args, piHermeticFlags()...)
	args = append(
		args,
		"--model", b.model,
		"--system-prompt", systemPrompt,
	)
	cmd := exec.CommandContext(ctx, binaryPath, args...)
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

// CompleteVision sends the multimodal prompt to pi, using the Read tool for
// local image access.
//
// Note --tools is an allowlist spanning built-in, extension, and custom tools,
// so "--tools read" already excludes extension tools here. piHermeticFlags is
// still applied for skills/context-file determinism and faster startup.
func (b PiScoringBackend) CompleteVision(ctx context.Context, systemPrompt, textContent, imagePath, schema string) ([]byte, error) {
	if _, err := os.Stat(imagePath); err != nil {
		return nil, fmt.Errorf("pi vision: image file not readable: %w", err)
	}
	binaryPath := b.BinaryPath
	if binaryPath == "" {
		binaryPath = "pi"
	}
	args := []string{
		"--print",
		"--no-session",
	}
	args = append(args, piHermeticFlags()...)
	args = append(
		args,
		"--model", b.model,
		"--tools", "read",
		"--system-prompt", systemPrompt,
	)
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	prompt := strings.TrimSpace(fmt.Sprintf("Read the image file at %s and score it.\n\nMetadata:\n%s", imagePath, textContent))
	cmd.Stdin = strings.NewReader(prompt)
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

// piModelString builds the combined "provider/model" string for --model.
// If model already contains a slash, it is returned as-is (already combined).
// Otherwise provider and model are joined with "/".
func piModelString(provider, model string) string {
	p := orDefault(provider, "anthropic")
	m := orDefault(model, claudeModel)
	if strings.Contains(m, "/") {
		return m
	}
	return p + "/" + m
}

// piHermeticFlags returns the flags that keep a scoring call hermetic - free of
// ambient host state that would otherwise vary between machines and runs.
//
// This exists because --no-builtin-tools is narrower than its name suggests: it
// disables pi's built-in tools only, leaving extension tools enabled. pi
// discovers those through HOME, which piEnv retains for auth resolution. On a
// host with a web-access extension installed, scoring calls were observed
// invoking web_search and fetch_content before answering - roughly 3.7x the
// token cost, a live network round-trip per score, and share-derived queries
// leaving the machine. It also made scores irreproducible, which silently
// undermines the golden-set eval harness.
//
//	--no-extensions    the verified defect: no extension tools, no extension prompts
//	--no-skills        skills inject prompt text; exclude for reproducibility
//	--no-context-files no AGENTS.md/CLAUDE.md discovery from cwd (defense in depth)
//
// Auth is unaffected: --no-extensions was verified against both anthropic/ and
// openai-codex/ models with an auth extension installed.
func piHermeticFlags() []string {
	return []string{"--no-extensions", "--no-skills", "--no-context-files"}
}

// piEnv returns a filtered copy of os.Environ() safe for pi subprocess use.
// It strips CLAUDE_* and PI_* vars that could affect pi behavior or leak
// credentials. EPIC-217 F3 (RG-2).
//
// NOTE: HOME is deliberately RETAINED - pi resolves auth from
// ~/.config/pi/agent/auth.json, unlike the Claude CLI path this otherwise
// mirrors. Earlier revisions of this comment claimed HOME was overridden to a
// neutral path; that was never true of this code and the discrepancy hid the
// extension-loading defect that piHermeticFlags now closes. Because HOME is
// live, hermeticity must be enforced by flags, not by environment scrubbing.
func piEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_") || (strings.HasPrefix(kv, "PI_") && !strings.HasPrefix(kv, "PI_CODING_AGENT_DIR=")) {
			continue
		}
		filtered = append(filtered, kv)
	}
	// Keep real HOME so pi can find ~/.config/pi/agent/auth.json.
	// Unlike Claude CLI, pi needs HOME for auth resolution.
	return filtered
}
