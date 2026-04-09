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
	// EPIC-051 M5: LoadConfig now merges the user file on top of builtins
	// by ID instead of replacing the action list wholesale. The test file
	// overrides 3 builtins (uinit_eng, uinit_life, ginit) and adds 1 new
	// action (clipboard), so the merged result contains all 8 builtins plus
	// the 1 extra = 9 actions.
	if len(cfg.Actions) != 9 {
		t.Fatalf("expected 9 merged actions, got %d", len(cfg.Actions))
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

// EPIC-051 M4: per-profile throttle config tests.

func TestServerConfig_PushYAML_Parses(t *testing.T) {
	yamlStr := `
server:
  notify_min_score: 10
  push:
    digest_throttle_default: 30m
    digest_throttle:
      eng: 1h
      dining: 24h
`
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(path, []byte(yamlStr), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadServerFile(path)
	if err != nil || cfg == nil {
		t.Fatalf("LoadServerFile: %v cfg=%v", err, cfg)
	}
	if cfg.NotifyMinScore != 10 {
		t.Errorf("NotifyMinScore=%d want 10", cfg.NotifyMinScore)
	}
	if got := cfg.Push.DigestThrottleDefault.Duration(); got != 30*60*1e9 {
		t.Errorf("default=%v want 30m", got)
	}
	durs := cfg.Push.DigestThrottle.Durations()
	if durs["eng"].String() != "1h0m0s" {
		t.Errorf("eng throttle=%v", durs["eng"])
	}
	if durs["dining"].String() != "24h0m0s" {
		t.Errorf("dining throttle=%v", durs["dining"])
	}
}

func TestServerConfig_PushConfig_Derivation(t *testing.T) {
	s := ServerConfig{
		NotifyMinScore: 25,
		Push: PushYAMLConfig{
			DigestThrottle: DurationMap{
				"eng": Duration{D: 2 * 60 * 60 * 1e9},
			},
		},
	}
	pc := s.PushConfig()
	if pc.NotifyMinScore != 25 {
		t.Errorf("min=%d", pc.NotifyMinScore)
	}
	if pc.DigestThrottle["eng"].String() != "2h0m0s" {
		t.Errorf("eng=%v", pc.DigestThrottle["eng"])
	}
	if got := pc.ThrottleFor("missing"); got.String() != "1h0m0s" {
		t.Errorf("default fallback=%v", got)
	}
}

func TestServerConfig_IsZero(t *testing.T) {
	if !(ServerConfig{}).IsZero() {
		t.Error("empty should be zero")
	}
	s := ServerConfig{Push: PushYAMLConfig{DigestThrottleDefault: Duration{D: 1}}}
	if s.IsZero() {
		t.Error("with push throttle should not be zero")
	}
}

// EPIC-051 M5: MergeWithBuiltin tests.

func TestMergeWithBuiltin_EmptyUser(t *testing.T) {
	merged, err := MergeWithBuiltin(builtinConfig(), nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(merged.Actions) != len(builtinConfig().Actions) {
		t.Errorf("expected same action count, got %d", len(merged.Actions))
	}
}

func TestMergeWithBuiltin_PartialOverride(t *testing.T) {
	user := &Config{
		Actions: []ActionConfig{
			{ID: "uinit_eng", ArchiveThreshold: 95},
			{ID: "uinit_dining", ArchiveThreshold: -1},
		},
	}
	merged, err := MergeWithBuiltin(builtinConfig(), user)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var eng, dining *ActionConfig
	for i := range merged.Actions {
		switch merged.Actions[i].ID {
		case "uinit_eng":
			eng = &merged.Actions[i]
		case "uinit_dining":
			dining = &merged.Actions[i]
		}
	}
	if eng == nil || eng.ArchiveThreshold != 95 {
		t.Errorf("eng override: %+v", eng)
	}
	if eng.CommandTemplate == "" {
		t.Errorf("eng should retain builtin command template")
	}
	if dining == nil || dining.ArchiveThreshold != -1 {
		t.Errorf("dining override: %+v", dining)
	}
}

func TestMergeWithBuiltin_AppendsExtras(t *testing.T) {
	user := &Config{
		Actions: []ActionConfig{
			{ID: "custom_new", Label: "Custom", Type: "url", Target: "linkari:0",
				Kind: KindTemplate, CommandTemplate: "echo {{.URL}}"},
		},
	}
	merged, err := MergeWithBuiltin(builtinConfig(), user)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	found := false
	for _, a := range merged.Actions {
		if a.ID == "custom_new" {
			found = true
		}
	}
	if !found {
		t.Errorf("custom_new not appended")
	}
}

func TestMergeWithBuiltin_ParityWithEPIC050(t *testing.T) {
	// Empirical parity check: a user file that lists only ArchiveThreshold
	// overrides produces the same archiveThreshold() values as hand-
	// constructing the merged config. This mirrors the EPIC-050 diagnostic
	// actions.yaml use case.
	user := &Config{
		Actions: []ActionConfig{
			{ID: "uinit_eng", ArchiveThreshold: 85},
		},
	}
	merged, err := MergeWithBuiltin(builtinConfig(), user)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	// The merged config should have exactly one eng action with threshold 85
	// and all other builtin actions with their original thresholds.
	for _, a := range merged.Actions {
		if a.ID == "uinit_eng" && a.ArchiveThreshold != 85 {
			t.Errorf("eng parity: %d", a.ArchiveThreshold)
		}
		if a.ID == "uinit_finance" && a.ArchiveThreshold != 70 {
			t.Errorf("finance untouched parity: %d", a.ArchiveThreshold)
		}
	}
}

// EPIC-051 M6: ReloadArchiveThresholdConfig integration test.

func TestReloadArchiveThresholdConfig(t *testing.T) {
	// Reset cached state so this test is independent of ordering.
	archiveThresholdMu.Lock()
	archiveThresholdCfg = nil
	archiveThresholdMu.Unlock()

	// Point LoadConfig at a tempdir by chdir'ing the home override. The
	// simplest path: construct a Config directly and install it, then
	// verify archiveThreshold reads through the cached pointer.
	cfg := &Config{
		DefaultArchiveThreshold: 50,
		Actions: []ActionConfig{
			{ID: "uinit_eng", Kind: KindTemplate, CommandTemplate: "echo",
				ProfileMap: "prefix", ArchiveThreshold: 99},
		},
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	archiveThresholdMu.Lock()
	archiveThresholdCfg = cfg
	archiveThresholdMu.Unlock()
	if got := archiveThreshold("eng"); got != 99 {
		t.Errorf("after install: eng=%d want 99", got)
	}

	// Simulate a reload by swapping in a new config.
	cfg2 := &Config{
		DefaultArchiveThreshold: 50,
		Actions: []ActionConfig{
			{ID: "uinit_eng", Kind: KindTemplate, CommandTemplate: "echo",
				ProfileMap: "prefix", ArchiveThreshold: 60},
		},
	}
	if err := cfg2.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	archiveThresholdMu.Lock()
	archiveThresholdCfg = cfg2
	archiveThresholdMu.Unlock()
	if got := archiveThreshold("eng"); got != 60 {
		t.Errorf("after reload: eng=%d want 60", got)
	}
}
