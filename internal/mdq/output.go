package mdq

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
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

// GroupEntry represents a directory group with its file count and titles.
type GroupEntry struct {
	Folder string   `json:"folder"`
	Count  int      `json:"count"`
	Titles []string `json:"titles"`
}

// GroupByDir groups query results by parent directory.
func GroupByDir(results []QueryResult) []GroupEntry {
	orderMap := map[string]int{}    // first-seen order
	groups := map[string][]string{} // folder -> titles

	for _, r := range results {
		folder := filepath.Base(filepath.Dir(r.File))
		if _, seen := orderMap[folder]; !seen {
			orderMap[folder] = len(orderMap)
		}
		groups[folder] = append(groups[folder], r.Value)
	}

	entries := make([]GroupEntry, 0, len(groups))
	for folder, titles := range groups {
		entries = append(entries, GroupEntry{
			Folder: folder,
			Count:  len(titles),
			Titles: titles,
		})
	}

	// Sort by first-seen order (stable directory ordering from glob).
	sort.Slice(entries, func(i, j int) bool {
		return orderMap[entries[i].Folder] < orderMap[entries[j].Folder]
	})

	return entries
}

// FormatGrouped renders grouped results in the specified format.
func FormatGrouped(groups []GroupEntry, format string) string {
	if len(groups) == 0 {
		return ""
	}
	switch format {
	case "json":
		return formatGroupedJSON(groups)
	case "table":
		return formatGroupedTable(groups)
	default:
		return formatGroupedText(groups)
	}
}

func formatGroupedText(groups []GroupEntry) string {
	var b strings.Builder
	for _, g := range groups {
		fmt.Fprintf(&b, "%s/ (%d files)\n", g.Folder, g.Count)
		for _, t := range g.Titles {
			fmt.Fprintf(&b, "  • %s\n", t)
		}
	}
	return b.String()
}

func formatGroupedJSON(groups []GroupEntry) string {
	data, err := json.MarshalIndent(groups, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return string(data) + "\n"
}

func formatGroupedTable(groups []GroupEntry) string {
	folderW, countW, titlesW := len("FOLDER"), len("COUNT"), len("TITLES")
	for _, g := range groups {
		if len(g.Folder) > folderW {
			folderW = len(g.Folder)
		}
		countStr := fmt.Sprintf("%d", g.Count)
		if len(countStr) > countW {
			countW = len(countStr)
		}
		joined := strings.Join(g.Titles, ", ")
		if len(joined) > titlesW {
			titlesW = len(joined)
		}
	}

	var b strings.Builder
	fmtStr := fmt.Sprintf("%%-%ds  %%%ds  %%-%ds\n", folderW, countW, titlesW)
	fmt.Fprintf(&b, fmtStr, "FOLDER", "COUNT", "TITLES")
	fmt.Fprintf(&b, "%s  %s  %s\n", strings.Repeat("-", folderW), strings.Repeat("-", countW), strings.Repeat("-", titlesW))
	for _, g := range groups {
		fmt.Fprintf(&b, fmtStr, g.Folder, fmt.Sprintf("%d", g.Count), strings.Join(g.Titles, ", "))
	}
	return b.String()
}
