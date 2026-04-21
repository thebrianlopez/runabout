package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractFunction(t *testing.T) {
	src := `package example

// Add adds two integers.
func Add(a, b int) int {
	return a + b
}

func Sub(a, b int) int {
	return a - b
}
`
	tree, parser := parseSource(t, src)
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	results, err := findDeclarations(root, []byte(src), root.Language(), "Add")
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Name != "Add" {
		t.Errorf("expected name 'Add', got %q", r.Name)
	}
	if r.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", r.Kind)
	}
	if !strings.Contains(r.Body, "return a + b") {
		t.Errorf("body does not contain expected code: %s", r.Body)
	}
}

func TestExtractMethodDifferentReceivers(t *testing.T) {
	src := `package example

type A struct{}
type B struct{}

func (a *A) Name() string { return "A" }
func (b *B) Name() string { return "B" }
`
	tree, parser := parseSource(t, src)
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	results, err := findDeclarations(root, []byte(src), root.Language(), "Name")
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results for ambiguous Name, got %d", len(results))
	}

	receivers := make(map[string]bool)
	for _, r := range results {
		if r.Receiver != nil {
			receivers[*r.Receiver] = true
		}
	}
	if !receivers["*A"] || !receivers["*B"] {
		t.Errorf("expected receivers *A and *B, got %v", receivers)
	}
}

func TestExtractMultipleInit(t *testing.T) {
	src := `package example

func init() {
	fmt.Println("first")
}

func init() {
	fmt.Println("second")
}

func init() {
	fmt.Println("third")
}
`
	tree, parser := parseSource(t, src)
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	results, err := findDeclarations(root, []byte(src), root.Language(), "init")
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 init() functions, got %d", len(results))
	}

	// Each should have different start lines
	lines := make(map[uint]bool)
	for _, r := range results {
		if r.StartLine == 0 {
			t.Error("expected non-zero start line")
		}
		lines[r.StartLine] = true
	}
	if len(lines) != 3 {
		t.Errorf("expected 3 distinct start lines, got %d", len(lines))
	}
}

func TestExtractNotFound(t *testing.T) {
	src := `package example

func Hello() {}
`
	tree, parser := parseSource(t, src)
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	results, err := findDeclarations(root, []byte(src), root.Language(), "NonExistent")
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestExtractByteForByteEquality(t *testing.T) {
	// Write a Go file, extract a function, then verify it matches the source lines
	src := `package example

func Greet(name string) string {
	return "Hello, " + name + "!"
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "greet.go")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	tree, srcBytes, parser, err := parseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	results, err := findDeclarations(root, srcBytes, root.Language(), "Greet")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// Read the same line range from the file
	lines := strings.Split(string(srcBytes), "\n")
	startIdx := results[0].StartLine - 1 // 0-indexed
	endIdx := results[0].EndLine         // exclusive
	if endIdx > uint(len(lines)) {
		endIdx = uint(len(lines))
	}
	fileLines := strings.Join(lines[startIdx:endIdx], "\n")

	if results[0].Body != fileLines {
		t.Errorf("body does not match source lines\n--- body ---\n%s\n--- source ---\n%s", results[0].Body, fileLines)
	}
}

// Integration tests against real linkari files.
// These test that ts-go can parse real-world Go code without panics or missed declarations.

var hotFiles = []string{
	"../../cmd/linkari/server_score.go",
	"../../cmd/linkari/server.go",
	"../../cmd/linkari/cmd_triage.go",
	"../../cmd/linkari/evaluator.go",
	"../../cmd/linkari/triage_verdict.go",
	"../../cmd/linkari/queue.go",
}

func TestIntegrationHotFiles(t *testing.T) {
	for _, path := range hotFiles {
		abs, err := filepath.Abs(path)
		if err != nil {
			t.Fatalf("abs path %s: %v", path, err)
		}
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			t.Skipf("hot file not found: %s (run from cmd/ts-go/)", abs)
		}

		t.Run(filepath.Base(path), func(t *testing.T) {
			tree, src, parser, err := parseFile(abs)
			if err != nil {
				t.Fatal(err)
			}
			defer tree.Close()
			defer parser.Close()

			root := tree.RootNode()
			lang := root.Language()

			funcs, err := extractFunctions(root, src, lang)
			if err != nil {
				t.Fatalf("extractFunctions: %v", err)
			}

			methods, err := extractMethods(root, src, lang)
			if err != nil {
				t.Fatalf("extractMethods: %v", err)
			}

			types, err := extractTypes(root, src, lang)
			if err != nil {
				t.Fatalf("extractTypes: %v", err)
			}

			total := len(funcs) + len(methods)
			if total == 0 {
				t.Errorf("expected at least 1 function/method in %s", filepath.Base(path))
			}

			t.Logf("%s: %d funcs, %d methods, %d types",
				filepath.Base(path), len(funcs), len(methods), len(types))

			// Verify no zero-line entries
			for _, f := range append(funcs, methods...) {
				if f.StartLine == 0 || f.EndLine == 0 {
					t.Errorf("function %s has zero line numbers", f.Name)
				}
				if f.EndLine < f.StartLine {
					t.Errorf("function %s: end_line %d < start_line %d", f.Name, f.EndLine, f.StartLine)
				}
			}
		})
	}
}

func TestIntegrationExtractRoundTrip(t *testing.T) {
	for _, path := range hotFiles {
		abs, err := filepath.Abs(path)
		if err != nil {
			t.Fatalf("abs path %s: %v", path, err)
		}
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			t.Skipf("hot file not found: %s", abs)
		}

		t.Run(filepath.Base(path), func(t *testing.T) {
			tree, src, parser, err := parseFile(abs)
			if err != nil {
				t.Fatal(err)
			}
			defer tree.Close()
			defer parser.Close()

			root := tree.RootNode()
			lang := root.Language()

			funcs, err := extractFunctions(root, src, lang)
			if err != nil {
				t.Fatal(err)
			}

			if len(funcs) == 0 {
				t.Skip("no functions to test extract on")
			}

			// Pick the first function and verify extract produces matching body
			target := funcs[0]
			results, err := findDeclarations(root, src, lang, target.Name)
			if err != nil {
				t.Fatal(err)
			}

			if len(results) == 0 {
				t.Fatalf("extract found no declaration for %q", target.Name)
			}

			// Verify the extracted body starts with the function signature
			found := false
			for _, r := range results {
				if r.StartLine == target.StartLine {
					found = true
					if !strings.Contains(r.Body, target.Name) {
						t.Errorf("extracted body for %s does not contain function name", target.Name)
					}
				}
			}
			if !found {
				t.Errorf("no extract result matches start_line %d for %s", target.StartLine, target.Name)
			}
		})
	}
}
