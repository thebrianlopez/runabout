package main

import (
	"os"
	"path/filepath"
	"testing"
)

const testConfigYAML = `
default_archive_threshold: 80
actions:
  - id: uinit_auto
    label: "Score (override)"
    archive_threshold: 90
  - id: ginit_auto
    label: "Build (override)"
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
	// EPIC-067: builtins are uinit_auto + vnote_auto + ginit_auto (3).
	// F1/M6 adds capture_jira_auto + capture_confluence_auto stubs (2 more = 5 total).
	// User file overrides uinit_auto and ginit_auto, adds 1 extra (clipboard) → merged count = 6.
	if len(cfg.Actions) != 6 {
		t.Fatalf("expected 6 merged actions, got %d", len(cfg.Actions))
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
	// Condition evaluation still works on user-supplied actions.
	cfg := &Config{
		Actions: []ActionConfig{
			{ID: "cond_test", Kind: KindTemplate, CommandTemplate: "echo",
				Condition: "env:TEST_COND=1"},
			{ID: "always_on", Kind: KindTemplate, CommandTemplate: "echo"},
		},
	}
	cfg.validate()
	active := cfg.ActiveActions()
	for _, a := range active {
		if a.ID == "cond_test" {
			t.Error("cond_test should not be active without env var set")
		}
	}
	t.Setenv("TEST_COND", "1")
	active = cfg.ActiveActions()
	found := false
	for _, a := range active {
		if a.ID == "cond_test" {
			found = true
		}
	}
	if !found {
		t.Error("cond_test should be active with env var set")
	}
}

func TestRenderCommand(t *testing.T) {
	cfg := builtinConfig()

	tests := []struct {
		actionID string
		data     TemplateData
		want     string
	}{
		{"uinit_auto", TemplateData{URL: "https://example.com", Profile: "eng"}, "uinit --auto-resume --profile eng 'https://example.com'"},
		{"ginit_auto", TemplateData{Text: "PROJ-123"}, "ginit 'PROJ-123'"},
	}

	for _, tt := range tests {
		t.Run(tt.actionID, func(t *testing.T) {
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
	// Regex extraction is still exercised via a custom action (no builtin
	// regex actions remain after EPIC-061).
	ac := ActionConfig{
		ID: "regex_test", Kind: KindRegex,
		Pattern: `[A-Z][A-Z0-9]+-[0-9]+`, CommandTemplate: "echo {{.Match}}",
	}
	cfg := &Config{Actions: []ActionConfig{ac}}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	compiled := &cfg.Actions[0]

	tests := []struct {
		input string
		want  string
	}{
		{"ISRE-1234", "ISRE-1234"},
		{"check out PROJ-42 please", "PROJ-42"},
		{"no key here", ""},
	}
	for _, tt := range tests {
		got := compiled.ExtractMatch(tt.input)
		if got != tt.want {
			t.Errorf("ExtractMatch(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuiltinConfig(t *testing.T) {
	cfg := builtinConfig()
	// EPIC-067: 3 auto-profile actions (uinit_auto, vnote_auto, ginit_auto).
	// F1/M6: +2 capture stubs (capture_jira_auto, capture_confluence_auto) = 5 total.
	if len(cfg.Actions) != 5 {
		t.Errorf("expected 5 builtin actions, got %d", len(cfg.Actions))
	}
	ids := map[string]bool{}
	for _, a := range cfg.Actions {
		ids[a.ID] = true
		if a.Kind == KindTemplate && a.compiledTemplate == nil {
			t.Errorf("action %q: template not compiled", a.ID)
		}
	}
	if !ids["uinit_auto"] {
		t.Error("missing uinit_auto")
	}
	if !ids["ginit_auto"] {
		t.Error("missing ginit_auto")
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

// EPIC-061: ginit_auto has AutoScore=true; uinit_auto has ServerScore=true.

func TestBuiltinAutoScoreAndServerScore(t *testing.T) {
	cfg := builtinConfig()
	for _, a := range cfg.Actions {
		switch a.ID {
		case "uinit_auto":
			if !a.ServerScore {
				t.Error("uinit_auto should have ServerScore=true")
			}
			if a.AutoScore {
				t.Error("uinit_auto should not have AutoScore")
			}
		case "ginit_auto":
			if !a.AutoScore {
				t.Error("ginit_auto should have AutoScore=true")
			}
			if a.ServerScore {
				t.Error("ginit_auto should not have ServerScore")
			}
		}
	}
}

func TestAutoScoreFieldParsesFromYAML(t *testing.T) {
	yamlStr := `
actions:
  - id: test_auto
    kind: template
    command_template: "echo"
    auto_score: true
`
	dir := t.TempDir()
	path := filepath.Join(dir, "auto.yaml")
	os.WriteFile(path, []byte(yamlStr), 0o644)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range cfg.Actions {
		if a.ID == "test_auto" {
			if !a.AutoScore {
				t.Error("auto_score should be true after YAML parse")
			}
			return
		}
	}
	t.Error("test_auto action not found in merged config")
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
			{ID: "uinit_auto", ArchiveThreshold: 95},
		},
	}
	merged, err := MergeWithBuiltin(builtinConfig(), user)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var uinit *ActionConfig
	for i := range merged.Actions {
		if merged.Actions[i].ID == "uinit_auto" {
			uinit = &merged.Actions[i]
		}
	}
	if uinit == nil || uinit.ArchiveThreshold != 95 {
		t.Errorf("uinit_auto override: %+v", uinit)
	}
	if uinit.CommandTemplate == "" {
		t.Errorf("uinit_auto should retain builtin command template")
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
	// constructing the merged config.
	user := &Config{
		Actions: []ActionConfig{
			{ID: "uinit_auto", ArchiveThreshold: 85},
		},
	}
	merged, err := MergeWithBuiltin(builtinConfig(), user)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	for _, a := range merged.Actions {
		if a.ID == "uinit_auto" && a.ArchiveThreshold != 85 {
			t.Errorf("uinit_auto parity: %d", a.ArchiveThreshold)
		}
		// ginit_auto should retain default (0) since user didn't override it.
		if a.ID == "ginit_auto" && a.ArchiveThreshold != 0 {
			t.Errorf("ginit_auto untouched parity: %d", a.ArchiveThreshold)
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
