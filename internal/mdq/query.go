package mdq

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Query defines what to extract from markdown documents.
type Query struct {
	Field   string
	Table   string
	Heading string
	Level   int
}

// QueryResult holds a single match from a query.
type QueryResult struct {
	File    string
	Section string
	Field   string
	Value   string
}

// Execute runs a query across files matching the glob pattern.
func Execute(pattern string, q Query) ([]QueryResult, error) {
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid glob pattern: %w", err)
	}
	if len(files) == 0 {
		return nil, nil
	}

	var results []QueryResult
	for _, path := range files {
		r, err := queryFile(path, q)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		results = append(results, r...)
	}
	return results, nil
}

func queryFile(path string, q Query) ([]QueryResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	doc, err := Parse(f)
	if err != nil {
		return nil, err
	}

	var results []QueryResult

	if q.Heading != "" {
		// List headings mode.
		results = collectHeadings(path, doc.Sections, q)
	} else if q.Field != "" {
		// Field query mode — search tables for a column matching field name.
		results = collectFields(path, doc.Sections, q, "")
	}

	return results, nil
}

// collectHeadings walks all sections and returns their titles.
func collectHeadings(file string, sections []Section, q Query) []QueryResult {
	var results []QueryResult
	for _, s := range sections {
		if q.Level > 0 && s.Level != q.Level {
			// Skip level-filtered sections, but still recurse children.
			results = append(results, collectHeadings(file, s.Children, q)...)
			continue
		}
		results = append(results, QueryResult{
			File:    file,
			Section: s.Title,
			Field:   "heading",
			Value:   s.Title,
		})
		results = append(results, collectHeadings(file, s.Children, q)...)
	}
	return results
}

// collectFields walks sections and extracts field values from tables.
func collectFields(file string, sections []Section, q Query, parentTitle string) []QueryResult {
	var results []QueryResult
	fieldLower := strings.ToLower(q.Field)
	tableLower := strings.ToLower(q.Table)

	for _, s := range sections {
		sectionTitle := s.Title
		if parentTitle != "" {
			sectionTitle = parentTitle + " > " + s.Title
		}

		// If table filter is set, only search matching sections.
		if q.Table != "" && !strings.EqualFold(s.Title, q.Table) {
			results = append(results, collectFields(file, s.Children, q, sectionTitle)...)
			continue
		}
		_ = tableLower

		for _, t := range s.Tables {
			// Check if this table has a column matching the field.
			colIdx := -1
			for i, h := range t.Headers {
				if strings.ToLower(h) == fieldLower {
					colIdx = i
					break
				}
			}
			if colIdx < 0 {
				// Also check for Field/Value pattern (2-column metadata tables).
				fieldCol, valueCol := -1, -1
				for i, h := range t.Headers {
					hl := strings.ToLower(h)
					if hl == "field" {
						fieldCol = i
					}
					if hl == "value" {
						valueCol = i
					}
				}
				if fieldCol >= 0 && valueCol >= 0 {
					for _, row := range t.Rows {
						rowField := row.Cells[t.Headers[fieldCol]]
						if strings.EqualFold(rowField, q.Field) {
							results = append(results, QueryResult{
								File:    file,
								Section: s.Title,
								Field:   q.Field,
								Value:   row.Cells[t.Headers[valueCol]],
							})
						}
					}
				}
				continue
			}

			// Standard column extraction.
			header := t.Headers[colIdx]
			for _, row := range t.Rows {
				val := row.Cells[header]
				if val != "" {
					results = append(results, QueryResult{
						File:    file,
						Section: s.Title,
						Field:   q.Field,
						Value:   val,
					})
				}
			}
		}

		results = append(results, collectFields(file, s.Children, q, sectionTitle)...)
	}
	return results
}
