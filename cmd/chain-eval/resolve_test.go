package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeFDD(t *testing.T, docsRoot, name, upstream string) {
	t.Helper()
	dir := filepath.Join(docsRoot, "design")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "## Status and Metadata\n| **Status** | Approved |\n| **Source PRD** | " + upstream + " |\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// End-to-end: runResolve wires Scan -> ResolveUpstreamReferents -> report,
// and --output writes the full per-record JSON.
func TestRunResolve_OutputFlag(t *testing.T) {
	docsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(docsRoot, "prds"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFDD(t, docsRoot, "PERSONAL_a_FDD.md", "PERSONAL_x_PRD.md")
	if err := os.WriteFile(filepath.Join(docsRoot, "prds", "PERSONAL_x_PRD.md"), []byte("# PRD\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(t.TempDir(), "resolve.json")
	t.Setenv("AUTOMATION_METRICS_DIR", t.TempDir()) // never hit the real bus

	cfg := resolveRunConfig{docsRoot: docsRoot, output: outputPath, quiet: true}
	code := runResolve(nil, cfg)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("expected output file, got: %v", err)
	}
	var out resolveOutput
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if out.Report.Resolved != 1 {
		t.Errorf("expected 1 resolved, got report: %+v", out.Report)
	}
	if len(out.Results) != 1 {
		t.Errorf("expected 1 result, got: %+v", out.Results)
	}
}

// declared_none exclusion, exercised through the full Scan -> Resolve wiring
// with a real NO-UPSTREAM sentinel cell (not a hand-built ArtifactRecord).
func TestRunResolve_DeclaredNoneExcludedEndToEnd(t *testing.T) {
	docsRoot := t.TempDir()
	writeFDD(t, docsRoot, "PERSONAL_a_FDD.md", "NO-UPSTREAM")

	outputPath := filepath.Join(t.TempDir(), "resolve.json")
	t.Setenv("AUTOMATION_METRICS_DIR", t.TempDir())

	cfg := resolveRunConfig{docsRoot: docsRoot, output: outputPath, quiet: true}
	code := runResolve(nil, cfg)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var out resolveOutput
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Report.DeclaredNoneExcluded != 1 {
		t.Errorf("expected 1 declared_none_excluded, got report: %+v", out.Report)
	}
	if out.Report.Resolved != 0 || out.Report.Unresolved != 0 || out.Report.Severed != 0 {
		t.Errorf("NO-UPSTREAM record must not enter resolution, got report: %+v", out.Report)
	}
	if len(out.Results) != 0 {
		t.Errorf("expected no results, got: %+v", out.Results)
	}
}

func TestRunResolve_DocsRootNotFound(t *testing.T) {
	code := runResolve(nil, resolveRunConfig{docsRoot: "/nonexistent/" + t.Name(), quiet: true})
	if code != 1 {
		t.Errorf("expected exit 1 for missing docs root, got %d", code)
	}
}

func TestResolveCmd_Help(t *testing.T) {
	cmd := resolveCmd()
	flags := []string{"--docs-root", "--core-root", "--output", "--quiet"}
	usage := cmd.UsageString()
	for _, flag := range flags {
		if !containsFlag(usage, flag) {
			t.Errorf("expected --help output to mention %q, got:\n%s", flag, usage)
		}
	}
}

func containsFlag(usage, flag string) bool {
	return len(usage) > 0 && (indexOf(usage, flag) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestResolveCoreRoot_FlagWins(t *testing.T) {
	t.Setenv("WS_ORG_CORE", "/should/not/be/used")
	if got := resolveCoreRoot("/explicit/core"); got != "/explicit/core" {
		t.Errorf("resolveCoreRoot with flag set = %q, want /explicit/core", got)
	}
}

func TestResolveCoreRoot_EnvFallback(t *testing.T) {
	t.Setenv("WS_ORG_CORE", "/env/core")
	if got := resolveCoreRoot(""); got != "/env/core" {
		t.Errorf("resolveCoreRoot env fallback = %q, want /env/core", got)
	}
}
