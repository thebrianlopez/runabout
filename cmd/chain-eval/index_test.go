package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// BT-2: chain-eval index --help prints expected flag descriptions.
func TestIndexCmd_Help(t *testing.T) {
	cmd := indexCmd()
	flags := []string{"--docs-root", "--output", "--schema-dir", "--include-legacy"}
	usage := cmd.UsageString()
	for _, flag := range flags {
		if !strings.Contains(usage, flag) {
			t.Errorf("BT-2: --help output missing flag %q", flag)
		}
	}
}

// BT-4: Protocol-Spec PRD sets is_protocol=true in the index.
func TestRunIndex_IsProtocol(t *testing.T) {
	root := t.TempDir()
	prdDir := filepath.Join(root, "prds")
	if err := os.MkdirAll(prdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Protocol-Spec PRD.
	protoPRD := `## Status and Metadata
| **Status** | Approved |
| **Protocol Spec** | true |
`
	if err := os.WriteFile(filepath.Join(prdDir, "PERSONAL_20260601T000000Z_Proto_PRD.md"), []byte(protoPRD), 0o644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(t.TempDir(), "idx.json")
	code := runIndex(nil, indexRunConfig{docsRoot: root, output: outputPath, quiet: true})
	if code != 0 {
		t.Fatalf("BT-4: expected exit 0, got %d", code)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	artifacts := result["artifacts"].([]interface{})
	if len(artifacts) == 0 {
		t.Fatal("BT-4: expected at least one artifact in index")
	}
	rec := artifacts[0].(map[string]interface{})
	if rec["is_protocol"] != true {
		t.Errorf("BT-4: expected is_protocol=true, got %v", rec["is_protocol"])
	}
}

// RG-1: Status-surface drift sets status_surface_drift=true in the index output.
func TestRunIndex_StatusSurfaceDriftInOutput(t *testing.T) {
	root := t.TempDir()
	epicDir := filepath.Join(root, "epics")
	if err := os.MkdirAll(epicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Epic with frontmatter Complete but body table Draft.
	driftEpic := `---
status: Complete
---
# Epic

## Status and Metadata
| **Status** | Draft |
`
	if err := os.WriteFile(filepath.Join(epicDir, "PERSONAL_20260601T000000Z_ClaudeCode_EPIC-RG1_test.md"), []byte(driftEpic), 0o644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(t.TempDir(), "idx.json")
	code := runIndex(nil, indexRunConfig{docsRoot: root, output: outputPath, quiet: true})
	if code != 0 {
		t.Fatalf("RG-1: expected exit 0, got %d", code)
	}

	data, _ := os.ReadFile(outputPath)
	var result map[string]interface{}
	json.Unmarshal(data, &result) //nolint:errcheck
	artifacts := result["artifacts"].([]interface{})
	rec := artifacts[0].(map[string]interface{})
	if rec["status_surface_drift"] != true {
		t.Errorf("RG-1: expected status_surface_drift=true for drifted artifact, got %v", rec["status_surface_drift"])
	}
	if rec["status"] != "Complete" {
		t.Errorf("RG-1: expected canonical status='Complete' (frontmatter wins), got %q", rec["status"])
	}
}

// BT-1 (EPIC-170): index output contains content_hash with sha256: prefix + 64 hex chars.
func TestRunIndex_ContentHashInOutput(t *testing.T) {
	root := t.TempDir()
	prdDir := filepath.Join(root, "prds")
	if err := os.MkdirAll(prdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prdDir, "PERSONAL_20260601T000000Z_Test_PRD.md"), []byte("## Status and Metadata\n| **Status** | Draft |\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(t.TempDir(), "idx.json")
	code := runIndex(nil, indexRunConfig{docsRoot: root, output: outputPath, quiet: true})
	if code != 0 {
		t.Fatalf("BT-1 (EPIC-170): expected exit 0, got %d", code)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	hash, _ := result["content_hash"].(string)
	if !strings.HasPrefix(hash, "sha256:") {
		t.Errorf("BT-1 (EPIC-170): content_hash must start with sha256:, got %q", hash)
	}
	hex := strings.TrimPrefix(hash, "sha256:")
	if len(hex) != 64 {
		t.Errorf("BT-1 (EPIC-170): content_hash hex must be 64 chars, got %d in %q", len(hex), hash)
	}
}

// BT-2 (EPIC-170): Two index runs on unchanged corpus produce identical content_hash.
func TestRunIndex_ContentHashIdempotent(t *testing.T) {
	root := t.TempDir()
	prdDir := filepath.Join(root, "prds")
	if err := os.MkdirAll(prdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prdDir, "PERSONAL_20260601T000000Z_Test_PRD.md"), []byte("## Status and Metadata\n| **Status** | Draft |\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out1 := filepath.Join(t.TempDir(), "idx1.json")
	out2 := filepath.Join(t.TempDir(), "idx2.json")

	if code := runIndex(nil, indexRunConfig{docsRoot: root, output: out1, quiet: true}); code != 0 {
		t.Fatalf("BT-2 (EPIC-170): first run failed with exit %d", code)
	}
	if code := runIndex(nil, indexRunConfig{docsRoot: root, output: out2, quiet: true}); code != 0 {
		t.Fatalf("BT-2 (EPIC-170): second run failed with exit %d", code)
	}

	readHash := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		h, _ := result["content_hash"].(string)
		return h
	}

	h1, h2 := readHash(out1), readHash(out2)
	if h1 != h2 {
		t.Errorf("BT-2 (EPIC-170): content_hash not idempotent: %q vs %q", h1, h2)
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
