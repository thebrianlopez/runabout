package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression guard for POMO PERSONAL_20260713T004848Z
// (chain-eval-prompt-path-discovery-gap) and DocsInfra F4 TC-F4-06.
//
// Root cause: chain-eval hardcoded a default prompt dir of "docs/core/prompts"
// (the pre-split subtree layout), which resolves on no current host. These
// tests lock in the ordered, existence-checked fallback so the stale literal
// can never again be the *sole* default.

// TC-F4-06a: an explicit CHAIN_PROMPTS_DIR override wins unconditionally.
func TestResolvePromptsDir_EnvOverrideWins(t *testing.T) {
	t.Setenv("CHAIN_PROMPTS_DIR", "/some/explicit/override")
	t.Setenv("ORG_PATH", "") // ensure override is not shadowed by discovery
	if got := resolvePromptsDir(); got != "/some/explicit/override" {
		t.Fatalf("want explicit override, got %q", got)
	}
}

// TC-F4-06b: with no override, an existing $ORG_PATH/core/prompts is selected.
func TestResolvePromptsDir_OrgPathFallback(t *testing.T) {
	org := t.TempDir()
	promptDir := filepath.Join(org, "core", "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "command_chain.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHAIN_PROMPTS_DIR", "")
	t.Setenv("ORG_PATH", org)

	if got := resolvePromptsDir(); got != promptDir {
		t.Fatalf("want %q, got %q", promptDir, got)
	}
}

// TC-F4-06c: when nothing resolves, the terminal fallback is the legacy
// literal returned only as a stable diagnostic path - never selected over a
// real directory. This documents that "docs/core/prompts" is acceptable ONLY
// as the last-resort candidate, not as a standalone hardcoded default.
func TestResolvePromptsDir_TerminalFallbackIsDiagnostic(t *testing.T) {
	t.Setenv("CHAIN_PROMPTS_DIR", "")
	t.Setenv("ORG_PATH", filepath.Join(t.TempDir(), "does-not-exist"))
	// HOME points at an empty dir so ~/core/prompts does not resolve either.
	t.Setenv("HOME", t.TempDir())

	got := resolvePromptsDir()
	if got != "docs/core/prompts" {
		t.Fatalf("want terminal diagnostic fallback %q, got %q", "docs/core/prompts", got)
	}
}
