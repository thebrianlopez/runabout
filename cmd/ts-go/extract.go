package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/spf13/cobra"
)

// ExtractResult holds the extracted function body and metadata.
type ExtractResult struct {
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	StartLine  uint    `json:"start_line"`
	EndLine    uint    `json:"end_line"`
	Receiver   *string `json:"receiver,omitempty"`
	Body       string  `json:"body"`
	DocComment string  `json:"doc_comment,omitempty"`
}

func newExtractCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "extract <file> <name>",
		Short: "Extract a function or method body by name",
		Args:  cobra.ExactArgs(2),
		RunE:  runExtract,
	}
}

func runExtract(cmd *cobra.Command, args []string) error {
	filePath := args[0]
	funcName := args[1]

	tree, src, parser, err := parseFile(filePath)
	if err != nil {
		return err
	}
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	lang := root.Language()

	results, err := findDeclarations(root, src, lang, funcName)
	if err != nil {
		return err
	}

	if len(results) == 0 {
		return fmt.Errorf("no function or method named %q found in %s", funcName, filePath)
	}

	switch formatFlag {
	case "compact":
		return printExtractCompact(results)
	default:
		return printExtractJSON(results)
	}
}

func findDeclarations(root *tree_sitter.Node, src []byte, lang *tree_sitter.Language, name string) ([]ExtractResult, error) {
	var results []ExtractResult

	funcResults, err := findFuncDeclarations(root, src, lang, name)
	if err != nil {
		return nil, err
	}
	results = append(results, funcResults...)

	methodResults, err := findMethodDeclarations(root, src, lang, name)
	if err != nil {
		return nil, err
	}
	results = append(results, methodResults...)

	return results, nil
}

func findFuncDeclarations(root *tree_sitter.Node, src []byte, lang *tree_sitter.Language, name string) ([]ExtractResult, error) {
	q, qErr := tree_sitter.NewQuery(lang, queryFuncDeclarations)
	if qErr != nil {
		return nil, fmt.Errorf("compile func query: %s", qErr.Message)
	}
	defer q.Close()

	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(q, root, src)
	captureNames := q.CaptureNames()

	var results []ExtractResult
	for {
		match := matches.Next()
		if match == nil {
			break
		}

		var funcName string
		var declNode *tree_sitter.Node

		for _, cap := range match.Captures {
			capName := captureNames[cap.Index]
			node := cap.Node
			switch capName {
			case "func.name":
				funcName = node.Utf8Text(src)
			case "func.decl":
				declNode = &node
			}
		}

		if funcName != name || declNode == nil {
			continue
		}

		result := ExtractResult{
			Name:      funcName,
			Kind:      "function",
			StartLine: declNode.StartPosition().Row + 1,
			EndLine:   declNode.EndPosition().Row + 1,
			Body:      declNode.Utf8Text(src),
		}

		result.DocComment = findDocComment(root, src, declNode)
		results = append(results, result)
	}

	return results, nil
}

func findMethodDeclarations(root *tree_sitter.Node, src []byte, lang *tree_sitter.Language, name string) ([]ExtractResult, error) {
	q, qErr := tree_sitter.NewQuery(lang, queryMethodDeclarations)
	if qErr != nil {
		return nil, fmt.Errorf("compile method query: %s", qErr.Message)
	}
	defer q.Close()

	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(q, root, src)
	captureNames := q.CaptureNames()

	var results []ExtractResult
	for {
		match := matches.Next()
		if match == nil {
			break
		}

		var methodName string
		var declNode *tree_sitter.Node
		var recvText string

		for _, cap := range match.Captures {
			capName := captureNames[cap.Index]
			node := cap.Node
			switch capName {
			case "method.name":
				methodName = node.Utf8Text(src)
			case "method.receiver":
				recvText = extractReceiverType(&node, src)
			case "method.decl":
				declNode = &node
			}
		}

		if methodName != name || declNode == nil {
			continue
		}

		result := ExtractResult{
			Name:      methodName,
			Kind:      "method",
			StartLine: declNode.StartPosition().Row + 1,
			EndLine:   declNode.EndPosition().Row + 1,
			Receiver:  &recvText,
			Body:      declNode.Utf8Text(src),
		}

		result.DocComment = findDocComment(root, src, declNode)
		results = append(results, result)
	}

	return results, nil
}

// findDocComment looks for a comment block immediately preceding the declaration node.
func findDocComment(root *tree_sitter.Node, src []byte, declNode *tree_sitter.Node) string {
	declRow := declNode.StartPosition().Row
	if declRow == 0 {
		return ""
	}

	var commentLines []string
	parent := declNode.Parent()
	if parent == nil {
		return ""
	}

	for i := uint(0); i < parent.ChildCount(); i++ {
		child := parent.Child(i)
		if child == nil {
			continue
		}
		if child.StartPosition().Row == declNode.StartPosition().Row && child.Kind() == declNode.Kind() {
			break
		}
		if child.Kind() == "comment" && child.EndPosition().Row+1 >= declRow {
			commentLines = append(commentLines, child.Utf8Text(src))
		} else if child.EndPosition().Row+1 < declRow {
			commentLines = nil
		}
	}

	return strings.Join(commentLines, "\n")
}

func printExtractJSON(results []ExtractResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if len(results) == 1 {
		return enc.Encode(results[0])
	}
	return enc.Encode(results)
}

func printExtractCompact(results []ExtractResult) error {
	for i, r := range results {
		if i > 0 {
			fmt.Println("---")
		}
		if r.DocComment != "" {
			fmt.Println(r.DocComment)
		}
		fmt.Println(r.Body)
	}
	return nil
}
