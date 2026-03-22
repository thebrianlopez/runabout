package mdq

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatText(t *testing.T) {
	results := []QueryResult{
		{File: "a.md", Field: "Status", Value: "Active"},
		{File: "b.md", Field: "Status", Value: "Done"},
	}
	got := Format(results, "text")
	if !strings.Contains(got, "a.md: Status = Active") {
		t.Errorf("missing a.md line in %q", got)
	}
	if !strings.Contains(got, "b.md: Status = Done") {
		t.Errorf("missing b.md line in %q", got)
	}
}

func TestFormatJSON(t *testing.T) {
	results := []QueryResult{
		{File: "a.md", Field: "Status", Value: "Active"},
	}
	got := Format(results, "json")
	var parsed []map[string]string
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, got)
	}
	if len(parsed) != 1 {
		t.Fatalf("got %d items", len(parsed))
	}
	if parsed[0]["Value"] != "Active" {
		t.Errorf("Value = %q", parsed[0]["Value"])
	}
}

func TestFormatTableOutput(t *testing.T) {
	results := []QueryResult{
		{File: "a.md", Field: "Status", Value: "Active"},
		{File: "long-file.md", Field: "Status", Value: "Done"},
	}
	got := Format(results, "table")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4 (header + sep + 2 data)", len(lines))
	}
	if !strings.Contains(lines[0], "FILE") {
		t.Errorf("missing FILE header in %q", lines[0])
	}
	if !strings.Contains(lines[1], "---") {
		t.Errorf("missing separator in %q", lines[1])
	}
}

func TestFormatEmpty(t *testing.T) {
	got := Format(nil, "text")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestFormatTableRenderer(t *testing.T) {
	tbl := Table{
		Headers: []string{"Name", "Age"},
		Rows: []TableRow{
			{Cells: map[string]string{"Name": "Alice", "Age": "30"}},
			{Cells: map[string]string{"Name": "Bob", "Age": "25"}},
		},
	}
	got := FormatTable(tbl)
	if !strings.Contains(got, "Alice") || !strings.Contains(got, "Bob") {
		t.Errorf("FormatTable missing data: %q", got)
	}
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 4 {
		t.Errorf("got %d lines, want 4", len(lines))
	}
}

func TestGroupByDir(t *testing.T) {
	results := []QueryResult{
		{File: "docs/epics/a.md", Field: "heading", Value: "Epic A"},
		{File: "docs/epics/b.md", Field: "heading", Value: "Epic B"},
		{File: "docs/ideas/c.md", Field: "heading", Value: "Idea C"},
	}
	groups := GroupByDir(results)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	if groups[0].Folder != "epics" {
		t.Errorf("groups[0].Folder = %q, want epics", groups[0].Folder)
	}
	if groups[0].Count != 2 {
		t.Errorf("groups[0].Count = %d, want 2", groups[0].Count)
	}
	if groups[1].Folder != "ideas" {
		t.Errorf("groups[1].Folder = %q, want ideas", groups[1].Folder)
	}
	if groups[1].Count != 1 {
		t.Errorf("groups[1].Count = %d, want 1", groups[1].Count)
	}
}

func TestFormatGroupedText(t *testing.T) {
	groups := []GroupEntry{
		{Folder: "epics", Count: 2, Titles: []string{"Epic A", "Epic B"}},
		{Folder: "ideas", Count: 1, Titles: []string{"Idea C"}},
	}
	got := FormatGrouped(groups, "text")
	if !strings.Contains(got, "epics/ (2 files)") {
		t.Errorf("missing epics header in %q", got)
	}
	if !strings.Contains(got, "  • Epic A") {
		t.Errorf("missing Epic A in %q", got)
	}
	if !strings.Contains(got, "ideas/ (1 files)") {
		t.Errorf("missing ideas header in %q", got)
	}
}

func TestFormatGroupedJSON(t *testing.T) {
	groups := []GroupEntry{
		{Folder: "epics", Count: 2, Titles: []string{"A", "B"}},
	}
	got := FormatGrouped(groups, "json")
	var parsed []GroupEntry
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, got)
	}
	if len(parsed) != 1 || parsed[0].Folder != "epics" || parsed[0].Count != 2 {
		t.Errorf("unexpected parsed result: %+v", parsed)
	}
}

func TestFormatGroupedTable(t *testing.T) {
	groups := []GroupEntry{
		{Folder: "epics", Count: 2, Titles: []string{"A", "B"}},
	}
	got := FormatGrouped(groups, "table")
	if !strings.Contains(got, "FOLDER") || !strings.Contains(got, "COUNT") {
		t.Errorf("missing table headers in %q", got)
	}
	if !strings.Contains(got, "epics") {
		t.Errorf("missing epics row in %q", got)
	}
}

func TestFormatGroupedEmpty(t *testing.T) {
	got := FormatGrouped(nil, "text")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestFormatSection(t *testing.T) {
	sec := &Section{
		Content: "Hello world.",
		Tables: []Table{
			{
				Headers: []string{"A"},
				Rows:    []TableRow{{Cells: map[string]string{"A": "1"}}},
			},
		},
	}
	got := FormatSection(sec)
	if !strings.Contains(got, "Hello world.") {
		t.Errorf("missing content in %q", got)
	}
	if !strings.Contains(got, "1") {
		t.Errorf("missing table data in %q", got)
	}
}
