package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

// SearchResult holds a single pattern match with capture information.
type SearchResult struct {
	File        string            `json:"file"`
	StartLine   uint              `json:"start_line"`
	EndLine     uint              `json:"end_line"`
	StartByte   uint              `json:"start_byte"`
	EndByte     uint              `json:"end_byte"`
	MatchedText string            `json:"matched_text"`
	Captures    map[string]string `json:"captures,omitempty"`
}

func newSearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search <pattern> <glob>",
		Short: "Search Go files for tree-sitter S-expression patterns",
		Long: `Search Go source files using tree-sitter S-expression queries.

The pattern is a tree-sitter query in S-expression syntax. The glob
is a file path pattern (e.g. 'cmd/linkari/*.go') expanded by the tool.

Supported predicates: #eq? (exact text match).
Unsupported predicates are silently ignored.

Examples:
  ts-go search '(function_declaration name: (identifier) @name)' '*.go'
  ts-go search '(call_expression function: (identifier) @fn (#eq? @fn "fmt.Println"))' 'cmd/**/*.go'`,
		Args: cobra.ExactArgs(2),
		RunE: runSearch,
	}
}

func runSearch(cmd *cobra.Command, args []string) error {
	pattern := args[0]
	glob := args[1]

	files, err := expandGlob(glob)
	if err != nil {
		return err
	}

	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())

	query, err := compileQuery(lang, pattern)
	if err != nil {
		return err
	}
	defer query.Close()

	var results []SearchResult
	for _, f := range files {
		matches, err := searchFile(f, query, lang)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", f, err)
			continue
		}
		results = append(results, matches...)
	}

	switch formatFlag {
	case "compact":
		return printSearchCompact(results)
	default:
		return printSearchJSON(results)
	}
}

// compileQuery wraps tree_sitter.NewQuery with structured error propagation.
func compileQuery(lang *tree_sitter.Language, src string) (*tree_sitter.Query, error) {
	q, qErr := tree_sitter.NewQuery(lang, src)
	if qErr != nil {
		return nil, fmt.Errorf("query compilation error: %s", qErr.Error())
	}
	return q, nil
}

// expandGlob expands a glob pattern and returns sorted .go files.
func expandGlob(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid glob %q: %w", pattern, err)
	}

	var goFiles []string
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if info.IsDir() {
			dirFiles, err := walkDir(m)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: walking %s: %v\n", m, err)
				continue
			}
			goFiles = append(goFiles, dirFiles...)
		} else if strings.HasSuffix(m, ".go") {
			goFiles = append(goFiles, m)
		}
	}

	sort.Strings(goFiles)
	return goFiles, nil
}

// walkDir walks a directory tree and returns all .go files.
func walkDir(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// searchFile runs the query against a single file and returns matches.
func searchFile(path string, query *tree_sitter.Query, lang *tree_sitter.Language) ([]SearchResult, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	parser := tree_sitter.NewParser()
	if err := parser.SetLanguage(lang); err != nil {
		parser.Close()
		return nil, fmt.Errorf("set language: %w", err)
	}
	defer parser.Close()

	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse %s: tree-sitter returned nil", path)
	}
	defer tree.Close()

	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()
	cursor.SetTimeoutMicros(5_000_000)

	// cursor.Matches evaluates #eq? text predicates automatically.
	matches := cursor.Matches(query, tree.RootNode(), src)

	var results []SearchResult
	for {
		match := matches.Next()
		if match == nil {
			break
		}

		if len(match.Captures) == 0 {
			continue
		}

		// Compute the byte range spanning all captures in this match.
		startByte := uint(match.Captures[0].Node.StartByte())
		endByte := uint(match.Captures[0].Node.EndByte())
		for _, c := range match.Captures[1:] {
			if sb := uint(c.Node.StartByte()); sb < startByte {
				startByte = sb
			}
			if eb := uint(c.Node.EndByte()); eb > endByte {
				endByte = eb
			}
		}

		captures := make(map[string]string)
		for _, c := range match.Captures {
			name := query.CaptureNames()[c.Index]
			captures[name] = c.Node.Utf8Text(src)
		}

		r := SearchResult{
			File:        path,
			StartLine:   uint(lineAt(src, startByte)),
			EndLine:     uint(lineAt(src, endByte)),
			StartByte:   startByte,
			EndByte:     endByte,
			MatchedText: string(src[startByte:endByte]),
			Captures:    captures,
		}
		results = append(results, r)
	}

	return results, nil
}

// lineAt returns the 1-based line number for a byte offset.
func lineAt(src []byte, offset uint) int {
	line := 1
	for i := uint(0); i < offset && i < uint(len(src)); i++ {
		if src[i] == '\n' {
			line++
		}
	}
	return line
}

func printSearchJSON(results []SearchResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

func printSearchCompact(results []SearchResult) error {
	if len(results) == 0 {
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "FILE\tLINE\tMATCH\tCAPTURES")
	for _, r := range results {
		capParts := make([]string, 0, len(r.Captures))
		for k, v := range r.Captures {
			capParts = append(capParts, fmt.Sprintf("@%s=%s", k, v))
		}
		sort.Strings(capParts)
		matchPreview := truncate(strings.ReplaceAll(r.MatchedText, "\n", "\\n"), 60)
		fmt.Fprintf(w, "%s\t%d-%d\t%s\t%s\n", r.File, r.StartLine, r.EndLine, matchPreview, strings.Join(capParts, " "))
	}
	return w.Flush()
}

// truncate shortens s to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
