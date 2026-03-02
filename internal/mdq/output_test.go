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
