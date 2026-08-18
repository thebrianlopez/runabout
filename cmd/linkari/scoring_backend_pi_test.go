package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CT-1: Complete returns trimmed stdout on exit 0.
func TestPiScoringBackend_Complete_ReturnsOutput(t *testing.T) {
	b := PiScoringBackend{model: "anthropic/claude-haiku-4-5-20251001", BinaryPath: "/bin/echo"}
	// /bin/echo ignores stdin and prints its args; we just need non-empty output.
	got, err := b.Complete(context.Background(), "sys", "content")
	if err != nil {
		t.Fatalf("CT-1: unexpected error: %v", err)
	}
	if got == "" {
		t.Fatal("CT-1: expected non-empty output, got empty string")
	}
}

// CT-2: CompleteJSON returns raw stdout bytes on exit 0.
func TestPiScoringBackend_CompleteJSON_ReturnsBytes(t *testing.T) {
	// Use a stub script that prints JSON to stdout.
	stub := writePiStub(t, `#!/bin/sh
echo '{"score":1}'
`, 0)

	b := PiScoringBackend{model: "anthropic/claude-haiku-4-5-20251001", BinaryPath: stub}
	got, err := b.CompleteJSON(context.Background(), "sys", "content", "{}")
	if err != nil {
		t.Fatalf("CT-2: unexpected error: %v", err)
	}
	if string(got) != `{"score":1}` {
		t.Fatalf("CT-2: expected %q, got %q", `{"score":1}`, string(got))
	}
}

// CT-3: Complete propagates non-zero exit as pi_exec_failed.
func TestPiScoringBackend_Complete_PropagatesExecError(t *testing.T) {
	stub := writePiStub(t, `#!/bin/sh
echo "bad" >&2
exit 1
`, 0)

	b := PiScoringBackend{model: "anthropic/m", BinaryPath: stub}
	_, err := b.Complete(context.Background(), "sys", "content")
	if err == nil {
		t.Fatal("CT-3: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "pi exec:") {
		t.Fatalf("CT-3: error should contain %q, got: %v", "pi exec:", err)
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Fatalf("CT-3: error should contain stderr text %q, got: %v", "bad", err)
	}
}

// CT-4: Complete returns pi_empty_output on empty stdout.
func TestPiScoringBackend_Complete_EmptyOutput(t *testing.T) {
	stub := writePiStub(t, `#!/bin/sh
exit 0
`, 0)

	b := PiScoringBackend{model: "anthropic/m", BinaryPath: stub}
	_, err := b.Complete(context.Background(), "sys", "content")
	if err == nil {
		t.Fatal("CT-4: expected error on empty output, got nil")
	}
	if !strings.Contains(err.Error(), "pi returned empty output") {
		t.Fatalf("CT-4: error should contain %q, got: %v", "pi returned empty output", err)
	}
}

// CT-5: Complete propagates context cancellation.
func TestPiScoringBackend_Complete_ContextCancelled(t *testing.T) {
	// Use a stub that sleeps long enough to be cancelled.
	stub := writePiStub(t, `#!/bin/sh
sleep 30
`, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	b := PiScoringBackend{model: "anthropic/m", BinaryPath: stub}
	_, err := b.Complete(ctx, "sys", "content")
	if err == nil {
		t.Fatal("CT-5: expected error on cancelled context, got nil")
	}
}

// CT-6: BinaryPath field is used (not hardcoded "pi").
func TestPiScoringBackend_UsesBinaryPathField(t *testing.T) {
	// /bin/echo will print its args and exit 0, giving non-empty output.

	b := PiScoringBackend{model: "anthropic/m", BinaryPath: "/bin/echo"}
	_, err := b.Complete(context.Background(), "sys", "content")
	if err != nil {
		t.Fatalf("CT-6: expected success with /bin/echo as piBinaryPath, got: %v", err)
	}
}

// RG-1: cmd.Dir must NOT be the workspace root; must equal os.TempDir().
func TestPiScoringBackend_RG1_DirIsNotWorkspace(t *testing.T) {
	stub := writePiStub(t, `#!/bin/sh
pwd
`, 0)

	b := PiScoringBackend{model: "anthropic/m", BinaryPath: stub}
	got, err := b.Complete(context.Background(), "sys", "content")
	if err != nil {
		t.Fatalf("RG-1: unexpected error: %v", err)
	}
	if got == "" {
		t.Fatal("RG-1: pwd returned empty")
	}
	wantReal, _ := filepath.EvalSymlinks(os.TempDir())
	gotReal, _ := filepath.EvalSymlinks(got)
	if gotReal != wantReal {
		t.Fatalf("RG-1: cmd.Dir should be %q, stub reported %q (resolved: %q vs %q)",
			os.TempDir(), got, wantReal, gotReal)
	}
}

// RG-2: CLAUDE_* and PI_* env vars must not appear in the pi subprocess env,
// except PI_CODING_AGENT_DIR which is whitelisted for auth resolution.
func TestPiScoringBackend_RG2_EnvVarsStripped(t *testing.T) {
	env := piEnv()
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_") {
			t.Errorf("RG-2: piEnv() must not contain CLAUDE_* vars, found: %q", kv)
		}
		if strings.HasPrefix(kv, "PI_") && !strings.HasPrefix(kv, "PI_CODING_AGENT_DIR=") {
			t.Errorf("RG-2: piEnv() must not contain PI_* vars (except PI_CODING_AGENT_DIR), found: %q", kv)
		}
	}
}

// RG-3: CLI flags passed to pi must match the expected contract.
// If Pi changes flag names, this test breaks the build.
func TestPiScoringBackend_RG3_CLIFlagsComplete(t *testing.T) {
	stub := writePiStub(t, `#!/bin/sh
echo "$@"
`, 0)

	b := PiScoringBackend{model: "openai-codex/gpt-5.4-mini", BinaryPath: stub}
	got, err := b.Complete(context.Background(), "test-system-prompt", "content")
	if err != nil {
		t.Fatalf("RG-3: unexpected error: %v", err)
	}

	required := []string{
		"--print",
		"--no-session",
		"--no-builtin-tools",
		"--model", "openai-codex/gpt-5.4-mini",
		"--system-prompt", "test-system-prompt",
	}
	for _, flag := range required {
		if !strings.Contains(got, flag) {
			t.Errorf("RG-3: expected flag %q in args, got: %s", flag, got)
		}
	}
}

// RG-6: every pi invocation must be hermetic - no extension tools, no skills,
// no ambient context files. Regression guard for the defect where
// --no-builtin-tools was assumed sufficient: it disables built-ins only, so a
// host with a web-access extension installed had scoring calls invoking
// web_search/fetch_content mid-score (cost inflation, network dependency, and
// non-reproducible scores that undermine the golden-set eval harness).
//
// Covers all three entry points. CompleteVision is already protected by the
// --tools allowlist, but is asserted here so the guarantee is uniform and
// cannot regress if that allowlist is ever widened.
func TestPiScoringBackend_RG6_InvocationsAreHermetic(t *testing.T) {
	stub := writePiStub(t, `#!/bin/sh
echo "$@"
`, 0)

	imgFile := filepath.Join(t.TempDir(), "test.png")
	if err := os.WriteFile(imgFile, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := PiScoringBackend{model: "openai-codex/gpt-5.4-mini", BinaryPath: stub}

	complete, err := b.Complete(context.Background(), "sys", "content")
	if err != nil {
		t.Fatalf("RG-6 Complete: unexpected error: %v", err)
	}
	completeJSON, err := b.CompleteJSON(context.Background(), "sys", "content", "{}")
	if err != nil {
		t.Fatalf("RG-6 CompleteJSON: unexpected error: %v", err)
	}
	vision, err := b.CompleteVision(context.Background(), "sys", "meta", imgFile, "{}")
	if err != nil {
		t.Fatalf("RG-6 CompleteVision: unexpected error: %v", err)
	}

	cases := []struct {
		method string
		args   string
	}{
		{"Complete", complete},
		{"CompleteJSON", string(completeJSON)},
		{"CompleteVision", string(vision)},
	}
	// Literal, not piHermeticFlags() - asserting against the helper would pass
	// vacuously if it ever returned an empty slice, which is precisely the
	// regression this guards.
	required := []string{"--no-extensions", "--no-skills", "--no-context-files"}
	for _, tc := range cases {
		for _, flag := range required {
			if !strings.Contains(tc.args, flag) {
				t.Errorf("RG-6 %s: expected hermetic flag %q in args, got: %s", tc.method, flag, tc.args)
			}
		}
	}

	if diff := len(required) - len(piHermeticFlags()); diff != 0 {
		t.Errorf("RG-6: piHermeticFlags() returned %d flags, guard pins %d - update both together",
			len(piHermeticFlags()), len(required))
	}
}

// RG-7: piEnv must retain HOME. pi resolves auth from ~/.config/pi/agent/auth.json,
// so stripping HOME breaks scoring entirely. This pins the property that a stale
// comment once denied (it claimed HOME was overridden to a neutral path) and makes
// the dependency explicit: because HOME is live, hermeticity is enforced by
// piHermeticFlags, not by environment scrubbing.
func TestPiScoringBackend_RG7_EnvRetainsHome(t *testing.T) {
	if os.Getenv("HOME") == "" {
		t.Skip("HOME unset in this environment")
	}
	var found bool
	for _, kv := range piEnv() {
		if strings.HasPrefix(kv, "HOME=") {
			found = true
			break
		}
	}
	if !found {
		t.Error("RG-7: piEnv() must retain HOME for pi auth resolution")
	}
}

// RG-4: --provider must NOT appear in the args (combined syntax uses --model only).
func TestPiScoringBackend_RG4_NoProviderFlag(t *testing.T) {
	stub := writePiStub(t, `#!/bin/sh
echo "$@"
`, 0)

	b := PiScoringBackend{model: "openai-codex/gpt-5.4-mini", BinaryPath: stub}

	got, err := b.Complete(context.Background(), "sys", "content")
	if err != nil {
		t.Fatalf("RG-4 Complete: unexpected error: %v", err)
	}
	if strings.Contains(got, "--provider") {
		t.Errorf("RG-4 Complete: --provider must not appear in args, got: %s", got)
	}

	gotJSON, err := b.CompleteJSON(context.Background(), "sys", "content", "{}")
	if err != nil {
		t.Fatalf("RG-4 CompleteJSON: unexpected error: %v", err)
	}
	if strings.Contains(string(gotJSON), "--provider") {
		t.Errorf("RG-4 CompleteJSON: --provider must not appear in args, got: %s", string(gotJSON))
	}
}

// RG-5: CompleteVision uses --tools read (not --no-builtin-tools).
func TestPiScoringBackend_RG5_VisionUsesToolsRead(t *testing.T) {
	stub := writePiStub(t, `#!/bin/sh
echo "$@"
`, 0)

	imgFile := filepath.Join(t.TempDir(), "test.png")
	if err := os.WriteFile(imgFile, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := PiScoringBackend{model: "openai-codex/gpt-5.4-mini", BinaryPath: stub}
	got, err := b.CompleteVision(context.Background(), "sys", "meta", imgFile, "{}")
	if err != nil {
		t.Fatalf("RG-5: unexpected error: %v", err)
	}
	args := string(got)
	if !strings.Contains(args, "--tools read") {
		t.Errorf("RG-5: expected --tools read in args, got: %s", args)
	}
	if strings.Contains(args, "--no-builtin-tools") {
		t.Errorf("RG-5: --no-builtin-tools must not appear in vision args, got: %s", args)
	}
	if strings.Contains(args, "--provider") {
		t.Errorf("RG-5: --provider must not appear in vision args, got: %s", args)
	}
}

// piModelString tests
func TestPiModelString_CombinedSyntax(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     string
	}{
		{"openai-codex", "gpt-5.4-mini", "openai-codex/gpt-5.4-mini"},
		{"anthropic", "claude-haiku-4-5-20251001", "anthropic/claude-haiku-4-5-20251001"},
		{"", "gpt-5.4-mini", "anthropic/gpt-5.4-mini"},
		{"", "", "anthropic/" + claudeModel},
		{"", "openai-codex/gpt-5.4-mini", "openai-codex/gpt-5.4-mini"},
	}
	for _, tt := range tests {
		got := piModelString(tt.provider, tt.model)
		if got != tt.want {
			t.Errorf("piModelString(%q, %q) = %q, want %q", tt.provider, tt.model, got, tt.want)
		}
	}
}

// writePiStub writes a shell script to a temp file, makes it executable,
// and returns its path. The test cleans it up automatically.
func writePiStub(t *testing.T, script string, exitCode int) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "pi-stub-*.sh")
	if err != nil {
		t.Fatalf("writePiStub: %v", err)
	}
	if _, err := f.WriteString(script); err != nil {
		t.Fatalf("writePiStub write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("writePiStub close: %v", err)
	}
	if err := os.Chmod(f.Name(), 0o755); err != nil {
		t.Fatalf("writePiStub chmod: %v", err)
	}
	return f.Name()
}
