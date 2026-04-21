package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

func TestRewriteSingleReplacement(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "test.go", `package example

// old comment
func hello() {}
`)

	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())
	query, err := compileQuery(lang, `(comment) @c`)
	if err != nil {
		t.Fatal(err)
	}
	defer query.Close()

	path := filepath.Join(dir, "test.go")
	rewriteDiff = false
	rewriteWrite = true
	defer func() { rewriteWrite = false }()

	if err := rewriteFile(path, query, lang, "// new comment"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(got), "// new comment") {
		t.Errorf("expected '// new comment' in output, got:\n%s", got)
	}
	if strings.Contains(string(got), "// old comment") {
		t.Errorf("old comment should be replaced, got:\n%s", got)
	}
}

func TestRewriteOverlapElimination(t *testing.T) {
	// Overlapping edits: the second should be skipped.
	edits := []byteEdit{
		{startByte: 10, endByte: 20, replacement: []byte("AAA")},
		{startByte: 15, endByte: 25, replacement: []byte("BBB")}, // overlaps
		{startByte: 30, endByte: 40, replacement: []byte("CCC")},
	}

	result := eliminateOverlaps(edits)

	if len(result) != 2 {
		t.Fatalf("expected 2 edits after overlap elimination, got %d", len(result))
	}

	if result[0].startByte != 10 || result[1].startByte != 30 {
		t.Errorf("unexpected edit positions: %d, %d", result[0].startByte, result[1].startByte)
	}
}

func TestRewriteGofmtRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// Source with awkward spacing that gofmt will fix.
	writeGoFile(t, dir, "ugly.go", `package example

func   hello()   string   {
return "hello"
}
`)

	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())
	query, err := compileQuery(lang, `(function_declaration name: (identifier) @name) @decl`)
	if err != nil {
		t.Fatal(err)
	}
	defer query.Close()

	path := filepath.Join(dir, "ugly.go")
	rewriteDiff = false
	rewriteWrite = true
	defer func() { rewriteWrite = false }()

	if err := rewriteFile(path, query, lang, `func @name() string { return "rewritten" }`); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// gofmt should have produced clean output.
	if strings.Contains(string(got), "   ") {
		t.Errorf("expected gofmt-clean output, got extra spaces:\n%s", got)
	}
	if !strings.Contains(string(got), "rewritten") {
		t.Errorf("expected 'rewritten' in output, got:\n%s", got)
	}
}

func TestRewriteCaptureInterpolation(t *testing.T) {
	captures := map[string]string{
		"name": "hello",
		"recv": "*Server",
	}

	got := interpolateCaptures("func @name(s @recv)", captures)
	want := "func hello(s *Server)"
	if got != want {
		t.Errorf("interpolateCaptures = %q, want %q", got, want)
	}
}

func TestRewriteCaptureInterpolationUnresolved(t *testing.T) {
	captures := map[string]string{"name": "hello"}

	got := interpolateCaptures("@name @unknown", captures)
	want := "hello @unknown"
	if got != want {
		t.Errorf("unresolved capture: got %q, want %q", got, want)
	}
}

func TestRewriteDiffOutput(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "test.go", `package example

// remove me
func hello() {}
`)

	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())
	query, err := compileQuery(lang, `(comment) @c`)
	if err != nil {
		t.Fatal(err)
	}
	defer query.Close()

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	path := filepath.Join(dir, "test.go")
	rewriteDiff = true
	rewriteWrite = false
	defer func() { rewriteDiff = false }()

	err = rewriteFile(path, query, lang, "")
	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatal(err)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "---") || !strings.Contains(output, "+++") {
		t.Errorf("expected unified diff headers, got:\n%s", output)
	}
	if !strings.Contains(output, "-// remove me") {
		t.Errorf("expected removed line in diff, got:\n%s", output)
	}
}

func TestRewritePostRewriteErrorDetection(t *testing.T) {
	// This tests that validateSyntax doesn't panic on invalid Go.
	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())

	// Invalid Go source — validateSyntax should warn on stderr, not crash.
	invalidSrc := []byte("package example\n\nfunc {{ broken")
	validateSyntax("test.go", invalidSrc, lang)
	// No assertion needed — we just verify it doesn't panic.
}

func TestRewriteApplyEditsReverse(t *testing.T) {
	src := []byte("hello world foo")
	edits := []byteEdit{
		{startByte: 0, endByte: 5, replacement: []byte("HELLO")},
		{startByte: 6, endByte: 11, replacement: []byte("WORLD")},
		{startByte: 12, endByte: 15, replacement: []byte("BAR")},
	}

	got := string(applyEdits(src, edits))
	want := "HELLO WORLD BAR"
	if got != want {
		t.Errorf("applyEdits = %q, want %q", got, want)
	}
}

func TestRewriteNoMatchesNoOp(t *testing.T) {
	dir := t.TempDir()
	content := `package example

func hello() {}
`
	writeGoFile(t, dir, "test.go", content)

	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())
	query, err := compileQuery(lang, `(method_declaration name: (field_identifier) @name) @decl`)
	if err != nil {
		t.Fatal(err)
	}
	defer query.Close()

	path := filepath.Join(dir, "test.go")
	rewriteDiff = false
	rewriteWrite = true
	defer func() { rewriteWrite = false }()

	if err := rewriteFile(path, query, lang, "replacement"); err != nil {
		t.Fatal(err)
	}

	// File should be unchanged.
	got, _ := os.ReadFile(path)
	if string(got) != content {
		t.Errorf("file should be unchanged, got:\n%s", got)
	}
}
