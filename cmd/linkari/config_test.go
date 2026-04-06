package main

import (
	"os"
	"path/filepath"
	"testing"
)

const testConfigYAML = `
default_archive_threshold: 80
actions:
  - id: uinit_eng
    label: "Linkari (Eng)"
    icon: eng
    type: url
    target: "linkari:0"
    kind: template
    command_template: 'uinit --auto-resume {{if and .Profile (ne .Profile "eng")}}--profile {{.Profile}} {{end}}{{.URL}}'
    profile_map: prefix
    archive_threshold: 80
  - id: uinit_life
    label: "Linkari (Life)"
    icon: life
    type: url
    target: "linkari:0"
    kind: template
    command_template: 'uinit --auto-resume --profile {{.Profile}} {{.URL}}'
    profile_map: prefix
    archive_threshold: -1
  - id: ginit
    label: ginit
    icon: work
    type: text
    target: "JIRA:0"
    kind: regex
    pattern: '[A-Z][A-Z0-9]+-[0-9]+'
    command_template: 'ginit {{.Match}} --yolo'
    condition: "env:TEST_GINIT_ENABLED=1"
  - id: clipboard
    label: Clipboard
    icon: paste
    type: text
    target: "local:0"
    kind: literal
`

func writeTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "actions.yaml")
	if err := os.WriteFile(path, []byte(testConfigYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfig(t *testing.T) {
	path := writeTestConfig(t)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Actions) != 4 {
		t.Fatalf("expected 4 actions, got %d", len(cfg.Actions))
	}
	if cfg.DefaultArchiveThreshold != 80 {
		t.Errorf("default_archive_threshold = %d, want 80", cfg.DefaultArchiveThreshold)
	}
}

func TestLoadConfigValidation(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"missing id", `actions: [{kind: template, command_template: "echo"}]`, "id is required"},
		{"duplicate id", `actions: [{id: a, kind: template, command_template: "x"}, {id: a, kind: template, command_template: "y"}]`, "duplicate action id"},
		{"missing template", `actions: [{id: a, kind: template}]`, "command_template required"},
		{"missing pattern", `actions: [{id: a, kind: regex, command_template: "x"}]`, "pattern required"},
		{"bad regex", `actions: [{id: a, kind: regex, pattern: "[invalid", command_template: "x"}]`, "invalid pattern"},
		{"unknown kind", `actions: [{id: a, kind: foo}]`, "unknown kind"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.name+".yaml")
			os.WriteFile(path, []byte(tt.yaml), 0o644)
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatal("expected error")
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestActiveActionsWithCondition(t *testing.T) {
	path := writeTestConfig(t)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	// ginit has condition env:TEST_GINIT_ENABLED=1.
	active := cfg.ActiveActions()
	for _, a := range active {
		if a.ID == "ginit" {
			t.Error("ginit should not be active without env var set")
		}
	}

	// Set the env var and check again.
	t.Setenv("TEST_GINIT_ENABLED", "1")
	active = cfg.ActiveActions()
	found := false
	for _, a := range active {
		if a.ID == "ginit" {
			found = true
		}
	}
	if !found {
		t.Error("ginit should be active with env var set")
	}
}

func TestRenderCommand(t *testing.T) {
	path := writeTestConfig(t)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		actionID string
		data     TemplateData
		want     string
	}{
		{"uinit_eng", TemplateData{URL: "https://example.com", Profile: "eng"}, "uinit --auto-resume https://example.com"},
		{"uinit_eng", TemplateData{URL: "https://example.com", Profile: "travel"}, "uinit --auto-resume --profile travel https://example.com"},
		{"uinit_life", TemplateData{URL: "https://example.com", Profile: "life"}, "uinit --auto-resume --profile life https://example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.actionID+"_"+tt.data.Profile, func(t *testing.T) {
			var ac *ActionConfig
			for i := range cfg.Actions {
				if cfg.Actions[i].ID == tt.actionID {
					ac = &cfg.Actions[i]
					break
				}
			}
			if ac == nil {
				t.Fatalf("action %q not found", tt.actionID)
			}
			got, err := ac.RenderCommand(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRegexExtractMatch(t *testing.T) {
	path := writeTestConfig(t)
	t.Setenv("TEST_GINIT_ENABLED", "1")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	var ginit *ActionConfig
	for i := range cfg.Actions {
		if cfg.Actions[i].ID == "ginit" {
			ginit = &cfg.Actions[i]
			break
		}
	}
	if ginit == nil {
		t.Fatal("ginit action not found")
	}

	tests := []struct {
		input string
		want  string
	}{
		{"ISRE-1234", "ISRE-1234"},
		{"check out PROJ-42 please", "PROJ-42"},
		{"no key here", ""},
	}
	for _, tt := range tests {
		got := ginit.ExtractMatch(tt.input)
		if got != tt.want {
			t.Errorf("ExtractMatch(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuiltinConfig(t *testing.T) {
	cfg := builtinConfig()
	if len(cfg.Actions) < 7 {
		t.Errorf("expected at least 7 builtin actions, got %d", len(cfg.Actions))
	}

	// Verify all templates compiled.
	for _, a := range cfg.Actions {
		if a.Kind == KindTemplate && a.compiledTemplate == nil {
			t.Errorf("action %q: template not compiled", a.ID)
		}
		if a.Kind == KindRegex && a.compiledRegex == nil {
			t.Errorf("action %q: regex not compiled", a.ID)
		}
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
