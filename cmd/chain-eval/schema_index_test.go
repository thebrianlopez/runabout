package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// nonConformantDocsRoot builds a docs tree whose artifacts breach several
// classes at once, so the index has something real to report.
func nonConformantDocsRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"prds", "design", "epics"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("prds/PERSONAL_20260501T000000Z_Thing_PRD.md", `# PRD

| Field | Value |
|-------|-------|
| **Status** | `+"`Approved`"+` |
| **Chain Runtime Version** | `+"`N/A`"+` |
`)

	write("design/PERSONAL_20260501T000000Z_Thing_FDD.md", `# FDD

| Field | Value |
|-------|-------|
| **Status** | `+"`Approved`"+` |
| **Source PRD** | `+"`PERSONAL_20260501T000000Z_Thing_PRD.md`"+` |
`)

	// Post-threshold epic: no frontmatter, out-of-enum status, and an upstream
	// row a human reads as linked but the extractor cannot.
	write("epics/PERSONAL_20260501T000000Z_Thing_EPIC-001.md", `# EPIC

| Field | Value |
|-------|-------|
| **Status** | `+"`Done`"+` |
| Source FDD | `+"`PERSONAL_20260501T000000Z_Thing_FDD.md`"+` |
`)

	return root
}

// CT-9: schema violations never fail the build. The indexer is fail-open by
// design - an indexer that refuses to run leaves the operator with no data
// about the drift it just found.
func TestCT9_SchemaViolationsDoNotFailTheBuild(t *testing.T) {
	if _, err := exec.LookPath("cue"); err != nil {
		t.Skip("cue not in PATH")
	}
	docsRoot := nonConformantDocsRoot(t)
	outputPath := filepath.Join(t.TempDir(), "index.json")

	// Keep emission off the operator's real bus.
	t.Setenv("AUTOMATION_METRICS_DIR", t.TempDir())

	code := runIndex(nil, indexRunConfig{docsRoot: docsRoot, output: outputPath, quiet: true})
	if code != 0 {
		t.Fatalf("CT-9: expected exit 0 despite violations, got %d", code)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("CT-9: index not written: %v", err)
	}
	var idx struct {
		Artifacts        []map[string]any `json:"artifacts"`
		SchemaViolations []struct {
			ArtifactPath string `json:"artifact_path"`
			Rule         string `json:"rule"`
			Severity     string `json:"severity"`
		} `json:"schema_violations"`
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatalf("CT-9: index is not valid JSON: %v", err)
	}
	if len(idx.SchemaViolations) == 0 {
		t.Fatal("CT-9: expected a non-empty schema_violations[] over a non-conformant corpus")
	}

	rules := map[string]bool{}
	for _, v := range idx.SchemaViolations {
		rules[v.Rule] = true
	}
	for _, want := range []string{"status_enum", "missing_frontmatter", "upstream_field_unextractable", "runtime_version_malformed"} {
		if !rules[want] {
			t.Errorf("CT-9: expected rule %q in the index, got %v", want, rules)
		}
	}
}

// schema_violations[] is always present in the serialized index, so a consumer
// can distinguish "no violations" from "field absent".
func TestSchemaViolationsAlwaysSerialized(t *testing.T) {
	docsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(docsRoot, "prds"), 0o755); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "index.json")
	t.Setenv("AUTOMATION_METRICS_DIR", t.TempDir())

	if code := runIndex(nil, indexRunConfig{docsRoot: docsRoot, output: outputPath, quiet: true}); code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["schema_violations"]; !ok {
		t.Error("schema_violations must always be present in the index")
	}
}

// An unresolvable schema degrades to a warning and still writes the index.
func TestSchemaValidationDegradesWhenSchemaMissing(t *testing.T) {
	docsRoot := nonConformantDocsRoot(t)
	outputPath := filepath.Join(t.TempDir(), "index.json")
	t.Setenv("AUTOMATION_METRICS_DIR", t.TempDir())
	t.Setenv("CHAIN_SCHEMA_DIR", t.TempDir()) // exists, but holds no schemas

	code := runIndex(nil, indexRunConfig{docsRoot: docsRoot, output: outputPath, quiet: true})
	if code != 0 {
		t.Fatalf("expected exit 0 with an unresolvable schema, got %d", code)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Errorf("index must still be written: %v", err)
	}
}
