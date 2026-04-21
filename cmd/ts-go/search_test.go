package main

import (
	"os"
	"path/filepath"
	"testing"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

func TestSearchZeroMatches(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "empty.go", `package example

func hello() {}
`)

	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())
	query, err := compileQuery(lang, `(method_declaration name: (field_identifier) @name) @decl`)
	if err != nil {
		t.Fatal(err)
	}
	defer query.Close()

	results, err := searchFile(filepath.Join(dir, "empty.go"), query, lang)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestSearchSingleFileMatch(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "funcs.go", `package example

func hello() string {
	return "hello"
}

func world() string {
	return "world"
}
`)

	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())
	query, err := compileQuery(lang, `(function_declaration name: (identifier) @name)`)
	if err != nil {
		t.Fatal(err)
	}
	defer query.Close()

	results, err := searchFile(filepath.Join(dir, "funcs.go"), query, lang)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if results[0].Captures["name"] != "hello" {
		t.Errorf("expected capture 'hello', got %q", results[0].Captures["name"])
	}
	if results[1].Captures["name"] != "world" {
		t.Errorf("expected capture 'world', got %q", results[1].Captures["name"])
	}

	if results[0].File != filepath.Join(dir, "funcs.go") {
		t.Errorf("expected file path, got %q", results[0].File)
	}
}

func TestSearchMultiFileWalk(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	writeGoFile(t, dir, "a.go", `package example
func alpha() {}
`)
	writeGoFile(t, sub, "b.go", `package sub
func beta() {}
`)

	files, err := expandGlob(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(files) < 2 {
		t.Fatalf("expected at least 2 files, got %d: %v", len(files), files)
	}

	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())
	query, err := compileQuery(lang, `(function_declaration name: (identifier) @name)`)
	if err != nil {
		t.Fatal(err)
	}
	defer query.Close()

	var allResults []SearchResult
	for _, f := range files {
		results, err := searchFile(f, query, lang)
		if err != nil {
			t.Fatalf("searchFile(%s): %v", f, err)
		}
		allResults = append(allResults, results...)
	}

	if len(allResults) != 2 {
		t.Errorf("expected 2 total results, got %d", len(allResults))
	}
}

func TestSearchCompactOutput(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "test.go", `package example

func hello() string { return "hi" }
`)

	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())
	query, err := compileQuery(lang, `(function_declaration name: (identifier) @name)`)
	if err != nil {
		t.Fatal(err)
	}
	defer query.Close()

	results, err := searchFile(filepath.Join(dir, "test.go"), query, lang)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// Verify result fields are populated.
	r := results[0]
	if r.StartLine == 0 || r.EndLine == 0 {
		t.Errorf("expected non-zero lines, got start=%d end=%d", r.StartLine, r.EndLine)
	}
	if r.MatchedText == "" {
		t.Error("expected non-empty matched text")
	}
}

func TestSearchCompileQueryError(t *testing.T) {
	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())
	_, err := compileQuery(lang, `(not_a_real_node_type)`)
	if err == nil {
		t.Error("expected query compilation error")
	}
}

func TestSearchLineAt(t *testing.T) {
	src := []byte("line1\nline2\nline3\n")

	tests := []struct {
		offset uint
		want   int
	}{
		{0, 1},
		{5, 1}, // newline itself
		{6, 2},
		{12, 3},
	}

	for _, tt := range tests {
		got := lineAt(src, tt.offset)
		if got != tt.want {
			t.Errorf("lineAt(src, %d) = %d, want %d", tt.offset, got, tt.want)
		}
	}
}

// writeGoFile is a test helper that writes a Go source file.
func writeGoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
