package hookval

import (
	"fmt"
	"sort"
	"strings"
)

// GenDocsTable returns a markdown table of all signals, sorted by name.
// Format matches the Hook Context Signals section in CLAUDE.md.
func GenDocsTable(schema *Schema) string {
	names := make([]string, 0, len(schema.Signals))
	for name := range schema.Signals {
		names = append(names, name)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString("| Signal | Type | Format / Domain | Description |\n")
	sb.WriteString("|--------|------|-----------------|-------------|\n")

	for _, name := range names {
		def := schema.Signals[name]
		domain := formatDomain(def)
		sb.WriteString(fmt.Sprintf("| `%s` | `%s` | %s | %s |\n",
			name, def.Type, domain, def.Description))
	}

	return sb.String()
}

func formatDomain(def SignalDef) string {
	switch def.Type {
	case "enum":
		quoted := make([]string, len(def.Values))
		for i, v := range def.Values {
			quoted[i] = "`" + v + "`"
		}
		return strings.Join(quoted, ", ")
	case "integer_or_literal", "iso8601_utc":
		parts := []string{"`" + def.Pattern + "`"}
		for _, l := range def.Literals {
			parts = append(parts, "`"+l+"`")
		}
		return strings.Join(parts, " or ")
	case "string_or_literal":
		parts := []string{"non-empty string"}
		for _, l := range def.Literals {
			parts = append(parts, "`"+l+"`")
		}
		return strings.Join(parts, " or ")
	case "string":
		return "non-empty string"
	case "path":
		return "absolute path"
	default:
		return def.Type
	}
}
