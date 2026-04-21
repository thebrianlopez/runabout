package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/spf13/cobra"
)

// TypeInfo describes a type declaration.
type TypeInfo struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`                  // "struct" | "interface" | "alias"
	StartLine  uint   `json:"start_line"`
	EndLine    uint   `json:"end_line"`
	StartByte  uint   `json:"start_byte,omitempty"`
	EndByte    uint   `json:"end_byte,omitempty"`
	FieldCount int    `json:"field_count,omitempty"`
}

func newTypesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "types <file>",
		Short: "List type declarations",
		Args:  cobra.ExactArgs(1),
		RunE:  runTypes,
	}
}

func runTypes(cmd *cobra.Command, args []string) error {
	tree, src, parser, err := parseFile(args[0])
	if err != nil {
		return err
	}
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	lang := root.Language()

	types, err := extractTypes(root, src, lang)
	if err != nil {
		return err
	}

	switch formatFlag {
	case "compact":
		return printTypesCompact(types)
	default:
		return printTypesJSON(types)
	}
}

func extractTypes(root *tree_sitter.Node, src []byte, lang *tree_sitter.Language) ([]TypeInfo, error) {
	q, qErr := tree_sitter.NewQuery(lang, queryTypeDeclarations)
	if qErr != nil {
		return nil, fmt.Errorf("compile type query: %s", qErr.Message)
	}
	defer q.Close()

	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(q, root, src)
	captureNames := q.CaptureNames()

	var types []TypeInfo
	for {
		match := matches.Next()
		if match == nil {
			break
		}

		var info TypeInfo

		for _, cap := range match.Captures {
			name := captureNames[cap.Index]
			node := &cap.Node
			switch name {
			case "type.name":
				info.Name = node.Utf8Text(src)
			case "type.body":
				info.Kind = classifyType(node)
				info.FieldCount = countFields(node)
			case "type.decl":
				info.StartLine = node.StartPosition().Row + 1
				info.EndLine = node.EndPosition().Row + 1
				info.StartByte = uint(node.StartByte())
				info.EndByte = uint(node.EndByte())
			}
		}

		types = append(types, info)
	}

	// Also query type aliases (type X = Y)
	aliases, err := extractTypeAliases(root, src, lang)
	if err != nil {
		return nil, err
	}
	types = append(types, aliases...)

	return types, nil
}

func extractTypeAliases(root *tree_sitter.Node, src []byte, lang *tree_sitter.Language) ([]TypeInfo, error) {
	q, qErr := tree_sitter.NewQuery(lang, queryTypeAliases)
	if qErr != nil {
		return nil, fmt.Errorf("compile alias query: %s", qErr.Message)
	}
	defer q.Close()

	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(q, root, src)
	captureNames := q.CaptureNames()

	var aliases []TypeInfo
	for {
		match := matches.Next()
		if match == nil {
			break
		}

		var info TypeInfo
		info.Kind = "alias"

		for _, cap := range match.Captures {
			name := captureNames[cap.Index]
			node := &cap.Node
			switch name {
			case "alias.name":
				info.Name = node.Utf8Text(src)
			case "alias.decl":
				info.StartLine = node.StartPosition().Row + 1
				info.EndLine = node.EndPosition().Row + 1
				info.StartByte = uint(node.StartByte())
				info.EndByte = uint(node.EndByte())
			}
		}

		aliases = append(aliases, info)
	}

	return aliases, nil
}

// classifyType returns "struct", "interface", or "alias" based on the type body node.
func classifyType(node *tree_sitter.Node) string {
	switch node.Kind() {
	case "struct_type":
		return "struct"
	case "interface_type":
		return "interface"
	default:
		return "alias"
	}
}

// countFields counts field declarations in a struct or method specs in an interface.
func countFields(node *tree_sitter.Node) int {
	count := 0
	switch node.Kind() {
	case "struct_type":
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child != nil && child.Kind() == "field_declaration_list" {
				for j := uint(0); j < child.ChildCount(); j++ {
					fc := child.Child(j)
					if fc != nil && fc.Kind() == "field_declaration" {
						count++
					}
				}
			}
		}
	case "interface_type":
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child != nil && child.Kind() == "method_spec_list" {
				for j := uint(0); j < child.ChildCount(); j++ {
					mc := child.Child(j)
					if mc != nil && (mc.Kind() == "method_spec" || mc.Kind() == "type_identifier" || mc.Kind() == "qualified_type") {
						count++
					}
				}
			}
		}
	}
	return count
}

func printTypesJSON(types []TypeInfo) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(types)
}

func printTypesCompact(types []TypeInfo) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, t := range types {
		fields := ""
		if t.FieldCount > 0 {
			fields = fmt.Sprintf("%d fields", t.FieldCount)
		}
		fmt.Fprintf(w, "%s\t%s\tL%d-L%d\t%s\n", t.Name, t.Kind, t.StartLine, t.EndLine, fields)
	}
	return w.Flush()
}
