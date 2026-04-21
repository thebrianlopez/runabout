package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/spf13/cobra"
)

// FuncInfo describes a function or method declaration.
type FuncInfo struct {
	Name             string  `json:"name"`
	Kind             string  `json:"kind"`                         // "function" | "method"
	Signature        string  `json:"signature"`
	StartLine        uint    `json:"start_line"`
	EndLine          uint    `json:"end_line"`
	StartByte        uint    `json:"start_byte,omitempty"`
	EndByte          uint    `json:"end_byte,omitempty"`
	Receiver         *string `json:"receiver,omitempty"`
	BuildConstraints *string `json:"build_constraints,omitempty"`
}

func newFuncsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "funcs <file>",
		Short: "List function and method declarations",
		Args:  cobra.ExactArgs(1),
		RunE:  runFuncs,
	}
}

func runFuncs(cmd *cobra.Command, args []string) error {
	tree, src, parser, err := parseFile(args[0])
	if err != nil {
		return err
	}
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	lang := root.Language()

	funcs, err := extractFunctions(root, src, lang)
	if err != nil {
		return err
	}

	methods, err := extractMethods(root, src, lang)
	if err != nil {
		return err
	}

	all := append(funcs, methods...)

	// Parse file-level build constraints
	if bc := parseBuildConstraints(src); bc != "" {
		for i := range all {
			all[i].BuildConstraints = &bc
		}
	}

	switch formatFlag {
	case "compact":
		return printFuncsCompact(all)
	default:
		return printFuncsJSON(all)
	}
}

func extractFunctions(root *tree_sitter.Node, src []byte, lang *tree_sitter.Language) ([]FuncInfo, error) {
	q, qErr := tree_sitter.NewQuery(lang, queryFuncDeclarations)
	if qErr != nil {
		return nil, fmt.Errorf("compile func query: %s", qErr.Message)
	}
	defer q.Close()

	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(q, root, src)
	captureNames := q.CaptureNames()

	var funcs []FuncInfo
	for {
		match := matches.Next()
		if match == nil {
			break
		}

		var info FuncInfo
		info.Kind = "function"

		for _, cap := range match.Captures {
			name := captureNames[cap.Index]
			node := &cap.Node
			switch name {
			case "func.name":
				info.Name = node.Utf8Text(src)
			case "func.decl":
				info.StartLine = node.StartPosition().Row + 1
				info.EndLine = node.EndPosition().Row + 1
				info.StartByte = uint(node.StartByte())
				info.EndByte = uint(node.EndByte())
				info.Signature = buildFuncSignature(node, src)
			}
		}

		funcs = append(funcs, info)
	}

	return funcs, nil
}

func extractMethods(root *tree_sitter.Node, src []byte, lang *tree_sitter.Language) ([]FuncInfo, error) {
	q, qErr := tree_sitter.NewQuery(lang, queryMethodDeclarations)
	if qErr != nil {
		return nil, fmt.Errorf("compile method query: %s", qErr.Message)
	}
	defer q.Close()

	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(q, root, src)
	captureNames := q.CaptureNames()

	var methods []FuncInfo
	for {
		match := matches.Next()
		if match == nil {
			break
		}

		var info FuncInfo
		info.Kind = "method"

		for _, cap := range match.Captures {
			name := captureNames[cap.Index]
			node := &cap.Node
			switch name {
			case "method.name":
				info.Name = node.Utf8Text(src)
			case "method.receiver":
				recv := extractReceiverType(node, src)
				info.Receiver = &recv
			case "method.decl":
				info.StartLine = node.StartPosition().Row + 1
				info.EndLine = node.EndPosition().Row + 1
				info.StartByte = uint(node.StartByte())
				info.EndByte = uint(node.EndByte())
				info.Signature = buildMethodSignature(node, src)
			}
		}

		methods = append(methods, info)
	}

	return methods, nil
}

// buildFuncSignature reconstructs the function signature from the declaration node,
// excluding the body block.
func buildFuncSignature(node *tree_sitter.Node, src []byte) string {
	var parts []string
	parts = append(parts, "func")

	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		kind := child.Kind()
		switch kind {
		case "identifier":
			parts = append(parts, child.Utf8Text(src))
		case "type_parameters", "type_parameter_list":
			parts[len(parts)-1] += child.Utf8Text(src)
		case "parameter_list":
			parts[len(parts)-1] += child.Utf8Text(src)
		case "type_identifier", "pointer_type", "qualified_type",
			"slice_type", "map_type", "channel_type", "function_type",
			"generic_type", "parenthesized_type":
			parts = append(parts, child.Utf8Text(src))
		case "block":
			// skip the body
		}
	}

	return strings.Join(parts, " ")
}

// buildMethodSignature reconstructs the method signature from the declaration node.
func buildMethodSignature(node *tree_sitter.Node, src []byte) string {
	var parts []string
	parts = append(parts, "func")

	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		kind := child.Kind()
		switch kind {
		case "parameter_list":
			text := child.Utf8Text(src)
			if len(parts) == 1 {
				// First parameter_list is the receiver
				parts = append(parts, text)
			} else {
				// Subsequent parameter_list is the params or results
				parts[len(parts)-1] += text
			}
		case "field_identifier":
			parts = append(parts, child.Utf8Text(src))
		case "type_parameters", "type_parameter_list":
			parts[len(parts)-1] += child.Utf8Text(src)
		case "type_identifier", "pointer_type", "qualified_type",
			"slice_type", "map_type", "channel_type", "function_type",
			"generic_type", "parenthesized_type":
			parts = append(parts, child.Utf8Text(src))
		case "block":
			// skip the body
		}
	}

	return strings.Join(parts, " ")
}

// extractReceiverType extracts the type from a receiver parameter list node.
func extractReceiverType(paramList *tree_sitter.Node, src []byte) string {
	for i := uint(0); i < paramList.ChildCount(); i++ {
		child := paramList.Child(i)
		if child == nil {
			continue
		}
		if child.Kind() == "parameter_declaration" {
			for j := uint(0); j < child.ChildCount(); j++ {
				typeChild := child.Child(j)
				if typeChild == nil {
					continue
				}
				switch typeChild.Kind() {
				case "pointer_type", "type_identifier", "generic_type":
					return typeChild.Utf8Text(src)
				}
			}
		}
	}
	// Fallback: return the whole receiver text minus parens
	text := paramList.Utf8Text(src)
	text = strings.TrimPrefix(text, "(")
	text = strings.TrimSuffix(text, ")")
	parts := strings.Fields(text)
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return text
}

// parseBuildConstraints extracts the //go:build directive from source bytes.
func parseBuildConstraints(src []byte) string {
	for _, line := range strings.SplitN(string(src), "\n", 20) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//go:build ") {
			return strings.TrimPrefix(trimmed, "//go:build ")
		}
		// Stop scanning after package declaration
		if strings.HasPrefix(trimmed, "package ") {
			break
		}
	}
	return ""
}

func printFuncsJSON(funcs []FuncInfo) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(funcs)
}

func printFuncsCompact(funcs []FuncInfo) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, f := range funcs {
		recv := ""
		if f.Receiver != nil {
			recv = *f.Receiver
		}
		fmt.Fprintf(w, "%s\t%s\tL%d-L%d\t%s\n", f.Name, f.Signature, f.StartLine, f.EndLine, recv)
	}
	return w.Flush()
}
