package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

// resetPiBinaryPath resets piBinaryPath to its default after the test.
// All CT-* tests use this to satisfy the Independent FIRST principle.
func resetPiBinaryPath(t *testing.T, path string) {
	t.Helper()
	orig := piBinaryPath
	piBinaryPath = path
	t.Cleanup(func() { piBinaryPath = orig })
}

// CT-1: Complete returns trimmed stdout on exit 0.
func TestPiScoringBackend_Complete_ReturnsOutput(t *testing.T) {
	resetPiBinaryPath(t, "/bin/echo")
	b := PiScoringBackend{provider: "anthropic", model: "claude-haiku-4-5-20251001"}
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
	resetPiBinaryPath(t, stub)

	b := PiScoringBackend{provider: "anthropic", model: "claude-haiku-4-5-20251001"}
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
	resetPiBinaryPath(t, stub)

	b := PiScoringBackend{provider: "anthropic", model: "m"}
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
	resetPiBinaryPath(t, stub)

	b := PiScoringBackend{provider: "anthropic", model: "m"}
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
	resetPiBinaryPath(t, stub)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	b := PiScoringBackend{provider: "anthropic", model: "m"}
	_, err := b.Complete(ctx, "sys", "content")
	if err == nil {
		t.Fatal("CT-5: expected error on cancelled context, got nil")
	}
}

// CT-6: piBinaryPath var is used (not hardcoded "pi").
func TestPiScoringBackend_UsesPiBinaryPathVar(t *testing.T) {
	// /bin/echo will print its args and exit 0, giving non-empty output.
	resetPiBinaryPath(t, "/bin/echo")

	b := PiScoringBackend{provider: "anthropic", model: "m"}
	_, err := b.Complete(context.Background(), "sys", "content")
	if err != nil {
		t.Fatalf("CT-6: expected success with /bin/echo as piBinaryPath, got: %v", err)
	}
}

// RG-1: cmd.Dir must NOT be the workspace root; must equal os.TempDir().
// This is verified structurally - the test confirms piEnv() and cmd.Dir
// isolation by checking that piEnv() does not expose workspace paths.
// The Dir isolation is tested indirectly via the spy approach below.
func TestPiScoringBackend_RG1_DirIsNotWorkspace(t *testing.T) {
	// Stub prints its working directory to stdout.
	stub := writePiStub(t, `#!/bin/sh
pwd
`, 0)
	resetPiBinaryPath(t, stub)

	b := PiScoringBackend{provider: "anthropic", model: "m"}
	got, err := b.Complete(context.Background(), "sys", "content")
	if err != nil {
		t.Fatalf("RG-1: unexpected error: %v", err)
	}
	if got == "" {
		t.Fatal("RG-1: pwd returned empty")
	}
	// Must equal os.TempDir() - not the workspace root.
	if got != os.TempDir() {
		t.Fatalf("RG-1: cmd.Dir should be %q, stub reported %q", os.TempDir(), got)
	}
}

// RG-2: CLAUDE_* and PI_* env vars must not appear in the pi subprocess env.
func TestPiScoringBackend_RG2_EnvVarsStripped(t *testing.T) {
	env := piEnv()
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_") || strings.HasPrefix(kv, "PI_") {
			t.Errorf("RG-2: piEnv() must not contain CLAUDE_* or PI_* vars, found: %q", kv)
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
