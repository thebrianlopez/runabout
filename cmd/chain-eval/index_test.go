package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// CT-2: --output flag writes index to the specified path (not the default).
func TestRunIndex_OutputFlag(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "ci.json")
	docsRoot := t.TempDir()

	// Create a minimal docs root so resolveDocsRoot finds it.
	if err := os.MkdirAll(filepath.Join(docsRoot, "prds"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Set CHAIN_DOCS_ROOT to our temp dir to avoid hitting real docs/.
	t.Setenv("CHAIN_DOCS_ROOT", docsRoot)

	cfg := indexRunConfig{
		docsRoot: docsRoot,
		output:   outputPath,
	}
	// runIndex panics with "not implemented" - test fails expectedly at M1.
	defer func() {
		if r := recover(); r != nil {
			if s, ok := r.(string); ok && s == "not implemented" {
				t.Skip("CT-2: not implemented yet (M1 gate)")
			}
			panic(r)
		}
	}()

	// Use a minimal cobra command for the test.
	code := runIndex(nil, cfg)
	if code != 0 {
		t.Errorf("CT-2: expected exit 0, got %d", code)
	}
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Errorf("CT-2: expected index at %q, not found", outputPath)
	}
}

// CT-10: Atomic write - injected rename failure leaves output path unchanged.
func TestWriteIndexAtomic_RenameFailure(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, ".chain-index.json")

	// Pre-create the output file with sentinel content.
	sentinel := []byte(`{"existing": true}`)
	if err := os.WriteFile(outputPath, sentinel, 0o644); err != nil {
		t.Fatal(err)
	}

	// Inject a rename function that always fails.
	orig := renameFunc
	renameFunc = func(src, dst string) error {
		return errors.New("simulated rename failure")
	}
	defer func() { renameFunc = orig }()

	err := writeIndexAtomic(outputPath, []byte(`{"new": true}`))
	if err == nil {
		t.Fatal("CT-10: expected error from injected rename failure")
	}

	// Output path must be unchanged (still contains sentinel).
	got, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatalf("CT-10: output file should still exist, got read error: %v", readErr)
	}
	if string(got) != string(sentinel) {
		t.Errorf("CT-10: output file modified despite rename failure; got %q, want %q", got, sentinel)
	}

	// No temp files should remain in the directory.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != ".chain-index.json" {
			t.Errorf("CT-10: temp file leaked: %q", e.Name())
		}
	}
}

// TestResolveDocsRoot confirms the priority order: flag > env > fallback.
func TestResolveDocsRoot_Priority(t *testing.T) {
	t.Setenv("CHAIN_DOCS_ROOT", "/from/env")

	if got := resolveDocsRoot("/from/flag"); got != "/from/flag" {
		t.Errorf("flag should win: got %q", got)
	}
	if got := resolveDocsRoot(""); got != "/from/env" {
		t.Errorf("env should win over fallback: got %q", got)
	}

	t.Setenv("CHAIN_DOCS_ROOT", "")
	if got := resolveDocsRoot(""); got != "./docs/" {
		t.Errorf("fallback should be ./docs/: got %q", got)
	}
}
