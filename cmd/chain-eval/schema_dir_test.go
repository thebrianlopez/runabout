package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Follow-on from EPIC-266 release checklist section 3: the schema dir default
// pointed at {docs-root}/core/schemas unconditionally, which does not exist on a
// docs repo without an embedded core/ copy. Validation then degraded silently.

func writeGateSchema(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chain_gate.cue"), []byte("package schemas\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// An explicit --schema-dir is honored verbatim, even when it holds no schema:
// an operator pointing somewhere on purpose must get a real error, not a
// silent substitution.
func TestResolveSchemaDir_FlagWins(t *testing.T) {
	core := writeGateSchema(t, filepath.Join(t.TempDir(), "schemas", "cue"))
	t.Setenv("WS_ORG_CORE", filepath.Dir(filepath.Dir(core)))

	flagDir := writeGateSchema(t, filepath.Join(t.TempDir(), "explicit"))
	got, ok := resolveSchemaDir(flagDir, t.TempDir())
	if got != flagDir || !ok {
		t.Errorf("flag dir = (%q, %v), want (%q, true)", got, ok, flagDir)
	}

	empty := t.TempDir()
	got, ok = resolveSchemaDir(empty, t.TempDir())
	if got != empty {
		t.Errorf("explicit empty dir = %q, want %q verbatim", got, empty)
	}
	if ok {
		t.Error("explicit dir without chain_gate.cue must report unresolved")
	}
}

// CHAIN_SCHEMA_DIR outranks WS_ORG_CORE.
func TestResolveSchemaDir_EnvPriority(t *testing.T) {
	envDir := writeGateSchema(t, filepath.Join(t.TempDir(), "env"))
	coreRoot := t.TempDir()
	writeGateSchema(t, filepath.Join(coreRoot, "schemas", "cue"))

	t.Setenv("CHAIN_SCHEMA_DIR", envDir)
	t.Setenv("WS_ORG_CORE", coreRoot)

	got, ok := resolveSchemaDir("", t.TempDir())
	if got != envDir || !ok {
		t.Errorf("resolved = (%q, %v), want (%q, true)", got, ok, envDir)
	}
}

// WS_ORG_CORE/schemas/cue resolves when CHAIN_SCHEMA_DIR is unset.
func TestResolveSchemaDir_WSOrgCore(t *testing.T) {
	coreRoot := t.TempDir()
	want := writeGateSchema(t, filepath.Join(coreRoot, "schemas", "cue"))

	t.Setenv("CHAIN_SCHEMA_DIR", "")
	t.Setenv("WS_ORG_CORE", coreRoot)

	got, ok := resolveSchemaDir("", t.TempDir())
	if got != want || !ok {
		t.Errorf("resolved = (%q, %v), want (%q, true)", got, ok, want)
	}
}

// The embedded docs copy still resolves, at either nesting depth.
func TestResolveSchemaDir_EmbeddedDocsCopy(t *testing.T) {
	t.Setenv("CHAIN_SCHEMA_DIR", "")
	t.Setenv("WS_ORG_CORE", "")

	docsRoot := t.TempDir()
	want := writeGateSchema(t, filepath.Join(docsRoot, "core", "schemas", "cue"))
	got, ok := resolveSchemaDir("", docsRoot)
	if got != want || !ok {
		t.Errorf("nested: resolved = (%q, %v), want (%q, true)", got, ok, want)
	}

	docsRoot2 := t.TempDir()
	want2 := writeGateSchema(t, filepath.Join(docsRoot2, "core", "schemas"))
	got, ok = resolveSchemaDir("", docsRoot2)
	if got != want2 || !ok {
		t.Errorf("flat: resolved = (%q, %v), want (%q, true)", got, ok, want2)
	}
}

// Nothing resolvable: fall back to the historical default and report unresolved
// so the caller can warn instead of skipping validation in silence.
func TestResolveSchemaDir_Unresolvable(t *testing.T) {
	t.Setenv("CHAIN_SCHEMA_DIR", "")
	t.Setenv("WS_ORG_CORE", "")
	t.Setenv("HOME", t.TempDir())

	docsRoot := t.TempDir()
	got, ok := resolveSchemaDir("", docsRoot)
	if ok {
		t.Error("expected unresolved when no candidate holds chain_gate.cue")
	}
	if want := filepath.Join(docsRoot, "core/schemas"); got != want {
		t.Errorf("fallback = %q, want %q", got, want)
	}
}

// An unresolvable schema dir must not fail the build: the indexer is fail-open.
func TestRunIndex_UnresolvableSchemaDirStillWritesIndex(t *testing.T) {
	t.Setenv("CHAIN_SCHEMA_DIR", "")
	t.Setenv("WS_ORG_CORE", "")
	t.Setenv("HOME", t.TempDir())

	root := writeFixtureCorpus(t)
	outputPath := filepath.Join(t.TempDir(), "idx.json")
	if code := runIndex(nil, indexRunConfig{docsRoot: root, output: outputPath, quiet: true}); code != 0 {
		t.Fatalf("expected exit 0 with no schema dir, got %d", code)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Errorf("expected index written despite missing schemas: %v", err)
	}
}

// Candidate list order is the documented contract.
func TestSchemaDirCandidates_Order(t *testing.T) {
	t.Setenv("CHAIN_SCHEMA_DIR", "/env/schemas")
	t.Setenv("WS_ORG_CORE", "/core")

	got := schemaDirCandidates("", "/docs")
	want := []string{"/env/schemas", "/core/schemas/cue", "/docs/core/schemas/cue", "/docs/core/schemas"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("candidates = %v, want prefix %v", got, want)
		}
	}
}
