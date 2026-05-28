package hookval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testSchemaYAML = `
version: "1.0"
signals:
  dirty:
    type: integer_or_literal
    pattern: "^[0-9]+$"
    literals: ["n/a"]
    description: "Count of changed files; n/a outside a git repo"
  branch:
    type: string_or_literal
    literals: ["n/a"]
    description: "Current branch name or commit hash; n/a outside a git repo"
  org:
    type: string
    description: "Grindr org slug from $ORG env"
  aws:
    type: string_or_literal
    literals: ["unset"]
    description: "Active AWS_PROFILE or unset"
  kube:
    type: string_or_literal
    literals: ["unset"]
    description: "Active kube context alias or unset"
  type:
    type: enum
    values: ["unknown", "fish-config", "go-service", "helm-chart", "jira-workspace", "python-service"]
    description: "Detected project type"
  date:
    type: iso8601_utc
    pattern: "^[0-9]{8}T[0-9]{6}Z$"
    description: "UTC timestamp at prompt submission"
  dir:
    type: path
    description: "Absolute path of current working directory"
`

func loadTestSchema(t *testing.T) *Schema {
	t.Helper()
	var s Schema
	if err := yaml.Unmarshal([]byte(testSchemaYAML), &s); err != nil {
		t.Fatalf("unmarshal test schema: %v", err)
	}
	return &s
}

// --- Schema loading ---

func TestLoadSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.yaml")
	if err := os.WriteFile(path, []byte(testSchemaYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSchema(path)
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	if s.Version != "1.0" {
		t.Errorf("version = %q, want %q", s.Version, "1.0")
	}
	if len(s.Signals) != 8 {
		t.Errorf("len(signals) = %d, want 8", len(s.Signals))
	}
}

func TestLoadSchema_Missing(t *testing.T) {
	_, err := LoadSchema("/nonexistent/schema.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// --- LintSchema ---

func TestLintSchema_Valid(t *testing.T) {
	s := loadTestSchema(t)
	errs := LintSchema(s)
	if len(errs) != 0 {
		t.Errorf("expected no lint errors, got: %v", errs)
	}
}

func TestLintSchema_MissingVersion(t *testing.T) {
	s := loadTestSchema(t)
	s.Version = ""
	errs := LintSchema(s)
	if len(errs) == 0 {
		t.Error("expected error for missing version")
	}
}

func TestLintSchema_EnumMissingValues(t *testing.T) {
	s := loadTestSchema(t)
	s.Signals["type"] = SignalDef{Type: "enum", Description: "test"}
	errs := LintSchema(s)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "values") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'values' error for enum, got: %v", errs)
	}
}

func TestLintSchema_PatternRequired(t *testing.T) {
	s := loadTestSchema(t)
	s.Signals["dirty"] = SignalDef{Type: "integer_or_literal", Description: "test"}
	errs := LintSchema(s)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "pattern") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'pattern' error for integer_or_literal, got: %v", errs)
	}
}

// --- ParseContext ---

func TestParseContext(t *testing.T) {
	json := `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"dirty=0 branch=main org=grindr aws=unset kube=unset type=go-service date=20260319T182245Z dir=/home/user"}}`
	signals, err := ParseContext([]byte(json))
	if err != nil {
		t.Fatalf("ParseContext: %v", err)
	}
	cases := map[string]string{
		"dirty":  "0",
		"branch": "main",
		"org":    "grindr",
		"aws":    "unset",
		"kube":   "unset",
		"type":   "go-service",
		"date":   "20260319T182245Z",
		"dir":    "/home/user",
	}
	for k, want := range cases {
		if got := signals[k]; got != want {
			t.Errorf("signal %q = %q, want %q", k, got, want)
		}
	}
}

func TestParseContext_InvalidJSON(t *testing.T) {
	_, err := ParseContext([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// --- ValidateSignals ---

func TestValidateSignals_AllPass(t *testing.T) {
	s := loadTestSchema(t)
	signals := map[string]string{
		"dirty":  "0",
		"branch": "main",
		"org":    "grindr",
		"aws":    "unset",
		"kube":   "unset",
		"type":   "go-service",
		"date":   "20260319T182245Z",
		"dir":    "/Users/brian/code",
	}
	results := ValidateSignals(s, signals)
	for _, r := range results {
		if !r.Pass {
			t.Errorf("signal %q failed: %s (value=%q)", r.Name, r.Rule, r.Value)
		}
	}
}

func TestValidateSignals_NaValues(t *testing.T) {
	s := loadTestSchema(t)
	signals := map[string]string{
		"dirty":  "n/a",
		"branch": "n/a",
		"org":    "grindr",
		"aws":    "unset",
		"kube":   "unset",
		"type":   "unknown",
		"date":   "20260319T182245Z",
		"dir":    "/tmp",
	}
	results := ValidateSignals(s, signals)
	for _, r := range results {
		if !r.Pass {
			t.Errorf("signal %q failed: %s (value=%q)", r.Name, r.Rule, r.Value)
		}
	}
}

// --- checkSignal type coverage ---

func TestCheckSignal_IntegerOrLiteral(t *testing.T) {
	def := SignalDef{Type: "integer_or_literal", Pattern: "^[0-9]+$", Literals: []string{"n/a"}}
	cases := []struct {
		val  string
		want bool
	}{
		{"0", true}, {"42", true}, {"n/a", true}, {"abc", false}, {"", false},
	}
	for _, c := range cases {
		got, _ := checkSignal(def, c.val)
		if got != c.want {
			t.Errorf("checkSignal(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}

func TestCheckSignal_Enum(t *testing.T) {
	def := SignalDef{Type: "enum", Values: []string{"a", "b", "c"}}
	cases := []struct {
		val  string
		want bool
	}{
		{"a", true}, {"b", true}, {"d", false}, {"", false},
	}
	for _, c := range cases {
		got, _ := checkSignal(def, c.val)
		if got != c.want {
			t.Errorf("checkSignal(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}

func TestCheckSignal_Iso8601UTC(t *testing.T) {
	def := SignalDef{Type: "iso8601_utc", Pattern: "^[0-9]{8}T[0-9]{6}Z$"}
	cases := []struct {
		val  string
		want bool
	}{
		{"20260319T182245Z", true}, {"2026-03-19", false}, {"", false},
	}
	for _, c := range cases {
		got, _ := checkSignal(def, c.val)
		if got != c.want {
			t.Errorf("checkSignal(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}

func TestCheckSignal_Path(t *testing.T) {
	def := SignalDef{Type: "path"}
	cases := []struct {
		val  string
		want bool
	}{
		{"/home/user", true}, {"/tmp", true}, {"relative", false}, {"", false},
	}
	for _, c := range cases {
		got, _ := checkSignal(def, c.val)
		if got != c.want {
			t.Errorf("checkSignal(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}

// --- GenDocsTable ---

func TestGenDocsTable(t *testing.T) {
	s := loadTestSchema(t)
	out := GenDocsTable(s)
	if !strings.Contains(out, "| Signal |") {
		t.Error("missing table header")
	}
	for name := range s.Signals {
		if !strings.Contains(out, "`"+name+"`") {
			t.Errorf("missing signal %q in table", name)
		}
	}
	// Verify sorted order — aws comes before branch
	awsIdx := strings.Index(out, "`aws`")
	branchIdx := strings.Index(out, "`branch`")
	if awsIdx > branchIdx {
		t.Error("table is not sorted: 'aws' should appear before 'branch'")
	}
}
