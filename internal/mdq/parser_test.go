package mdq

import (
	"strings"
	"testing"
)

func TestParseTitle(t *testing.T) {
	input := "# My Document\n\n## Section One\n\nSome content.\n"
	doc, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "My Document" {
		t.Errorf("Title = %q, want %q", doc.Title, "My Document")
	}
}

func TestParseHeadings(t *testing.T) {
	input := "# Title\n## A\n### B\n## C\n"
	doc, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Sections) != 1 {
		t.Fatalf("got %d root sections, want 1", len(doc.Sections))
	}
	root := doc.Sections[0]
	if root.Title != "Title" {
		t.Errorf("root title = %q", root.Title)
	}
	if len(root.Children) != 2 {
		t.Fatalf("root has %d children, want 2", len(root.Children))
	}
	if root.Children[0].Title != "A" {
		t.Errorf("child[0] = %q, want A", root.Children[0].Title)
	}
	if len(root.Children[0].Children) != 1 {
		t.Fatalf("child A has %d children, want 1", len(root.Children[0].Children))
	}
	if root.Children[0].Children[0].Title != "B" {
		t.Errorf("grandchild = %q, want B", root.Children[0].Children[0].Title)
	}
	if root.Children[1].Title != "C" {
		t.Errorf("child[1] = %q, want C", root.Children[1].Title)
	}
}

func TestParseSectionContent(t *testing.T) {
	input := "## Intro\n\nHello world.\nSecond line.\n\n## Next\n"
	doc, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Sections) < 1 {
		t.Fatal("no sections")
	}
	want := "Hello world.\nSecond line."
	if doc.Sections[0].Content != want {
		t.Errorf("Content = %q, want %q", doc.Sections[0].Content, want)
	}
}

func TestParseTable(t *testing.T) {
	input := `## Status

| Field | Value |
|-------|-------|
| **ID** | 123 |
| **Status** | Active |
`
	doc, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Sections) != 1 {
		t.Fatalf("got %d sections", len(doc.Sections))
	}
	sec := doc.Sections[0]
	if len(sec.Tables) != 1 {
		t.Fatalf("got %d tables, want 1", len(sec.Tables))
	}
	tbl := sec.Tables[0]
	if len(tbl.Headers) != 2 {
		t.Fatalf("got %d headers, want 2", len(tbl.Headers))
	}
	if tbl.Headers[0] != "Field" || tbl.Headers[1] != "Value" {
		t.Errorf("headers = %v", tbl.Headers)
	}
	if len(tbl.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(tbl.Rows))
	}
	if tbl.Rows[0].Cells["Field"] != "ID" {
		t.Errorf("row[0].Field = %q, want ID", tbl.Rows[0].Cells["Field"])
	}
	if tbl.Rows[1].Cells["Value"] != "Active" {
		t.Errorf("row[1].Value = %q, want Active", tbl.Rows[1].Cells["Value"])
	}
}

func TestParseMultipleTables(t *testing.T) {
	input := `## Data

| A | B |
|---|---|
| 1 | 2 |

Some text between tables.

| X | Y |
|---|---|
| 3 | 4 |
`
	doc, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	sec := doc.Sections[0]
	if len(sec.Tables) != 2 {
		t.Fatalf("got %d tables, want 2", len(sec.Tables))
	}
	if sec.Tables[0].Rows[0].Cells["A"] != "1" {
		t.Errorf("table1 A = %q", sec.Tables[0].Rows[0].Cells["A"])
	}
	if sec.Tables[1].Rows[0].Cells["X"] != "3" {
		t.Errorf("table2 X = %q", sec.Tables[1].Rows[0].Cells["X"])
	}
}

func TestNormalizeField(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"**bold**", "bold"},
		{"`code`", "code"},
		{"__under__", "under"},
		{"*italic*", "italic"},
		{"_em_", "em"},
		{"plain", "plain"},
		{"  spaced  ", "spaced"},
	}
	for _, tc := range tests {
		got := normalizeField(tc.input)
		if got != tc.want {
			t.Errorf("normalizeField(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFindSection(t *testing.T) {
	input := "# Doc\n## Summary\n\nHello\n\n## Details\n\n### Sub\n\nWorld\n"
	doc, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}

	sec := FindSection(doc, "Summary")
	if sec == nil {
		t.Fatal("FindSection returned nil")
	}
	if sec.Content != "Hello" {
		t.Errorf("Content = %q, want Hello", sec.Content)
	}

	sub := FindSection(doc, "Sub")
	if sub == nil {
		t.Fatal("FindSection(Sub) returned nil")
	}
	if sub.Content != "World" {
		t.Errorf("Content = %q, want World", sub.Content)
	}

	if FindSection(doc, "Nonexistent") != nil {
		t.Error("FindSection should return nil for missing section")
	}
}

func TestFindSectionCaseInsensitive(t *testing.T) {
	input := "## Status and Metadata\n\nData here.\n"
	doc, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	sec := FindSection(doc, "status and metadata")
	if sec == nil {
		t.Fatal("case-insensitive find failed")
	}
}

func TestParseHorizontalRule(t *testing.T) {
	input := "## A\n\nContent above.\n\n---\n\n## B\n\nContent below.\n"
	doc, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Sections) != 2 {
		t.Fatalf("got %d sections, want 2", len(doc.Sections))
	}
	if strings.Contains(doc.Sections[0].Content, "---") {
		t.Error("horizontal rule should be stripped from content")
	}
}

func TestParseEmptyInput(t *testing.T) {
	doc, err := Parse(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "" {
		t.Errorf("Title = %q, want empty", doc.Title)
	}
	if len(doc.Sections) != 0 {
		t.Errorf("got %d sections, want 0", len(doc.Sections))
	}
}
