package main

import (
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	"github.com/spf13/cobra"
)

// byteEdit represents a single replacement in a source file.
type byteEdit struct {
	startByte   uint
	endByte     uint
	replacement []byte
}

var (
	rewriteDiff  bool
	rewriteWrite bool
)

func newRewriteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rewrite <pattern> <replacement> <glob>",
		Short: "Rewrite Go source using tree-sitter pattern matching",
		Long: `Rewrite Go source files by matching a tree-sitter S-expression pattern
and replacing matches with a template string.

The replacement template can reference captures using @capture_name syntax.
After replacement, the output is formatted with gofmt.

By default, patched source is printed to stdout. Use --write to modify
files in-place, or --diff to preview changes as a unified diff.

The --format flag is ignored for rewrite (output is always Go source or diff).

Supported predicates: #eq? (exact text match on captures).
Unsupported predicates (#match?, #any-of?, etc.) are silently ignored.

Examples:
  ts-go rewrite '(function_declaration name: (identifier) @name)' 'func renamed_@name()' '*.go'
  ts-go rewrite '(call_expression function: (identifier) @fn)' '@fn' 'cmd/*.go' --diff
  ts-go rewrite '(comment) @c' '' 'main.go' --write`,
		Args: cobra.ExactArgs(3),
		RunE: runRewrite,
	}

	cmd.Flags().BoolVar(&rewriteDiff, "diff", false, "show unified diff instead of patched source")
	cmd.Flags().BoolVar(&rewriteWrite, "write", false, "modify files in-place")

	return cmd
}

func runRewrite(cmd *cobra.Command, args []string) error {
	pattern := args[0]
	replacement := args[1]
	glob := args[2]

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

	for _, f := range files {
		if err := rewriteFile(f, query, lang, replacement); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", f, err)
		}
	}

	return nil
}

func rewriteFile(path string, query *tree_sitter.Query, lang *tree_sitter.Language, replacementTpl string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	parser := tree_sitter.NewParser()
	if err := parser.SetLanguage(lang); err != nil {
		parser.Close()
		return fmt.Errorf("set language: %w", err)
	}
	defer parser.Close()

	tree := parser.Parse(src, nil)
	if tree == nil {
		return fmt.Errorf("parse %s: tree-sitter returned nil", path)
	}
	defer tree.Close()

	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()
	cursor.SetTimeoutMicros(5_000_000)

	matches := cursor.Matches(query, tree.RootNode(), src)

	var edits []byteEdit
	for {
		match := matches.Next()
		if match == nil {
			break
		}
		if len(match.Captures) == 0 {
			continue
		}

		// Build capture map for this match.
		captures := make(map[string]string)
		for _, c := range match.Captures {
			name := query.CaptureNames()[c.Index]
			captures[name] = c.Node.Utf8Text(src)
		}

		// Compute match range spanning all captures.
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

		replacement := interpolateCaptures(replacementTpl, captures)

		edits = append(edits, byteEdit{
			startByte:   startByte,
			endByte:     endByte,
			replacement: []byte(replacement),
		})
	}

	if len(edits) == 0 {
		return nil
	}

	edits = eliminateOverlaps(edits)
	patched := applyEdits(src, edits)

	// gofmt post-processing.
	formatted, err := format.Source(patched)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: gofmt failed for %s, using raw output: %v\n", path, err)
		formatted = patched
	}

	// Post-rewrite syntax validation.
	validateSyntax(path, formatted, lang)

	if rewriteDiff {
		diff := unifiedDiff(path, src, formatted)
		if diff != "" {
			fmt.Print(diff)
		}
		return nil
	}

	if rewriteWrite {
		return os.WriteFile(path, formatted, 0o644)
	}

	// Default: print to stdout.
	_, err = os.Stdout.Write(formatted)
	return err
}

// capturePattern matches @capture_name references in replacement templates.
var capturePattern = regexp.MustCompile(`@([a-zA-Z_][a-zA-Z0-9_.]*)`)

// interpolateCaptures replaces @capture_name references with captured text.
func interpolateCaptures(tpl string, captures map[string]string) string {
	return capturePattern.ReplaceAllStringFunc(tpl, func(match string) string {
		name := match[1:] // strip leading @
		if val, ok := captures[name]; ok {
			return val
		}
		return match // leave unresolved captures as-is
	})
}

// eliminateOverlaps sorts edits by startByte and removes overlapping edits.
func eliminateOverlaps(edits []byteEdit) []byteEdit {
	sort.Slice(edits, func(i, j int) bool {
		return edits[i].startByte < edits[j].startByte
	})

	var result []byteEdit
	var prevEnd uint
	for _, e := range edits {
		if e.startByte < prevEnd {
			continue // overlapping, skip
		}
		result = append(result, e)
		prevEnd = e.endByte
	}
	return result
}

// applyEdits applies byte edits in reverse order to avoid offset invalidation.
func applyEdits(src []byte, edits []byteEdit) []byte {
	patched := make([]byte, len(src))
	copy(patched, src)

	// Apply in reverse order so byte offsets remain valid.
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		before := patched[:e.startByte]
		after := patched[e.endByte:]
		patched = make([]byte, len(before)+len(e.replacement)+len(after))
		copy(patched, before)
		copy(patched[len(before):], e.replacement)
		copy(patched[len(before)+len(e.replacement):], after)
	}

	return patched
}

// validateSyntax re-parses patched source and warns on stderr if it has errors.
func validateSyntax(path string, src []byte, lang *tree_sitter.Language) {
	parser := tree_sitter.NewParser()
	if err := parser.SetLanguage(lang); err != nil {
		parser.Close()
		return
	}
	defer parser.Close()

	tree := parser.Parse(src, nil)
	if tree == nil {
		fmt.Fprintf(os.Stderr, "warning: post-rewrite parse failed for %s\n", path)
		return
	}
	defer tree.Close()

	if tree.RootNode().HasError() {
		fmt.Fprintf(os.Stderr, "warning: post-rewrite syntax errors detected in %s\n", path)
	}
}

// unifiedDiff produces a unified diff between original and patched content.
func unifiedDiff(path string, original, patched []byte) string {
	origLines := splitLines(string(original))
	patchLines := splitLines(string(patched))

	if len(origLines) == len(patchLines) {
		same := true
		for i := range origLines {
			if origLines[i] != patchLines[i] {
				same = false
				break
			}
		}
		if same {
			return ""
		}
	}

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("--- a/%s\n", filepath.Base(path)))
	buf.WriteString(fmt.Sprintf("+++ b/%s\n", filepath.Base(path)))

	// Simple line-by-line diff with context.
	const contextLines = 3
	type change struct {
		origStart, origEnd   int
		patchStart, patchEnd int
	}

	// Find changed regions using LCS-based diff.
	changes := diffLines(origLines, patchLines)
	if len(changes) == 0 {
		return ""
	}

	for _, c := range changes {
		// Compute context bounds.
		ctxStart := c.origStart - contextLines
		if ctxStart < 0 {
			ctxStart = 0
		}
		ctxEnd := c.origEnd + contextLines
		if ctxEnd > len(origLines) {
			ctxEnd = len(origLines)
		}
		pCtxStart := c.patchStart - contextLines
		if pCtxStart < 0 {
			pCtxStart = 0
		}
		pCtxEnd := c.patchEnd + contextLines
		if pCtxEnd > len(patchLines) {
			pCtxEnd = len(patchLines)
		}

		origLen := ctxEnd - ctxStart
		patchLen := pCtxEnd - pCtxStart
		buf.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", ctxStart+1, origLen, pCtxStart+1, patchLen))

		// Context before.
		for i := ctxStart; i < c.origStart; i++ {
			buf.WriteString(" " + origLines[i] + "\n")
		}
		// Removed lines.
		for i := c.origStart; i < c.origEnd; i++ {
			buf.WriteString("-" + origLines[i] + "\n")
		}
		// Added lines.
		for i := c.patchStart; i < c.patchEnd; i++ {
			buf.WriteString("+" + patchLines[i] + "\n")
		}
		// Context after.
		for i := c.origEnd; i < ctxEnd; i++ {
			buf.WriteString(" " + origLines[i] + "\n")
		}
	}

	return buf.String()
}

// splitLines splits text into lines without trailing newlines.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// Remove trailing empty line from trailing newline.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type lineChange struct {
	origStart, origEnd   int
	patchStart, patchEnd int
}

// diffLines computes changed regions between two line slices.
func diffLines(a, b []string) []lineChange {
	// Build a simple O(n*m) edit script via dynamic programming.
	n, m := len(a), len(b)

	// For very large files, fall back to a simpler approach.
	if n*m > 10_000_000 {
		return simpleDiff(a, b)
	}

	// LCS table.
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// Trace back to find edit operations.
	type op struct {
		kind byte // '=' same, '-' delete from a, '+' insert from b
		aIdx int
		bIdx int
	}

	var ops []op
	i, j := n, m
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && a[i-1] == b[j-1] {
			ops = append(ops, op{'=', i - 1, j - 1})
			i--
			j--
		} else if i > 0 && (j == 0 || dp[i-1][j] >= dp[i][j-1]) {
			ops = append(ops, op{'-', i - 1, 0})
			i--
		} else {
			ops = append(ops, op{'+', 0, j - 1})
			j--
		}
	}

	// Reverse ops (we built them backwards).
	for l, r := 0, len(ops)-1; l < r; l, r = l+1, r-1 {
		ops[l], ops[r] = ops[r], ops[l]
	}

	// Collapse consecutive changes into lineChange regions.
	var changes []lineChange
	idx := 0
	for idx < len(ops) {
		if ops[idx].kind == '=' {
			idx++
			continue
		}

		origStart, origEnd := -1, -1
		patchStart, patchEnd := -1, -1

		for idx < len(ops) && ops[idx].kind != '=' {
			o := ops[idx]
			if o.kind == '-' {
				if origStart == -1 {
					origStart = o.aIdx
				}
				origEnd = o.aIdx + 1
			} else { // '+'
				if patchStart == -1 {
					patchStart = o.bIdx
				}
				patchEnd = o.bIdx + 1
			}
			idx++
		}

		if origStart == -1 {
			origStart = 0
			origEnd = 0
			// Find the position in original for pure insertions.
			if idx < len(ops) {
				origStart = ops[idx].aIdx
				origEnd = origStart
			}
		}
		if patchStart == -1 {
			patchStart = 0
			patchEnd = 0
			if idx < len(ops) {
				patchStart = ops[idx].bIdx
				patchEnd = patchStart
			}
		}

		changes = append(changes, lineChange{origStart, origEnd, patchStart, patchEnd})
	}

	return changes
}

// simpleDiff falls back to treating the entire file as one change.
func simpleDiff(a, b []string) []lineChange {
	return []lineChange{{0, len(a), 0, len(b)}}
}
