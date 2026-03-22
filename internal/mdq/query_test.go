package mdq

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExecuteFieldQuery(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "a.md", `# Doc A

## Status

| Field | Value |
|-------|-------|
| **Status** | Active |
| **Owner** | alice |
`)
	writeTestFile(t, dir, "b.md", `# Doc B

## Status

| Field | Value |
|-------|-------|
| **Status** | Complete |
| **Owner** | bob |
`)

	pattern := filepath.Join(dir, "*.md")
	results, err := Execute(pattern, Query{Field: "Status"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	values := map[string]string{}
	for _, r := range results {
		values[filepath.Base(r.File)] = r.Value
	}
	if values["a.md"] != "Active" {
		t.Errorf("a.md Status = %q, want Active", values["a.md"])
	}
	if values["b.md"] != "Complete" {
		t.Errorf("b.md Status = %q, want Complete", values["b.md"])
	}
}

func TestExecuteColumnQuery(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "tools.md", `# Tools

## Summary

| Tool | Problem |
|------|---------|
| mdq | Query markdown |
| perfgate | Performance gates |
`)

	results, err := Execute(filepath.Join(dir, "tools.md"), Query{Field: "Tool"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Value != "mdq" {
		t.Errorf("result[0] = %q, want mdq", results[0].Value)
	}
	if results[1].Value != "perfgate" {
		t.Errorf("result[1] = %q, want perfgate", results[1].Value)
	}
}

func TestExecuteTableScope(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "doc.md", `# Doc

## Metadata

| Field | Value |
|-------|-------|
| **Status** | Active |

## Other

| Field | Value |
|-------|-------|
| **Status** | Ignored |
`)

	results, err := Execute(filepath.Join(dir, "doc.md"), Query{Field: "Status", Table: "Metadata"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Value != "Active" {
		t.Errorf("Value = %q, want Active", results[0].Value)
	}
}

func TestExecuteHeadings(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "doc.md", `# Title
## A
### B
## C
`)

	results, err := Execute(filepath.Join(dir, "doc.md"), Query{Heading: "*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("got %d headings, want 4", len(results))
	}
}

func TestExecuteHeadingsWithLevel(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "doc.md", `# Title
## A
### B
## C
`)

	results, err := Execute(filepath.Join(dir, "doc.md"), Query{Heading: "*", Level: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d headings, want 2", len(results))
	}
	if results[0].Value != "A" || results[1].Value != "C" {
		t.Errorf("headings = [%q, %q], want [A, C]", results[0].Value, results[1].Value)
	}
}

func TestExecuteNoMatches(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "empty.md", "# Nothing here\n")

	results, err := Execute(filepath.Join(dir, "empty.md"), Query{Field: "Status"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestExecuteNoFiles(t *testing.T) {
	dir := t.TempDir()
	results, err := Execute(filepath.Join(dir, "*.md"), Query{Field: "Status"})
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}
}

func TestExecuteBadPattern(t *testing.T) {
	_, err := Execute("[invalid", Query{Field: "x"})
	if err == nil {
		t.Error("expected error for bad glob pattern")
	}
}

func TestExecuteWithExclude(t *testing.T) {
	dir := t.TempDir()
	// Create subdirectories with markdown files.
	for _, sub := range []string{"epics", "standups", "ideas"} {
		subDir := filepath.Join(dir, sub)
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, subDir, "doc.md", "# "+sub+" Doc\n")
	}

	// Without exclude: all 3 files.
	all, err := ExecuteWithOptions(filepath.Join(dir, "*/*.md"), Query{Heading: "*"}, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d results without exclude, want 3", len(all))
	}

	// With exclude: skip standups.
	filtered, err := ExecuteWithOptions(filepath.Join(dir, "*/*.md"), Query{Heading: "*"}, ListOptions{
		Exclude: []string{"standups"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("got %d results with exclude, want 2", len(filtered))
	}
	for _, r := range filtered {
		if filepath.Base(filepath.Dir(r.File)) == "standups" {
			t.Errorf("standups should have been excluded, got %s", r.File)
		}
	}
}

func TestExecuteGitAlwaysExcluded(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, gitDir, "config.md", "# Git Config\n")
	okDir := filepath.Join(dir, "docs")
	if err := os.MkdirAll(okDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, okDir, "readme.md", "# Readme\n")

	results, err := ExecuteWithOptions(filepath.Join(dir, "*/*.md"), Query{Heading: "*"}, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (.git should be auto-excluded)", len(results))
	}
	if filepath.Base(filepath.Dir(results[0].File)) != "docs" {
		t.Errorf("expected docs file, got %s", results[0].File)
	}
}

func TestShouldExclude(t *testing.T) {
	excludeSet := map[string]bool{".git": true, "temp": true}

	tests := []struct {
		path string
		want bool
	}{
		{"docs/epics/file.md", false},
		{".git/config.md", true},
		{"a/temp/file.md", true},
		{"a/b/temp/file.md", true},
		{"temp/file.md", true},
		{"docs/temporary/file.md", false}, // "temporary" != "temp"
	}

	for _, tt := range tests {
		got := shouldExclude(tt.path, excludeSet)
		if got != tt.want {
			t.Errorf("shouldExclude(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
