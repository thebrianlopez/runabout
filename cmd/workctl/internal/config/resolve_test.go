package config

import (
	"strings"
	"testing"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		file    *FileConfig
		profile string
		flags   *FlagValues
		check   func(t *testing.T, rc *ResolvedConfig)
		wantErr bool
	}{
		{
			name: "defaults only (no config file)",
			file: nil,
			flags: &FlagValues{
				ProjectKeys: "SR", ProjectKeysSet: true,
				StartDate: "2025-01-01", StartDateSet: true,
				EndDate: "2025-06-30", EndDateSet: true,
			},
			check: func(t *testing.T, rc *ResolvedConfig) {
				if rc.TimeZone != "America/Chicago" {
					t.Errorf("timezone = %q, want America/Chicago", rc.TimeZone)
				}
				if !rc.Jira {
					t.Error("jira should default to true")
				}
				if !rc.Confluence {
					t.Error("confluence should default to true")
				}
				if rc.ProjectKeys[0] != "SR" {
					t.Errorf("project_keys = %v, want [SR]", rc.ProjectKeys)
				}
			},
		},
		{
			name: "file defaults override hardcoded",
			file: &FileConfig{
				Defaults: DefaultsConfig{
					TimeZone:  "UTC",
					Summary:   boolPtr(true),
					OutputDir: "results",
				},
			},
			flags: &FlagValues{
				ProjectKeys: "SR", ProjectKeysSet: true,
			},
			check: func(t *testing.T, rc *ResolvedConfig) {
				if rc.TimeZone != "UTC" {
					t.Errorf("timezone = %q, want UTC", rc.TimeZone)
				}
				if !rc.Summary {
					t.Error("summary should be true from file defaults")
				}
				if rc.OutputDir != "results" {
					t.Errorf("output_dir = %q, want results", rc.OutputDir)
				}
			},
		},
		{
			name: "profile overrides defaults",
			file: &FileConfig{
				Defaults: DefaultsConfig{
					TimeZone: "UTC",
					Summary:  boolPtr(false),
				},
				Profiles: map[string]ProfileConfig{
					"annual": {
						ProjectKeys: strPtr("SR,ISRE"),
						StartDate:   strPtr("2025-01-01"),
						EndDate:     strPtr("2025-12-31"),
						Summary:     boolPtr(true),
					},
				},
			},
			profile: "annual",
			check: func(t *testing.T, rc *ResolvedConfig) {
				if len(rc.ProjectKeys) != 2 || rc.ProjectKeys[0] != "SR" || rc.ProjectKeys[1] != "ISRE" {
					t.Errorf("project_keys = %v, want [SR ISRE]", rc.ProjectKeys)
				}
				if rc.StartDate != "2025-01-01" {
					t.Errorf("start = %q, want 2025-01-01", rc.StartDate)
				}
				if rc.EndDate != "2025-12-31" {
					t.Errorf("end = %q, want 2025-12-31", rc.EndDate)
				}
				if !rc.Summary {
					t.Error("summary should be true from profile")
				}
				if rc.TimeZone != "UTC" {
					t.Errorf("timezone = %q, want UTC (from defaults)", rc.TimeZone)
				}
			},
		},
		{
			name: "flags override profile",
			file: &FileConfig{
				Profiles: map[string]ProfileConfig{
					"test": {
						ProjectKeys: strPtr("SR"),
						StartDate:   strPtr("2025-01-01"),
						EndDate:     strPtr("2025-12-31"),
						Summary:     boolPtr(true),
					},
				},
			},
			profile: "test",
			flags: &FlagValues{
				ProjectKeys: "DATA", ProjectKeysSet: true,
				Summary: false, SummarySet: true,
			},
			check: func(t *testing.T, rc *ResolvedConfig) {
				if len(rc.ProjectKeys) != 1 || rc.ProjectKeys[0] != "DATA" {
					t.Errorf("project_keys = %v, want [DATA] (flag wins)", rc.ProjectKeys)
				}
				if rc.Summary {
					t.Error("summary should be false (flag wins over profile)")
				}
				// Start/end still from profile
				if rc.StartDate != "2025-01-01" {
					t.Errorf("start = %q, want 2025-01-01 (from profile)", rc.StartDate)
				}
			},
		},
		{
			name:    "unknown profile error",
			file:    &FileConfig{Profiles: map[string]ProfileConfig{"exists": {}}},
			profile: "nonexistent",
			wantErr: true,
		},
		{
			name:    "profile without config file error",
			file:    nil,
			profile: "anything",
			wantErr: true,
		},
		{
			name: "atlassian + github from file",
			file: &FileConfig{
				Atlassian: AtlassianConfig{
					Domain: "test.atlassian.net",
					Email:  "user@test.com",
					Token:  "secret",
				},
				GitHub: GitHubConfig{
					Token: "ghp_abc",
					User:  "octocat",
				},
			},
			check: func(t *testing.T, rc *ResolvedConfig) {
				if rc.AtlassianDomain != "test.atlassian.net" {
					t.Errorf("domain = %q", rc.AtlassianDomain)
				}
				if rc.AtlassianEmail != "user@test.com" {
					t.Errorf("email = %q", rc.AtlassianEmail)
				}
				if rc.AtlassianToken != "secret" {
					t.Errorf("token = %q", rc.AtlassianToken)
				}
				if rc.GitHubToken != "ghp_abc" {
					t.Errorf("github token = %q", rc.GitHubToken)
				}
				if rc.GitHubUser != "octocat" {
					t.Errorf("github user = %q", rc.GitHubUser)
				}
			},
		},
		{
			name: "profile with since",
			file: &FileConfig{
				Profiles: map[string]ProfileConfig{
					"recent": {
						Since:   strPtr("7d"),
						Summary: boolPtr(true),
					},
				},
			},
			profile: "recent",
			flags:   &FlagValues{ProjectKeys: "SR", ProjectKeysSet: true},
			check: func(t *testing.T, rc *ResolvedConfig) {
				expected := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
				if rc.StartDate != expected {
					t.Errorf("start = %q, want %q (7 days ago)", rc.StartDate, expected)
				}
				today := time.Now().Format("2006-01-02")
				if rc.EndDate != today {
					t.Errorf("end = %q, want %q (today)", rc.EndDate, today)
				}
			},
		},
		{
			name: "profile with filters",
			file: &FileConfig{
				Profiles: map[string]ProfileConfig{
					"filtered": {
						ProjectKeys:  strPtr("SR"),
						JiraStatus:   strPtr("Done,In Progress"),
						JiraType:     strPtr("Bug"),
						JiraPriority: strPtr("High,Critical"),
					},
				},
			},
			profile: "filtered",
			check: func(t *testing.T, rc *ResolvedConfig) {
				if len(rc.JiraStatus) != 2 {
					t.Errorf("jira_status = %v, want 2 items", rc.JiraStatus)
				}
				if len(rc.JiraType) != 1 || rc.JiraType[0] != "Bug" {
					t.Errorf("jira_type = %v, want [Bug]", rc.JiraType)
				}
				if len(rc.JiraPriority) != 2 {
					t.Errorf("jira_priority = %v, want 2 items", rc.JiraPriority)
				}
			},
		},
		{
			name: "no flags at all (nil)",
			file: &FileConfig{
				Defaults: DefaultsConfig{TimeZone: "Europe/London"},
			},
			flags: nil,
			check: func(t *testing.T, rc *ResolvedConfig) {
				if rc.TimeZone != "Europe/London" {
					t.Errorf("timezone = %q, want Europe/London", rc.TimeZone)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc, err := Resolve(tt.file, tt.profile, tt.flags)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, rc)
			}
		})
	}
}

func TestParseSince(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(t *testing.T, start, end string)
	}{
		{
			name:  "7 days",
			input: "7d",
			check: func(t *testing.T, start, end string) {
				expected := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
				if start != expected {
					t.Errorf("start = %q, want %q", start, expected)
				}
				if end != time.Now().Format("2006-01-02") {
					t.Errorf("end = %q, want today", end)
				}
			},
		},
		{
			name:  "2 weeks",
			input: "2w",
			check: func(t *testing.T, start, end string) {
				expected := time.Now().AddDate(0, 0, -14).Format("2006-01-02")
				if start != expected {
					t.Errorf("start = %q, want %q", start, expected)
				}
			},
		},
		{
			name:  "1 month",
			input: "1m",
			check: func(t *testing.T, start, end string) {
				expected := time.Now().AddDate(0, -1, 0).Format("2006-01-02")
				if start != expected {
					t.Errorf("start = %q, want %q", start, expected)
				}
			},
		},
		{
			name:  "1 year",
			input: "1y",
			check: func(t *testing.T, start, end string) {
				expected := time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
				if start != expected {
					t.Errorf("start = %q, want %q", start, expected)
				}
			},
		},
		{name: "empty", input: "", wantErr: true},
		{name: "single char", input: "d", wantErr: true},
		{name: "invalid unit", input: "7x", wantErr: true},
		{name: "zero", input: "0d", wantErr: true},
		{name: "negative", input: "-1d", wantErr: true},
		{name: "not a number", input: "abcd", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, err := ParseSince(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, start, end)
			}
		})
	}
}

func TestToQueryConfig(t *testing.T) {
	rc := &ResolvedConfig{
		Email:             "",
		ProjectKeys:       []string{"SR", "ISRE"},
		SpaceKeys:         nil,
		GitHubUser:        "octocat",
		GitHubAPIStrategy: "search",
		StartDate:         "2025-01-01",
		EndDate:           "2025-12-31",
		TimeZone:          "UTC",
		Debug:             true,
		JiraStatus:        []string{"Done"},
		JiraType:          []string{"Bug", "Story"},
		ConfluenceType:    "page",
		Summary:           true,
		Format:            "json",
	}

	qc, err := ToQueryConfig(rc)
	if err != nil {
		t.Fatalf("ToQueryConfig: %v", err)
	}

	if qc.Mode != models.ProjectMode {
		t.Errorf("mode = %v, want ProjectMode", qc.Mode)
	}
	if len(qc.ProjectKeys) != 2 {
		t.Errorf("project_keys = %v", qc.ProjectKeys)
	}
	if qc.GitHubUser != "octocat" {
		t.Errorf("github_user = %q", qc.GitHubUser)
	}
	if qc.GitHubAPIStrategy != "search" {
		t.Errorf("github_api = %q", qc.GitHubAPIStrategy)
	}
	if !qc.Debug {
		t.Error("debug should be true")
	}
	if !qc.Summary {
		t.Error("summary should be true")
	}
	if len(qc.JiraStatus) != 1 || qc.JiraStatus[0] != "Done" {
		t.Errorf("jira_status = %v", qc.JiraStatus)
	}
}

func TestToQueryConfigModes(t *testing.T) {
	tests := []struct {
		name     string
		rc       ResolvedConfig
		wantMode models.QueryMode
		wantErr  bool
	}{
		{
			name:     "user mode",
			rc:       ResolvedConfig{Email: "user@test.com"},
			wantMode: models.UserMode,
		},
		{
			name:     "project mode",
			rc:       ResolvedConfig{ProjectKeys: []string{"SR"}},
			wantMode: models.ProjectMode,
		},
		{
			name:     "space mode",
			rc:       ResolvedConfig{SpaceKeys: []string{"ENG"}},
			wantMode: models.SpaceMode,
		},
		{
			name:     "mixed mode",
			rc:       ResolvedConfig{ProjectKeys: []string{"SR"}, SpaceKeys: []string{"ENG"}},
			wantMode: models.MixedMode,
		},
		{
			name:     "github mode",
			rc:       ResolvedConfig{GitHubUser: "octocat"},
			wantMode: models.GitHubMode,
		},
		{
			name:    "no mode (error)",
			rc:      ResolvedConfig{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qc, err := ToQueryConfig(&tt.rc)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if qc.Mode != tt.wantMode {
				t.Errorf("mode = %v, want %v", qc.Mode, tt.wantMode)
			}
		})
	}
}

func TestResolve_WorkspaceConfigFromFile(t *testing.T) {
	file := &FileConfig{
		Workspace: &WorkspaceConfig{
			OrgPath:      "~/code/grindr",
			WorkspaceDir: "my_workspaces",
			GitCacheDir:  ".my_cache",
			Jira: &WorkspaceJiraConfig{
				DefaultRepos:  []string{"svc-auth", "svc-gateway", "infra-terraform"},
				BranchPattern: "feature/{key}",
			},
		},
	}

	rc, err := Resolve(file, "", nil)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}

	if rc.WorkspaceOrgPath != "~/code/grindr" {
		t.Errorf("WorkspaceOrgPath = %q, want ~/code/grindr", rc.WorkspaceOrgPath)
	}
	if rc.WorkspaceDir != "my_workspaces" {
		t.Errorf("WorkspaceDir = %q, want my_workspaces", rc.WorkspaceDir)
	}
	if rc.WorkspaceGitCacheDir != ".my_cache" {
		t.Errorf("WorkspaceGitCacheDir = %q, want .my_cache", rc.WorkspaceGitCacheDir)
	}
	if len(rc.WorkspaceDefaultRepos) != 3 {
		t.Fatalf("WorkspaceDefaultRepos count = %d, want 3", len(rc.WorkspaceDefaultRepos))
	}
	if rc.WorkspaceDefaultRepos[0] != "svc-auth" {
		t.Errorf("WorkspaceDefaultRepos[0] = %q, want svc-auth", rc.WorkspaceDefaultRepos[0])
	}
	if rc.WorkspaceDefaultRepos[2] != "infra-terraform" {
		t.Errorf("WorkspaceDefaultRepos[2] = %q, want infra-terraform", rc.WorkspaceDefaultRepos[2])
	}
	if rc.WorkspaceBranchPattern != "feature/{key}" {
		t.Errorf("WorkspaceBranchPattern = %q, want feature/{key}", rc.WorkspaceBranchPattern)
	}
}

func TestResolve_WorkspaceConfigPartial(t *testing.T) {
	// Only org_path set; other fields should retain hardcoded defaults.
	file := &FileConfig{
		Workspace: &WorkspaceConfig{
			OrgPath: "~/code/org",
		},
	}

	rc, err := Resolve(file, "", nil)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}

	if rc.WorkspaceOrgPath != "~/code/org" {
		t.Errorf("WorkspaceOrgPath = %q, want ~/code/org", rc.WorkspaceOrgPath)
	}
	// Defaults should remain
	if rc.WorkspaceDir != "jira_work" {
		t.Errorf("WorkspaceDir = %q, want jira_work (default)", rc.WorkspaceDir)
	}
	if rc.WorkspaceGitCacheDir != ".git_cache" {
		t.Errorf("WorkspaceGitCacheDir = %q, want .git_cache (default)", rc.WorkspaceGitCacheDir)
	}
	if rc.WorkspaceBranchPattern != "{key}" {
		t.Errorf("WorkspaceBranchPattern = %q, want {key} (default)", rc.WorkspaceBranchPattern)
	}
	if len(rc.WorkspaceDefaultRepos) != 2 {
		t.Errorf("WorkspaceDefaultRepos = %v, want default 2 repos", rc.WorkspaceDefaultRepos)
	}
}

func TestResolve_WorkspaceConfigNil(t *testing.T) {
	// When workspace is nil in file config, hardcoded defaults should apply.
	file := &FileConfig{
		Workspace: nil,
	}

	rc, err := Resolve(file, "", nil)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}

	if rc.WorkspaceOrgPath != "" {
		t.Errorf("WorkspaceOrgPath = %q, want empty (default)", rc.WorkspaceOrgPath)
	}
	if rc.WorkspaceDir != "jira_work" {
		t.Errorf("WorkspaceDir = %q, want jira_work (default)", rc.WorkspaceDir)
	}
	if rc.WorkspaceGitCacheDir != ".git_cache" {
		t.Errorf("WorkspaceGitCacheDir = %q, want .git_cache (default)", rc.WorkspaceGitCacheDir)
	}
	if rc.WorkspaceBranchPattern != "{key}" {
		t.Errorf("WorkspaceBranchPattern = %q, want {key} (default)", rc.WorkspaceBranchPattern)
	}
}

func TestApplyWorkspaceConfig_JiraNilNoRepos(t *testing.T) {
	// When Jira sub-config is nil, repos and branch pattern should not change.
	rc, _ := Resolve(nil, "", nil)
	origRepos := rc.WorkspaceDefaultRepos
	origPattern := rc.WorkspaceBranchPattern

	applyWorkspaceConfig(rc, &WorkspaceConfig{
		OrgPath: "/some/path",
		// Jira is nil
	})

	if rc.WorkspaceOrgPath != "/some/path" {
		t.Errorf("WorkspaceOrgPath = %q, want /some/path", rc.WorkspaceOrgPath)
	}
	if len(rc.WorkspaceDefaultRepos) != len(origRepos) {
		t.Errorf("WorkspaceDefaultRepos changed from %v", origRepos)
	}
	if rc.WorkspaceBranchPattern != origPattern {
		t.Errorf("WorkspaceBranchPattern changed from %q", origPattern)
	}
}

func TestApplyWorkspaceConfig_EmptyReposPreservesDefault(t *testing.T) {
	// When Jira.DefaultRepos is an empty slice, it should NOT overwrite the defaults
	// (the code checks len > 0).
	rc, _ := Resolve(nil, "", nil)
	origRepos := rc.WorkspaceDefaultRepos

	applyWorkspaceConfig(rc, &WorkspaceConfig{
		Jira: &WorkspaceJiraConfig{
			DefaultRepos:  []string{},
			BranchPattern: "",
		},
	})

	if len(rc.WorkspaceDefaultRepos) != len(origRepos) {
		t.Errorf("WorkspaceDefaultRepos = %v, want original %v (empty slice should not override)", rc.WorkspaceDefaultRepos, origRepos)
	}
}

func TestResolveOutputDirPropagation(t *testing.T) {
	rc, err := Resolve(&FileConfig{
		Defaults: DefaultsConfig{OutputDir: "custom"},
	}, "", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !strings.HasPrefix(rc.JiraOutput, "custom/") {
		t.Errorf("jira output = %q, want custom/ prefix", rc.JiraOutput)
	}
	if !strings.HasPrefix(rc.ConfluenceOutput, "custom/") {
		t.Errorf("confluence output = %q, want custom/ prefix", rc.ConfluenceOutput)
	}
	if !strings.HasPrefix(rc.GitHubOutput, "custom/") {
		t.Errorf("github output = %q, want custom/ prefix", rc.GitHubOutput)
	}
}
