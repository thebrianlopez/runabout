package mdq

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Format renders query results in the specified format (text, json, table).
func Format(results []QueryResult, format string) string {
	if len(results) == 0 {
		return ""
	}

	switch format {
	case "json":
		return formatJSON(results)
	case "table":
		return formatTable(results)
	default:
		return formatText(results)
	}
}

func formatText(results []QueryResult) string {
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "%s: %s = %s\n", r.File, r.Field, r.Value)
	}
	return b.String()
}

func formatJSON(results []QueryResult) string {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return string(data) + "\n"
}

func formatTable(results []QueryResult) string {
	if len(results) == 0 {
		return ""
	}

	// Calculate column widths.
	fileW, fieldW, valueW := len("FILE"), len("FIELD"), len("VALUE")
	for _, r := range results {
		if len(r.File) > fileW {
			fileW = len(r.File)
		}
		if len(r.Field) > fieldW {
			fieldW = len(r.Field)
		}
		if len(r.Value) > valueW {
			valueW = len(r.Value)
		}
	}

	var b strings.Builder
	fmtStr := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds\n", fileW, fieldW, valueW)
	fmt.Fprintf(&b, fmtStr, "FILE", "FIELD", "VALUE")
	fmt.Fprintf(&b, "%s  %s  %s\n", strings.Repeat("-", fileW), strings.Repeat("-", fieldW), strings.Repeat("-", valueW))
	for _, r := range results {
		fmt.Fprintf(&b, fmtStr, r.File, r.Field, r.Value)
	}
	return b.String()
}

// FormatTable renders a parsed Table as aligned text.
func FormatTable(t Table) string {
	if len(t.Headers) == 0 {
		return ""
	}

	// Calculate column widths.
	widths := make([]int, len(t.Headers))
	for i, h := range t.Headers {
		widths[i] = len(h)
	}
	for _, row := range t.Rows {
		for i, h := range t.Headers {
			if val := row.Cells[h]; len(val) > widths[i] {
				widths[i] = len(val)
			}
		}
	}

	var b strings.Builder
	// Header row.
	for i, h := range t.Headers {
		if i > 0 {
			b.WriteString("  ")
		}
		fmt.Fprintf(&b, "%-*s", widths[i], h)
	}
	b.WriteString("\n")

	// Separator.
	for i, w := range widths {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(strings.Repeat("-", w))
	}
	b.WriteString("\n")

	// Data rows.
	for _, row := range t.Rows {
		for i, h := range t.Headers {
			if i > 0 {
				b.WriteString("  ")
			}
			fmt.Fprintf(&b, "%-*s", widths[i], row.Cells[h])
		}
		b.WriteString("\n")
	}
	return b.String()
}

// FormatSection renders a section's content as text.
func FormatSection(s *Section) string {
	var b strings.Builder
	b.WriteString(s.Content)
	for _, t := range s.Tables {
		b.WriteString("\n\n")
		b.WriteString(FormatTable(t))
	}
	return b.String()
}
