package mdq

import (
	"bufio"
	"io"
	"regexp"
	"strings"
)

// Document represents a parsed markdown document.
type Document struct {
	Title    string
	Sections []Section
}

// Section represents a heading and its content.
type Section struct {
	Level    int
	Title    string
	Content  string
	Children []Section
	Tables   []Table
}

// Table represents a markdown table within a section.
type Table struct {
	Headers []string
	Rows    []TableRow
}

// TableRow represents a single row in a markdown table.
type TableRow struct {
	Cells map[string]string
}

var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
var separatorRe = regexp.MustCompile(`^\|[-\s:]+\|[-\s:|]+$`)

type flatSection struct {
	level int
	sec   Section
}

// Parse reads markdown from r and returns a structured Document.
func Parse(r io.Reader) (*Document, error) {
	scanner := bufio.NewScanner(r)
	doc := &Document{}

	var flat []flatSection
	var currentIdx int = -1   // index into flat for current section
	var tableHeaders []string // non-nil when inside a table
	var tableRows []TableRow
	var sawSeparator bool

	flushTable := func() {
		if currentIdx >= 0 && tableHeaders != nil && sawSeparator {
			flat[currentIdx].sec.Tables = append(flat[currentIdx].sec.Tables, Table{
				Headers: tableHeaders,
				Rows:    tableRows,
			})
		}
		tableHeaders = nil
		tableRows = nil
		sawSeparator = false
	}

	for scanner.Scan() {
		line := scanner.Text()

		// Check for heading.
		if m := headingRe.FindStringSubmatch(line); m != nil {
			flushTable()
			level := len(m[1])
			title := strings.TrimSpace(m[2])
			if doc.Title == "" && level == 1 {
				doc.Title = title
			}
			flat = append(flat, flatSection{level: level, sec: Section{Level: level, Title: title}})
			currentIdx = len(flat) - 1
			continue
		}

		// Check for table lines.
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			if separatorRe.MatchString(line) {
				sawSeparator = true
				continue
			}
			cells := parseTableRow(line)
			if tableHeaders == nil {
				tableHeaders = make([]string, len(cells))
				for i, c := range cells {
					tableHeaders[i] = normalizeField(c)
				}
			} else if sawSeparator {
				row := TableRow{Cells: make(map[string]string, len(tableHeaders))}
				for i, h := range tableHeaders {
					val := ""
					if i < len(cells) {
						val = normalizeField(cells[i])
					}
					row.Cells[h] = val
				}
				tableRows = append(tableRows, row)
			}
			continue
		}

		// Non-table, non-heading line: flush any pending table and append content.
		if tableHeaders != nil {
			flushTable()
		}

		if currentIdx >= 0 {
			trimmed := strings.TrimSpace(line)
			if trimmed == "---" {
				continue
			}
			if flat[currentIdx].sec.Content == "" {
				if trimmed != "" {
					flat[currentIdx].sec.Content = line
				}
			} else {
				flat[currentIdx].sec.Content += "\n" + line
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flushTable()

	// Trim trailing whitespace from content.
	for i := range flat {
		flat[i].sec.Content = strings.TrimRight(flat[i].sec.Content, " \t\n")
	}

	doc.Sections = buildHierarchy(flat)
	return doc, nil
}

// buildHierarchy nests flat sections into a tree based on heading level.
func buildHierarchy(flat []flatSection) []Section {
	if len(flat) == 0 {
		return nil
	}

	var roots []Section
	type stackEntry struct {
		level int
		sec   *Section
	}
	var stack []stackEntry

	for _, f := range flat {
		s := f.sec
		// Pop stack until we find a parent with a lower level.
		for len(stack) > 0 && stack[len(stack)-1].level >= f.level {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, s)
			stack = append(stack, stackEntry{level: f.level, sec: &roots[len(roots)-1]})
		} else {
			parent := stack[len(stack)-1].sec
			parent.Children = append(parent.Children, s)
			stack = append(stack, stackEntry{level: f.level, sec: &parent.Children[len(parent.Children)-1]})
		}
	}
	return roots
}

// parseTableRow splits a pipe-delimited row into cells.
func parseTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = strings.TrimSpace(p)
	}
	return cells
}

// normalizeField strips markdown formatting: **, __, *, _, and backticks.
func normalizeField(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "`", "")
	if len(s) >= 2 && s[0] == '*' && s[len(s)-1] == '*' {
		s = s[1 : len(s)-1]
	}
	if len(s) >= 2 && s[0] == '_' && s[len(s)-1] == '_' {
		s = s[1 : len(s)-1]
	}
	return strings.TrimSpace(s)
}

// FindSection searches a document for a section matching the given title (case-insensitive).
func FindSection(doc *Document, title string) *Section {
	lower := strings.ToLower(title)
	var search func(sections []Section) *Section
	search = func(sections []Section) *Section {
		for i := range sections {
			if strings.ToLower(sections[i].Title) == lower {
				return &sections[i]
			}
			if found := search(sections[i].Children); found != nil {
				return found
			}
		}
		return nil
	}
	return search(doc.Sections)
}
