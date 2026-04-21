package main

import (
	"os"
	"path/filepath"
	"testing"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

func parseSource(t *testing.T, src string) (*tree_sitter.Tree, *tree_sitter.Parser) {
	t.Helper()
	parser := tree_sitter.NewParser()
	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())
	if err := parser.SetLanguage(lang); err != nil {
		t.Fatalf("set language: %v", err)
	}
	tree := parser.Parse([]byte(src), nil)
	if tree == nil {
		t.Fatal("parse returned nil tree")
	}
	return tree, parser
}

func TestExtractFunctions(t *testing.T) {
	src := `package example

func hello() string {
	return "hello"
}

func add(a, b int) int {
	return a + b
}
`
	tree, parser := parseSource(t, src)
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	funcs, err := extractFunctions(root, []byte(src), root.Language())
	if err != nil {
		t.Fatal(err)
	}

	if len(funcs) != 2 {
		t.Fatalf("expected 2 functions, got %d", len(funcs))
	}

	if funcs[0].Name != "hello" {
		t.Errorf("expected name 'hello', got %q", funcs[0].Name)
	}
	if funcs[0].Kind != "function" {
		t.Errorf("expected kind 'function', got %q", funcs[0].Kind)
	}
	if funcs[0].StartLine != 3 {
		t.Errorf("expected start_line 3, got %d", funcs[0].StartLine)
	}

	if funcs[1].Name != "add" {
		t.Errorf("expected name 'add', got %q", funcs[1].Name)
	}
}

func TestExtractMethods(t *testing.T) {
	src := `package example

type Server struct{}

func (s *Server) Start() error {
	return nil
}

func (s Server) Name() string {
	return "server"
}
`
	tree, parser := parseSource(t, src)
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	methods, err := extractMethods(root, []byte(src), root.Language())
	if err != nil {
		t.Fatal(err)
	}

	if len(methods) != 2 {
		t.Fatalf("expected 2 methods, got %d", len(methods))
	}

	if methods[0].Name != "Start" {
		t.Errorf("expected name 'Start', got %q", methods[0].Name)
	}
	if methods[0].Kind != "method" {
		t.Errorf("expected kind 'method', got %q", methods[0].Kind)
	}
	if methods[0].Receiver == nil || *methods[0].Receiver != "*Server" {
		t.Errorf("expected receiver '*Server', got %v", methods[0].Receiver)
	}

	if methods[1].Name != "Name" {
		t.Errorf("expected name 'Name', got %q", methods[1].Name)
	}
	if methods[1].Receiver == nil || *methods[1].Receiver != "Server" {
		t.Errorf("expected receiver 'Server', got %v", methods[1].Receiver)
	}
}

func TestGenericFunction(t *testing.T) {
	src := `package example

func Map[T any, U any](s []T, f func(T) U) []U {
	result := make([]U, len(s))
	for i, v := range s {
		result[i] = f(v)
	}
	return result
}
`
	tree, parser := parseSource(t, src)
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	funcs, err := extractFunctions(root, []byte(src), root.Language())
	if err != nil {
		t.Fatal(err)
	}

	if len(funcs) != 1 {
		t.Fatalf("expected 1 function, got %d", len(funcs))
	}

	if funcs[0].Name != "Map" {
		t.Errorf("expected name 'Map', got %q", funcs[0].Name)
	}
	// Signature should include type parameters
	if funcs[0].Signature == "" {
		t.Error("expected non-empty signature")
	}
	t.Logf("generic signature: %s", funcs[0].Signature)
}

func TestGenericMethod(t *testing.T) {
	src := `package example

type Container[T any] struct {
	items []T
}

func (c *Container[T]) Add(item T) {
	c.items = append(c.items, item)
}
`
	tree, parser := parseSource(t, src)
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	methods, err := extractMethods(root, []byte(src), root.Language())
	if err != nil {
		t.Fatal(err)
	}

	if len(methods) != 1 {
		t.Fatalf("expected 1 method, got %d", len(methods))
	}

	if methods[0].Name != "Add" {
		t.Errorf("expected name 'Add', got %q", methods[0].Name)
	}
	if methods[0].Receiver == nil {
		t.Error("expected non-nil receiver")
	}
}

func TestBuildConstraints(t *testing.T) {
	src := `//go:build linux && amd64

package example

func platformSpecific() {}
`
	bc := parseBuildConstraints([]byte(src))
	if bc != "linux && amd64" {
		t.Errorf("expected 'linux && amd64', got %q", bc)
	}

	// No build constraint
	srcNone := `package example

func normal() {}
`
	bcNone := parseBuildConstraints([]byte(srcNone))
	if bcNone != "" {
		t.Errorf("expected empty, got %q", bcNone)
	}
}

func TestCompactOutputTokenCost(t *testing.T) {
	// Write a realistic Go file to temp dir and verify compact output is under 800 bytes
	src := `package example

type Server struct {
	addr string
	port int
}

func NewServer(addr string, port int) *Server {
	return &Server{addr: addr, port: port}
}

func (s *Server) Start() error {
	return nil
}

func (s *Server) Stop() error {
	return nil
}

func (s *Server) Addr() string {
	return s.addr
}

func handleRequest(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("OK"))
}

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "server.go")
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
	funcs, err := extractFunctions(root, srcBytes, root.Language())
	if err != nil {
		t.Fatal(err)
	}
	methods, err := extractMethods(root, srcBytes, root.Language())
	if err != nil {
		t.Fatal(err)
	}
	all := append(funcs, methods...)

	// Estimate compact output size
	totalBytes := 0
	for _, f := range all {
		// Name + tab + signature + tab + line range + tab + receiver + newline
		line := f.Name + "\t" + f.Signature + "\t"
		totalBytes += len(line) + 20 // padding for line ranges and receiver
	}

	if totalBytes > 800 {
		t.Errorf("compact output estimate %d bytes exceeds 800 byte budget", totalBytes)
	}
}
