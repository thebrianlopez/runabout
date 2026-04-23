package main

import (
	"fmt"
	"os"
	"path/filepath"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

// parseFile reads a Go source file and returns the parsed tree, source bytes,
// and the parser. Callers must call tree.Close() and parser.Close() when done.
func parseFile(path string) (*tree_sitter.Tree, []byte, *tree_sitter.Parser, error) {
	if filepath.Ext(path) != ".go" {
		return nil, nil, nil, fmt.Errorf("not a Go file: %s", path)
	}

	src, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read %s: %w", path, err)
	}

	parser := tree_sitter.NewParser()
	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())
	if err := parser.SetLanguage(lang); err != nil {
		parser.Close()
		return nil, nil, nil, fmt.Errorf("set language: %w", err)
	}

	tree := parser.Parse(src, nil)
	if tree == nil {
		parser.Close()
		return nil, nil, nil, fmt.Errorf("parse %s: tree-sitter returned nil", path)
	}

	return tree, src, parser, nil
}
