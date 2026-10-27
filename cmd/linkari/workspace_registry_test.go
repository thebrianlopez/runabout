package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testRegistryYAML = `schema_version: 1
workspaces:
  - name: mental-health
    scope: therapy work; family relationships
    tags: [therapy, family]
  - name: shopping
    scope: purchases, rewards optimization
    tags: [shopping, rewards, credit-cards]
  - name: social-calendar
    scope: events, plans, RSVPs
`

func writeTestRegistry(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workspaces.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadTestRegistry(t *testing.T) *WorkspaceRegistry {
	t.Helper()
	reg, err := LoadWorkspaceRegistry(writeTestRegistry(t, testRegistryYAML))
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestLoadWorkspaceRegistry(t *testing.T) {
	reg := loadTestRegistry(t)
	if len(reg.Workspaces) != 3 {
		t.Fatalf("expected 3 workspaces, got %d", len(reg.Workspaces))
	}
	if reg.Workspaces[0].Name != "mental-health" {
		t.Fatalf("unexpected first entry: %s", reg.Workspaces[0].Name)
	}
}

func TestLoadWorkspaceRegistryMissing(t *testing.T) {
	if _, err := LoadWorkspaceRegistry(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadWorkspaceRegistryEmptyEntriesDropped(t *testing.T) {
	mixed := "schema_version: 1\nworkspaces:\n  - name: \"\"\n    scope: x\n  - name: shopping\n    scope: rewards\n"
	reg, err := LoadWorkspaceRegistry(writeTestRegistry(t, mixed))
	if err != nil {
		t.Fatalf("expected empty-name entries to be dropped, got error: %v", err)
	}
	if len(reg.Workspaces) != 1 || reg.Workspaces[0].Name != "shopping" {
		t.Fatalf("expected only shopping to remain, got %+v", reg.Workspaces)
	}
}

func TestLoadWorkspaceRegistryAllEmptyIsError(t *testing.T) {
	if _, err := LoadWorkspaceRegistry(writeTestRegistry(t, "schema_version: 1\nworkspaces:\n  - name: \"\"\n")); err == nil {
		t.Fatal("expected error when no usable entries remain")
	}
}

func TestHasWorkspace(t *testing.T) {
	reg := loadTestRegistry(t)
	if !reg.HasWorkspace("shopping") {
		t.Fatal("shopping should be known")
	}
	if !reg.HasWorkspace("none") || !reg.HasWorkspace("") {
		t.Fatal("none and empty always validate")
	}
	if reg.HasWorkspace("hallucinated") {
		t.Fatal("unknown name must not validate")
	}
}

func TestMatchTags(t *testing.T) {
	reg := loadTestRegistry(t)

	if name, ok := reg.MatchTags([]string{"rewards"}); !ok || name != "shopping" {
		t.Fatalf("expected shopping, got %q ok=%v", name, ok)
	}
	if name, ok := reg.MatchTags([]string{"shopping"}); !ok || name != "shopping" {
		t.Fatalf("name-as-tag match failed: %q ok=%v", name, ok)
	}
	// Case-insensitive.
	if name, ok := reg.MatchTags([]string{"Therapy"}); !ok || name != "mental-health" {
		t.Fatalf("case-insensitive match failed: %q ok=%v", name, ok)
	}
	// No match.
	if _, ok := reg.MatchTags([]string{"kubernetes"}); ok {
		t.Fatal("expected no match")
	}
	// Ambiguous: two workspaces claim the same tag.
	ambiguous := strings.Replace(testRegistryYAML, "tags: [shopping, rewards, credit-cards]", "tags: [rewards]", 1)
	ambiguous = strings.Replace(ambiguous, "- name: social-calendar\n    scope: events, plans, RSVPs\n", "- name: social-calendar\n    scope: events, plans, RSVPs\n    tags: [rewards]\n", 1)
	reg2, err := LoadWorkspaceRegistry(writeTestRegistry(t, ambiguous))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg2.MatchTags([]string{"rewards"}); ok {
		t.Fatal("ambiguous match must return false")
	}
	// Nil registry.
	var nilReg *WorkspaceRegistry
	if _, ok := nilReg.MatchTags([]string{"shopping"}); ok {
		t.Fatal("nil registry must not match")
	}
}

func TestBuildWorkspaceContextBlockDisabled(t *testing.T) {
	if got := buildWorkspaceContextBlock(nil, nil, 400); got != "" {
		t.Fatalf("nil registry must produce empty block, got %q", got)
	}
}

func TestBuildWorkspaceContextBlockTagStage(t *testing.T) {
	reg := loadTestRegistry(t)
	block := buildWorkspaceContextBlock(reg, []string{"rewards"}, 400)
	if !strings.Contains(block, `"shopping"`) {
		t.Fatalf("tag stage must name the resolved workspace, got %q", block)
	}
	if strings.Contains(block, "WORKSPACE_REGISTRY") {
		t.Fatal("single tag match must not inject the full registry")
	}
}

func TestBuildWorkspaceContextBlockInferenceStage(t *testing.T) {
	reg := loadTestRegistry(t)
	block := buildWorkspaceContextBlock(reg, nil, 400)
	for _, want := range []string{"WORKSPACE_REGISTRY", "mental-health", "shopping", "social-calendar", "workspace_confidence"} {
		if !strings.Contains(block, want) {
			t.Fatalf("inference block missing %q", want)
		}
	}
}

func TestBuildWorkspaceContextBlockTokenBudget(t *testing.T) {
	// Tiny budget: some entries must be dropped but the block stays well-formed.
	reg := loadTestRegistry(t)
	block := buildWorkspaceContextBlock(reg, nil, 10)
	if !strings.Contains(block, "WORKSPACE_REGISTRY") {
		t.Fatal("budgeted block lost its envelope")
	}
	if strings.Contains(block, "social-calendar") {
		t.Fatal("token budget did not trim entries")
	}
}

func TestNormalizeWorkspaceAssignment(t *testing.T) {
	reg := loadTestRegistry(t)
	cases := []struct {
		in, want string
	}{
		{"shopping", "shopping"},
		{"  SHOPPING ", "shopping"},
		{"none", ""},
		{"", ""},
		{"hallucinated", ""},
	}
	for _, tc := range cases {
		if got := normalizeWorkspaceAssignment(reg, tc.in); got != tc.want {
			t.Errorf("normalizeWorkspaceAssignment(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := normalizeWorkspaceAssignment(nil, "shopping"); got != "" {
		t.Errorf("nil registry must collapse assignments, got %q", got)
	}
}

func TestClampWorkspaceConfidence(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{0.85, 0.85},
		{1.0, 1.0},
		{0, 0},
		{-0.5, 0},
		{1.5, 0},
	}
	for _, tc := range cases {
		if got := clampWorkspaceConfidence(tc.in); got != tc.want {
			t.Errorf("clampWorkspaceConfidence(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestWorkspaceConfigValidateDisabled(t *testing.T) {
	var cfg WorkspaceConfig
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled config must validate: %v", err)
	}
}

func TestWorkspaceConfigValidateEnabled(t *testing.T) {
	path := writeTestRegistry(t, testRegistryYAML)
	cfg := WorkspaceConfig{Enabled: true, Path: path, Profiles: []string{"life", "eng"}, MaxContextTokens: 400}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cfg.Profiles = nil
	if err := cfg.Validate(); err == nil {
		t.Fatal("empty profiles must fail validation")
	}

	cfg = WorkspaceConfig{Enabled: true, Path: path, Profiles: []string{"life"}, MaxContextTokens: 2001}
	if err := cfg.Validate(); err == nil {
		t.Fatal("max_context_tokens over 2000 must fail validation")
	}

	cfg = WorkspaceConfig{Enabled: true, Profiles: []string{"life"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("missing registry file must fail validation when enabled")
	}
}
