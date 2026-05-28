package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandEnvVars(t *testing.T) {
	t.Setenv("WORKCTL_TEST_A", "hello")
	t.Setenv("WORKCTL_TEST_B", "world")

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"single var", "token: ${WORKCTL_TEST_A}", "token: hello"},
		{"two vars", "${WORKCTL_TEST_A}_${WORKCTL_TEST_B}", "hello_world"},
		{"unset var", "val: ${WORKCTL_UNSET_VAR}", "val: "},
		{"no braces", "val: $NOTBRACES", "val: $NOTBRACES"},
		{"no vars", "plain text", "plain text"},
		{"empty input", "", ""},
		{"mixed", "pre ${WORKCTL_TEST_A} mid $BARE ${WORKCTL_TEST_B} post", "pre hello mid $BARE world post"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExpandEnvVars(tt.input)
			if got != tt.want {
				t.Errorf("ExpandEnvVars(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLoadConfigFile(t *testing.T) {
	t.Run("valid full config", func(t *testing.T) {
		yaml := `
defaults:
  timezone: America/Chicago
  summary: true
  output_dir: output
  jira: true
  confluence: true
  github: true
  github_api: auto

atlassian:
  domain: example.atlassian.net
  email: user@example.com

github:
  user: octocat

profiles:
  annual-review:
    project_keys: SR,ISRE
    start: "2025-01-01"
    end: "2025-12-31"
    summary: true
    description: "Annual performance review"
  quick-status:
    since: 7d
    summary: true
    jira: true
    confluence: false
`
		path := writeTemp(t, "config.yaml", yaml)
		cfg, err := LoadConfigFile(path)
		if err != nil {
			t.Fatalf("LoadConfigFile: %v", err)
		}

		if cfg.Defaults.TimeZone != "America/Chicago" {
			t.Errorf("timezone = %q, want America/Chicago", cfg.Defaults.TimeZone)
		}
		if cfg.Atlassian.Domain != "example.atlassian.net" {
			t.Errorf("domain = %q, want example.atlassian.net", cfg.Atlassian.Domain)
		}
		if cfg.GitHub.User != "octocat" {
			t.Errorf("github user = %q, want octocat", cfg.GitHub.User)
		}
		if len(cfg.Profiles) != 2 {
			t.Errorf("profiles count = %d, want 2", len(cfg.Profiles))
		}

		ar := cfg.Profiles["annual-review"]
		if ar.ProjectKeys == nil || *ar.ProjectKeys != "SR,ISRE" {
			t.Errorf("annual-review project_keys unexpected")
		}
		if ar.Summary == nil || !*ar.Summary {
			t.Errorf("annual-review summary should be true")
		}

		qs := cfg.Profiles["quick-status"]
		if qs.Since == nil || *qs.Since != "7d" {
			t.Errorf("quick-status since should be 7d")
		}
		if qs.Confluence == nil || *qs.Confluence {
			t.Errorf("quick-status confluence should be false")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		path := writeTemp(t, "empty.yaml", "")
		cfg, err := LoadConfigFile(path)
		if err != nil {
			t.Fatalf("LoadConfigFile: %v", err)
		}
		if cfg.Profiles != nil {
			t.Errorf("profiles should be nil for empty config")
		}
	})

	t.Run("invalid yaml", func(t *testing.T) {
		path := writeTemp(t, "bad.yaml", ":::invalid:::\n  - ][")
		_, err := LoadConfigFile(path)
		if err == nil {
			t.Fatal("expected error for invalid YAML")
		}
	})

	t.Run("env expansion in values", func(t *testing.T) {
		t.Setenv("WORKCTL_TEST_DOMAIN", "test.atlassian.net")
		yaml := `
atlassian:
  domain: ${WORKCTL_TEST_DOMAIN}
  email: user@example.com
`
		path := writeTemp(t, "envconfig.yaml", yaml)
		cfg, err := LoadConfigFile(path)
		if err != nil {
			t.Fatalf("LoadConfigFile: %v", err)
		}
		if cfg.Atlassian.Domain != "test.atlassian.net" {
			t.Errorf("domain = %q, want test.atlassian.net", cfg.Atlassian.Domain)
		}
	})

	t.Run("career lens config", func(t *testing.T) {
		yaml := `
career_lens:
  tracks:
    custom_track:
      description: "Custom career track"
      weights:
        cross_team_impact: 0.5
        change_velocity: 0.5
  ceilings:
    change_velocity: 30.0
    multi_project_span: 10.0
`
		path := writeTemp(t, "career.yaml", yaml)
		cfg, err := LoadConfigFile(path)
		if err != nil {
			t.Fatalf("LoadConfigFile: %v", err)
		}
		if cfg.CareerLens == nil {
			t.Fatal("career_lens should not be nil")
		}
		if len(cfg.CareerLens.Tracks) != 1 {
			t.Errorf("tracks count = %d, want 1", len(cfg.CareerLens.Tracks))
		}
		track := cfg.CareerLens.Tracks["custom_track"]
		if track.Description != "Custom career track" {
			t.Errorf("description = %q, want 'Custom career track'", track.Description)
		}
		if len(track.Weights) != 2 {
			t.Errorf("weights count = %d, want 2", len(track.Weights))
		}
		if cfg.CareerLens.Ceilings["change_velocity"] != 30.0 {
			t.Errorf("change_velocity ceiling = %f, want 30.0", cfg.CareerLens.Ceilings["change_velocity"])
		}
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := LoadConfigFile("/nonexistent/path.yaml")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("workspace config", func(t *testing.T) {
		yaml := `
workspace:
  org_path: ~/code/myorg
  workspace_dir: jira_work
  git_cache_dir: .git_cache
  jira:
    default_repos:
      - infra-terraform
      - infra-helm
      - my-service
    branch_pattern: "feature/{key}"
`
		path := writeTemp(t, "workspace.yaml", yaml)
		cfg, err := LoadConfigFile(path)
		if err != nil {
			t.Fatalf("LoadConfigFile: %v", err)
		}

		if cfg.Workspace == nil {
			t.Fatal("workspace config should not be nil")
		}
		if cfg.Workspace.OrgPath != "~/code/myorg" {
			t.Errorf("org_path = %q, want ~/code/myorg", cfg.Workspace.OrgPath)
		}
		if cfg.Workspace.WorkspaceDir != "jira_work" {
			t.Errorf("workspace_dir = %q, want jira_work", cfg.Workspace.WorkspaceDir)
		}
		if cfg.Workspace.GitCacheDir != ".git_cache" {
			t.Errorf("git_cache_dir = %q, want .git_cache", cfg.Workspace.GitCacheDir)
		}
		if cfg.Workspace.Jira == nil {
			t.Fatal("workspace.jira should not be nil")
		}
		if len(cfg.Workspace.Jira.DefaultRepos) != 3 {
			t.Errorf("default_repos count = %d, want 3", len(cfg.Workspace.Jira.DefaultRepos))
		}
		expectedRepos := []string{"infra-terraform", "infra-helm", "my-service"}
		for i, want := range expectedRepos {
			if i < len(cfg.Workspace.Jira.DefaultRepos) && cfg.Workspace.Jira.DefaultRepos[i] != want {
				t.Errorf("default_repos[%d] = %q, want %q", i, cfg.Workspace.Jira.DefaultRepos[i], want)
			}
		}
		if cfg.Workspace.Jira.BranchPattern != "feature/{key}" {
			t.Errorf("branch_pattern = %q, want feature/{key}", cfg.Workspace.Jira.BranchPattern)
		}
	})

	t.Run("workspace config partial", func(t *testing.T) {
		yaml := `
workspace:
  org_path: ~/code/org
`
		path := writeTemp(t, "workspace_partial.yaml", yaml)
		cfg, err := LoadConfigFile(path)
		if err != nil {
			t.Fatalf("LoadConfigFile: %v", err)
		}

		if cfg.Workspace == nil {
			t.Fatal("workspace config should not be nil")
		}
		if cfg.Workspace.OrgPath != "~/code/org" {
			t.Errorf("org_path = %q, want ~/code/org", cfg.Workspace.OrgPath)
		}
		// Other fields should be zero values
		if cfg.Workspace.WorkspaceDir != "" {
			t.Errorf("workspace_dir should be empty, got %q", cfg.Workspace.WorkspaceDir)
		}
		if cfg.Workspace.Jira != nil {
			t.Errorf("jira should be nil when not specified")
		}
	})

	t.Run("workspace config absent", func(t *testing.T) {
		yaml := `
defaults:
  timezone: UTC
`
		path := writeTemp(t, "no_workspace.yaml", yaml)
		cfg, err := LoadConfigFile(path)
		if err != nil {
			t.Fatalf("LoadConfigFile: %v", err)
		}

		if cfg.Workspace != nil {
			t.Errorf("workspace should be nil when not in config, got %+v", cfg.Workspace)
		}
	})
}

func TestDiscoverConfigFile(t *testing.T) {
	t.Run("explicit path found", func(t *testing.T) {
		path := writeTemp(t, "explicit.yaml", "defaults: {}")
		got, err := DiscoverConfigFile(path)
		if err != nil {
			t.Fatalf("DiscoverConfigFile: %v", err)
		}
		if got != path {
			t.Errorf("got %q, want %q", got, path)
		}
	})

	t.Run("explicit path not found", func(t *testing.T) {
		_, err := DiscoverConfigFile("/nonexistent.yaml")
		if err == nil {
			t.Fatal("expected error for missing explicit path")
		}
	})

	t.Run("local .workctl.yaml", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, ".workctl.yaml")
		if err := os.WriteFile(cfgPath, []byte("defaults: {}"), 0o644); err != nil {
			t.Fatal(err)
		}

		// Change to temp dir
		orig, _ := os.Getwd()
		t.Cleanup(func() { os.Chdir(orig) })
		os.Chdir(dir)

		got, err := DiscoverConfigFile("")
		if err != nil {
			t.Fatalf("DiscoverConfigFile: %v", err)
		}
		if got != ".workctl.yaml" {
			t.Errorf("got %q, want .workctl.yaml", got)
		}
	})

	t.Run("no config found", func(t *testing.T) {
		dir := t.TempDir()
		orig, _ := os.Getwd()
		t.Cleanup(func() { os.Chdir(orig) })
		os.Chdir(dir)

		// Isolate XDG_CONFIG_HOME so the user's real config isn't discovered
		t.Setenv("XDG_CONFIG_HOME", dir)

		got, err := DiscoverConfigFile("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})
}

func TestProfileConfigPointerSemantics(t *testing.T) {
	yaml := `
profiles:
  partial:
    project_keys: SR
    summary: true
  minimal:
    since: 7d
`
	path := writeTemp(t, "pointer.yaml", yaml)
	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}

	partial := cfg.Profiles["partial"]
	// Set fields
	if partial.ProjectKeys == nil {
		t.Error("project_keys should be set")
	}
	if partial.Summary == nil {
		t.Error("summary should be set")
	}
	// Unset fields should be nil
	if partial.Email != nil {
		t.Error("email should be nil (unset)")
	}
	if partial.Debug != nil {
		t.Error("debug should be nil (unset)")
	}
	if partial.StartDate != nil {
		t.Error("start should be nil (unset)")
	}
	if partial.Jira != nil {
		t.Error("jira should be nil (unset)")
	}

	minimal := cfg.Profiles["minimal"]
	if minimal.Since == nil {
		t.Error("since should be set")
	}
	if minimal.ProjectKeys != nil {
		t.Error("project_keys should be nil (unset)")
	}
}

func TestLoadConfigFileRaw(t *testing.T) {
	t.Setenv("WORKCTL_RAW_TEST", "expanded-value")
	yaml := `
atlassian:
  token: ${WORKCTL_RAW_TEST}
github:
  token: ghp_plaintext123
`
	path := writeTemp(t, "raw.yaml", yaml)

	raw, err := LoadConfigFileRaw(path)
	if err != nil {
		t.Fatalf("LoadConfigFileRaw: %v", err)
	}

	// Raw should preserve ${...} references, not expand them.
	if raw.Atlassian.Token != "${WORKCTL_RAW_TEST}" {
		t.Errorf("raw atlassian.token = %q, want ${WORKCTL_RAW_TEST}", raw.Atlassian.Token)
	}
	if raw.GitHub.Token != "ghp_plaintext123" {
		t.Errorf("raw github.token = %q, want ghp_plaintext123", raw.GitHub.Token)
	}
}

func TestCheckPlaintextTokens(t *testing.T) {
	tests := []struct {
		name         string
		atlToken     string
		ghToken      string
		wantWarnings int
	}{
		{"both env refs", "${ATLASSIAN_API_TOKEN}", "${GITHUB_TOKEN}", 0},
		{"both empty", "", "", 0},
		{"atlassian plaintext", "xyzabc123", "${GITHUB_TOKEN}", 1},
		{"github plaintext", "${ATLASSIAN_API_TOKEN}", "ghp_plaintext123", 1},
		{"both plaintext", "xyzabc123", "ghp_plaintext123", 2},
		{"env ref with suffix", "${ATLASSIAN_API_TOKEN}_extra", "${GITHUB_TOKEN}", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := &FileConfig{
				Atlassian: AtlassianConfig{Token: tt.atlToken},
				GitHub:    GitHubConfig{Token: tt.ghToken},
			}
			warnings := CheckPlaintextTokens(raw)
			if len(warnings) != tt.wantWarnings {
				t.Errorf("got %d warnings, want %d: %v", len(warnings), tt.wantWarnings, warnings)
			}
		})
	}

	t.Run("atlassian warning message", func(t *testing.T) {
		raw := &FileConfig{
			Atlassian: AtlassianConfig{Token: "plaintext-secret"},
		}
		warnings := CheckPlaintextTokens(raw)
		if len(warnings) != 1 {
			t.Fatalf("expected 1 warning, got %d", len(warnings))
		}
		if !strings.Contains(warnings[0], "atlassian.token") {
			t.Errorf("warning should mention atlassian.token: %s", warnings[0])
		}
		if !strings.Contains(warnings[0], "${ATLASSIAN_API_TOKEN}") {
			t.Errorf("warning should suggest env var: %s", warnings[0])
		}
	})

	t.Run("github warning message", func(t *testing.T) {
		raw := &FileConfig{
			GitHub: GitHubConfig{Token: "ghp_abc123"},
		}
		warnings := CheckPlaintextTokens(raw)
		if len(warnings) != 1 {
			t.Fatalf("expected 1 warning, got %d", len(warnings))
		}
		if !strings.Contains(warnings[0], "github.token") {
			t.Errorf("warning should mention github.token: %s", warnings[0])
		}
		if !strings.Contains(warnings[0], "${GITHUB_TOKEN}") {
			t.Errorf("warning should suggest env var: %s", warnings[0])
		}
	})
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
