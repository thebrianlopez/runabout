package config

// resolve_gap_test.go — targeted tests to close coverage gaps in resolve.go.
// Exercises applyFileDefaults, applyCacheConfig, applyProfile, applyFlags,
// and the Resolve() error paths that were 0% per the coverage report.

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// applyFileDefaults — exercise every conditional branch
// ---------------------------------------------------------------------------

func TestApplyFileDefaults_AllFields(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)
	defaults := DefaultsConfig{
		TimeZone:          "Europe/London",
		OutputDir:         "/tmp/workctl-test",
		Format:            "csv",
		Summary:           boolPtr(true),
		Debug:             boolPtr(true),
		Jira:              boolPtr(false),
		Confluence:        boolPtr(false),
		GitHubEnabled:     boolPtr(false),
		ConfluenceType:    "blogpost",
		ConfluenceHydrate: boolPtr(true),
		GitHubAPIStrategy: "graphql",
		GitHubRepos:       "org/repo1, org/repo2",
		GitHubEnrich:      boolPtr(true),
	}

	applyFileDefaults(rc, &defaults)

	if rc.TimeZone != "Europe/London" {
		t.Errorf("TimeZone = %q, want Europe/London", rc.TimeZone)
	}
	if rc.OutputDir != "/tmp/workctl-test" {
		t.Errorf("OutputDir = %q, want /tmp/workctl-test", rc.OutputDir)
	}
	if !strings.HasPrefix(rc.JiraOutput, "/tmp/workctl-test/") {
		t.Errorf("JiraOutput = %q, expected prefix /tmp/workctl-test/", rc.JiraOutput)
	}
	if rc.Format != "csv" {
		t.Errorf("Format = %q, want csv", rc.Format)
	}
	if !rc.Summary {
		t.Error("Summary should be true")
	}
	if !rc.Debug {
		t.Error("Debug should be true")
	}
	if rc.Jira {
		t.Error("Jira should be false")
	}
	if rc.Confluence {
		t.Error("Confluence should be false")
	}
	if rc.GitHub {
		t.Error("GitHub should be false")
	}
	if rc.ConfluenceType != "blogpost" {
		t.Errorf("ConfluenceType = %q, want blogpost", rc.ConfluenceType)
	}
	if !rc.ConfluenceHydrate {
		t.Error("ConfluenceHydrate should be true")
	}
	if rc.GitHubAPIStrategy != "graphql" {
		t.Errorf("GitHubAPIStrategy = %q, want graphql", rc.GitHubAPIStrategy)
	}
	if len(rc.GitHubRepos) != 2 {
		t.Errorf("GitHubRepos len = %d, want 2: %v", len(rc.GitHubRepos), rc.GitHubRepos)
	}
	if !rc.GitHubEnrich {
		t.Error("GitHubEnrich should be true")
	}
}

func TestApplyFileDefaults_EmptyFieldsNoChange(t *testing.T) {
	// When all defaults are zero, nothing in rc should change.
	rc, _ := Resolve(nil, "", nil)
	origTZ := rc.TimeZone
	origFmt := rc.Format

	applyFileDefaults(rc, &DefaultsConfig{})

	if rc.TimeZone != origTZ {
		t.Errorf("TimeZone changed to %q, want %q", rc.TimeZone, origTZ)
	}
	if rc.Format != origFmt {
		t.Errorf("Format changed to %q, want %q", rc.Format, origFmt)
	}
}

// ---------------------------------------------------------------------------
// applyCacheConfig — Enabled + TTL branches
// ---------------------------------------------------------------------------

func TestApplyCacheConfig_DisableCache(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)
	enabled := false
	applyCacheConfig(rc, &CacheConfig{Enabled: &enabled})
	if rc.CacheEnabled {
		t.Error("CacheEnabled should be false")
	}
}

func TestApplyCacheConfig_PerSourceTTLs(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)
	enabled := true
	applyCacheConfig(rc, &CacheConfig{
		Enabled: &enabled,
		TTL: &CacheTTLConfig{
			Jira:          "2h",
			Confluence:    "30m",
			GitHubEvents:  "15m",
			GitHubSearch:  "45m",
			GitHubGraphQL: "1h",
		},
	})

	if rc.CacheTTL["jira"] != 2*time.Hour {
		t.Errorf("jira TTL = %v, want 2h", rc.CacheTTL["jira"])
	}
	if rc.CacheTTL["confluence"] != 30*time.Minute {
		t.Errorf("confluence TTL = %v, want 30m", rc.CacheTTL["confluence"])
	}
	if rc.CacheTTL["github_events"] != 15*time.Minute {
		t.Errorf("github_events TTL = %v, want 15m", rc.CacheTTL["github_events"])
	}
	if rc.CacheTTL["github_search"] != 45*time.Minute {
		t.Errorf("github_search TTL = %v, want 45m", rc.CacheTTL["github_search"])
	}
	if rc.CacheTTL["github_graphql"] != time.Hour {
		t.Errorf("github_graphql TTL = %v, want 1h", rc.CacheTTL["github_graphql"])
	}
}

func TestApplyCacheConfig_InvalidTTLIgnored(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)
	// Invalid duration string — should be silently ignored.
	applyCacheConfig(rc, &CacheConfig{
		TTL: &CacheTTLConfig{Jira: "not-a-duration"},
	})
	if _, ok := rc.CacheTTL["jira"]; ok {
		t.Error("invalid TTL should not be stored, but jira key exists")
	}
}

func TestApplyCacheConfig_EmptyTTLStringIgnored(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)
	// Empty string → parseTTL returns early without setting a key.
	applyCacheConfig(rc, &CacheConfig{
		TTL: &CacheTTLConfig{Jira: ""},
	})
	if _, ok := rc.CacheTTL["jira"]; ok {
		t.Error("empty TTL string should not set a cache key")
	}
}

// ---------------------------------------------------------------------------
// applyProfile — all pointer fields
// ---------------------------------------------------------------------------

func TestApplyProfile_AllFields(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)

	p := ProfileConfig{
		Email:             strPtr("user@example.com"),
		ProjectKeys:       strPtr("SR,ISRE"),
		SpaceKeys:         strPtr("ENG,INFRA"),
		GitHubUser:        strPtr("octocat"),
		GitHubAPIStrategy: strPtr("events"),
		GitHubRepos:       strPtr("org/repo"),
		GitHubEnrich:      boolPtr(true),
		StartDate:         strPtr("2026-01-01"),
		EndDate:           strPtr("2026-01-31"),
		TimeZone:          strPtr("UTC"),
		OutputDir:         strPtr("/tmp/profile-out"),
		Format:            strPtr("json"),
		Summary:           boolPtr(true),
		Debug:             boolPtr(true),
		Jira:              boolPtr(false),
		Confluence:        boolPtr(false),
		GitHubEnabled:     boolPtr(false),
		ConfluenceType:    strPtr("blogpost"),
		ConfluenceHydrate: boolPtr(true),
		JiraStatus:        strPtr("Done,Closed"),
		JiraType:          strPtr("Bug,Story"),
		JiraPriority:      strPtr("P0,P1"),
	}

	if err := applyProfile(rc, &p); err != nil {
		t.Fatalf("applyProfile error: %v", err)
	}

	if rc.Email != "user@example.com" {
		t.Errorf("Email = %q", rc.Email)
	}
	if len(rc.ProjectKeys) != 2 || rc.ProjectKeys[0] != "SR" {
		t.Errorf("ProjectKeys = %v", rc.ProjectKeys)
	}
	if len(rc.SpaceKeys) != 2 || rc.SpaceKeys[0] != "ENG" {
		t.Errorf("SpaceKeys = %v", rc.SpaceKeys)
	}
	if rc.GitHubUser != "octocat" {
		t.Errorf("GitHubUser = %q", rc.GitHubUser)
	}
	if rc.GitHubAPIStrategy != "events" {
		t.Errorf("GitHubAPIStrategy = %q", rc.GitHubAPIStrategy)
	}
	if len(rc.GitHubRepos) != 1 {
		t.Errorf("GitHubRepos = %v", rc.GitHubRepos)
	}
	if !rc.GitHubEnrich {
		t.Error("GitHubEnrich should be true")
	}
	if rc.StartDate != "2026-01-01" {
		t.Errorf("StartDate = %q", rc.StartDate)
	}
	if rc.EndDate != "2026-01-31" {
		t.Errorf("EndDate = %q", rc.EndDate)
	}
	if rc.TimeZone != "UTC" {
		t.Errorf("TimeZone = %q", rc.TimeZone)
	}
	if rc.OutputDir != "/tmp/profile-out" {
		t.Errorf("OutputDir = %q", rc.OutputDir)
	}
	if !strings.HasPrefix(rc.JiraOutput, "/tmp/profile-out/") {
		t.Errorf("JiraOutput = %q should start with /tmp/profile-out/", rc.JiraOutput)
	}
	if rc.Format != "json" {
		t.Errorf("Format = %q", rc.Format)
	}
	if !rc.Summary {
		t.Error("Summary should be true")
	}
	if !rc.Debug {
		t.Error("Debug should be true")
	}
	if rc.Jira {
		t.Error("Jira should be false")
	}
	if rc.Confluence {
		t.Error("Confluence should be false")
	}
	if rc.GitHub {
		t.Error("GitHub should be false")
	}
	if rc.ConfluenceType != "blogpost" {
		t.Errorf("ConfluenceType = %q", rc.ConfluenceType)
	}
	if !rc.ConfluenceHydrate {
		t.Error("ConfluenceHydrate should be true")
	}
	if len(rc.JiraStatus) != 2 {
		t.Errorf("JiraStatus = %v", rc.JiraStatus)
	}
	if len(rc.JiraType) != 2 {
		t.Errorf("JiraType = %v", rc.JiraType)
	}
	if len(rc.JiraPriority) != 2 {
		t.Errorf("JiraPriority = %v", rc.JiraPriority)
	}
}

func TestApplyProfile_IndividualOutputOverrides(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)

	p := ProfileConfig{
		JiraOutput:       strPtr("/custom/jira.json"),
		ConfluenceOutput: strPtr("/custom/conf.json"),
		GitHubOutput:     strPtr("/custom/github.json"),
	}

	if err := applyProfile(rc, &p); err != nil {
		t.Fatalf("applyProfile error: %v", err)
	}

	if rc.JiraOutput != "/custom/jira.json" {
		t.Errorf("JiraOutput = %q, want /custom/jira.json", rc.JiraOutput)
	}
	if rc.ConfluenceOutput != "/custom/conf.json" {
		t.Errorf("ConfluenceOutput = %q, want /custom/conf.json", rc.ConfluenceOutput)
	}
	if rc.GitHubOutput != "/custom/github.json" {
		t.Errorf("GitHubOutput = %q, want /custom/github.json", rc.GitHubOutput)
	}
}

func TestApplyProfile_SinceField(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)

	p := ProfileConfig{Since: strPtr("7d")}
	if err := applyProfile(rc, &p); err != nil {
		t.Fatalf("applyProfile Since error: %v", err)
	}
	// StartDate should be a valid date (7 days ago)
	if rc.StartDate == "" {
		t.Error("StartDate should be set from Since")
	}
	if rc.EndDate == "" {
		t.Error("EndDate should be set from Since")
	}
}

func TestApplyProfile_SinceInvalid(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)

	p := ProfileConfig{Since: strPtr("bad-value")}
	err := applyProfile(rc, &p)
	if err == nil {
		t.Error("applyProfile with invalid Since should return error")
	}
}

// ---------------------------------------------------------------------------
// applyFlags — all Set fields
// ---------------------------------------------------------------------------

func TestApplyFlags_NilNoOp(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)
	origTZ := rc.TimeZone

	applyFlags(rc, nil) // must not panic

	if rc.TimeZone != origTZ {
		t.Error("applyFlags(nil) should not modify rc")
	}
}

func TestApplyFlags_AllFields(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)

	f := &FlagValues{
		Email:             "flag@example.com",
		EmailSet:          true,
		ProjectKeys:       "A,B",
		ProjectKeysSet:    true,
		SpaceKeys:         "S1,S2",
		SpaceKeysSet:      true,
		GitHubUser:        "ghuser",
		GitHubUserSet:     true,
		GitHubAPIStrategy: "search",
		GitHubAPISet:      true,
		StartDate:         "2026-03-01",
		StartDateSet:      true,
		EndDate:           "2026-03-31",
		EndDateSet:        true,
		TimeZone:          "Asia/Tokyo",
		TimeZoneSet:       true,
		JiraOutput:        "/out/jira.json",
		JiraOutputSet:     true,
		ConfluenceOutput:  "/out/conf.json",
		ConfOutputSet:     true,
		GitHubOutput:      "/out/gh.json",
		GitHubOutputSet:   true,
		Format:            "json",
		FormatSet:         true,
		Summary:           true,
		SummarySet:        true,
		Debug:             true,
		DebugSet:          true,
		Jira:              false,
		JiraSet:           true,
		Confluence:        false,
		ConfluenceSet:     true,
		GitHub:            false,
		GitHubSet:         true,
		JiraStatus:        "Done",
		JiraStatusSet:     true,
		JiraType:          "Bug",
		JiraTypeSet:       true,
		JiraPriority:      "P0",
		JiraPrioritySet:   true,
		ConfluenceType:    "page",
		ConfTypeSet:       true,
		ConfluenceHydrate: true,
		ConfHydrateSet:    true,
		GitHubRepos:       "org/r",
		GitHubReposSet:    true,
		GitHubEnrich:      true,
		GitHubEnrichSet:   true,
	}

	applyFlags(rc, f)

	if rc.Email != "flag@example.com" {
		t.Errorf("Email = %q", rc.Email)
	}
	if len(rc.ProjectKeys) != 2 {
		t.Errorf("ProjectKeys = %v", rc.ProjectKeys)
	}
	if len(rc.SpaceKeys) != 2 {
		t.Errorf("SpaceKeys = %v", rc.SpaceKeys)
	}
	if rc.GitHubUser != "ghuser" {
		t.Errorf("GitHubUser = %q", rc.GitHubUser)
	}
	if rc.GitHubAPIStrategy != "search" {
		t.Errorf("GitHubAPIStrategy = %q", rc.GitHubAPIStrategy)
	}
	if rc.StartDate != "2026-03-01" {
		t.Errorf("StartDate = %q", rc.StartDate)
	}
	if rc.EndDate != "2026-03-31" {
		t.Errorf("EndDate = %q", rc.EndDate)
	}
	if rc.TimeZone != "Asia/Tokyo" {
		t.Errorf("TimeZone = %q", rc.TimeZone)
	}
	if rc.JiraOutput != "/out/jira.json" {
		t.Errorf("JiraOutput = %q", rc.JiraOutput)
	}
	if rc.ConfluenceOutput != "/out/conf.json" {
		t.Errorf("ConfluenceOutput = %q", rc.ConfluenceOutput)
	}
	if rc.GitHubOutput != "/out/gh.json" {
		t.Errorf("GitHubOutput = %q", rc.GitHubOutput)
	}
	if rc.Jira {
		t.Error("Jira should be false")
	}
	if rc.Confluence {
		t.Error("Confluence should be false")
	}
	if rc.GitHub {
		t.Error("GitHub should be false")
	}
	if len(rc.JiraStatus) != 1 || rc.JiraStatus[0] != "Done" {
		t.Errorf("JiraStatus = %v", rc.JiraStatus)
	}
	if len(rc.JiraType) != 1 || rc.JiraType[0] != "Bug" {
		t.Errorf("JiraType = %v", rc.JiraType)
	}
	if len(rc.JiraPriority) != 1 {
		t.Errorf("JiraPriority = %v", rc.JiraPriority)
	}
	if rc.ConfluenceType != "page" {
		t.Errorf("ConfluenceType = %q", rc.ConfluenceType)
	}
	if !rc.ConfluenceHydrate {
		t.Error("ConfluenceHydrate should be true")
	}
	if len(rc.GitHubRepos) != 1 {
		t.Errorf("GitHubRepos = %v", rc.GitHubRepos)
	}
	if !rc.GitHubEnrich {
		t.Error("GitHubEnrich should be true")
	}
}

func TestApplyFlags_CacheTTL_ValidDuration(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)

	applyFlags(rc, &FlagValues{
		CacheTTLOverride: "4h",
		CacheTTLSet:      true,
	})

	if !rc.CacheEnabled {
		t.Error("CacheEnabled should remain true for non-zero TTL")
	}
	for _, src := range []string{"jira", "confluence", "github_events", "github_search", "github_graphql"} {
		if rc.CacheTTL[src] != 4*time.Hour {
			t.Errorf("CacheTTL[%s] = %v, want 4h", src, rc.CacheTTL[src])
		}
	}
}

func TestApplyFlags_CacheTTL_ZeroDisablesCache(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)

	applyFlags(rc, &FlagValues{
		CacheTTLOverride: "0",
		CacheTTLSet:      true,
	})

	if rc.CacheEnabled {
		t.Error("CacheEnabled should be false for TTL=0")
	}
}

func TestApplyFlags_CacheTTL_InvalidIgnored(t *testing.T) {
	rc, _ := Resolve(nil, "", nil)
	// Invalid duration that is also not "0" → LogDebug + return early.
	applyFlags(rc, &FlagValues{
		CacheTTLOverride: "notaduration",
		CacheTTLSet:      true,
	})
	// CacheTTL should remain nil (no keys set)
	if len(rc.CacheTTL) != 0 {
		t.Errorf("CacheTTL should be empty for invalid input, got %v", rc.CacheTTL)
	}
}

// ---------------------------------------------------------------------------
// Resolve — error paths and path rebuilding
// ---------------------------------------------------------------------------

func TestResolve_ProfileWithNoFile(t *testing.T) {
	_, err := Resolve(nil, "myprofile", nil)
	if err == nil {
		t.Error("Resolve with profile but no file should return error")
	}
	if !strings.Contains(err.Error(), "--profile") {
		t.Errorf("error should mention --profile, got: %v", err)
	}
}

func TestResolve_UnknownProfile(t *testing.T) {
	file := &FileConfig{
		Profiles: map[string]ProfileConfig{
			"existing": {},
		},
	}
	_, err := Resolve(file, "nonexistent", nil)
	if err == nil {
		t.Error("Resolve with unknown profile should return error")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention profile name, got: %v", err)
	}
	// Should list available profiles.
	if !strings.Contains(err.Error(), "existing") {
		t.Errorf("error should list available profiles, got: %v", err)
	}
}

func TestResolve_ProfileApplyError(t *testing.T) {
	// Profile with invalid Since causes applyProfile to return error.
	file := &FileConfig{
		Profiles: map[string]ProfileConfig{
			"bad": {Since: strPtr("bad-value")},
		},
	}
	_, err := Resolve(file, "bad", nil)
	if err == nil {
		t.Error("Resolve with invalid profile Since should return error")
	}
}

func TestResolve_OutputPathRebuiltWhenOutputDirChanges(t *testing.T) {
	// When a profile sets OutputDir but flags don't set individual output paths,
	// Resolve should rebuild jira/conf/github output paths.
	file := &FileConfig{
		Profiles: map[string]ProfileConfig{
			"custom": {OutputDir: strPtr("/tmp/new-dir")},
		},
	}
	// Flags with no individual output paths set.
	flags := &FlagValues{}

	rc, err := Resolve(file, "custom", flags)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if !strings.HasPrefix(rc.JiraOutput, "/tmp/new-dir/") {
		t.Errorf("JiraOutput = %q, expected /tmp/new-dir/ prefix", rc.JiraOutput)
	}
	if !strings.HasPrefix(rc.ConfluenceOutput, "/tmp/new-dir/") {
		t.Errorf("ConfluenceOutput = %q, expected /tmp/new-dir/ prefix", rc.ConfluenceOutput)
	}
	if !strings.HasPrefix(rc.GitHubOutput, "/tmp/new-dir/") {
		t.Errorf("GitHubOutput = %q, expected /tmp/new-dir/ prefix", rc.GitHubOutput)
	}
}

func TestResolve_ValidGitHubRepoFromFile(t *testing.T) {
	file := &FileConfig{
		Defaults: DefaultsConfig{
			GitHubRepos: "org/valid-repo",
		},
	}
	rc, err := Resolve(file, "", nil)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if len(rc.GitHubRepos) != 1 || rc.GitHubRepos[0] != "org/valid-repo" {
		t.Errorf("GitHubRepos = %v", rc.GitHubRepos)
	}
}

func TestResolve_InvalidGitHubRepoReturnsError(t *testing.T) {
	file := &FileConfig{
		Defaults: DefaultsConfig{
			GitHubRepos: "not-a-valid-repo",
		},
	}
	_, err := Resolve(file, "", nil)
	if err == nil {
		t.Error("Resolve with invalid github_repos should return error")
	}
}

func TestResolve_NilFileNilFlags_ReturnsDefaults(t *testing.T) {
	rc, err := Resolve(nil, "", nil)
	if err != nil {
		t.Fatalf("Resolve(nil,\"\",nil) error: %v", err)
	}
	// Default values from hardcoded layer
	if rc.TimeZone == "" {
		t.Error("TimeZone should have a default")
	}
	if rc.Format == "" {
		t.Error("Format should have a default")
	}
	if !rc.CacheEnabled {
		t.Error("CacheEnabled should default to true")
	}
}

// ---------------------------------------------------------------------------
// ParseSince — error paths and all unit variants
// ---------------------------------------------------------------------------

func TestParseSince_AllUnits(t *testing.T) {
	cases := []struct{ input, unit string }{
		{"7d", "day"},
		{"2w", "week"},
		{"3m", "month"},
		{"1y", "year"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			start, end, err := ParseSince(c.input)
			if err != nil {
				t.Errorf("ParseSince(%q) error: %v", c.input, err)
			}
			if start == "" || end == "" {
				t.Errorf("ParseSince(%q) returned empty start/end", c.input)
			}
		})
	}
}

func TestParseSince_TooShort(t *testing.T) {
	_, _, err := ParseSince("1")
	if err == nil {
		t.Error("ParseSince with 1-char input should error")
	}
}

func TestParseSince_InvalidNumber(t *testing.T) {
	_, _, err := ParseSince("xd")
	if err == nil {
		t.Error("ParseSince with non-numeric number should error")
	}
}

func TestParseSince_ZeroNumber(t *testing.T) {
	_, _, err := ParseSince("0d")
	if err == nil {
		t.Error("ParseSince with 0 should error (must be positive)")
	}
}

func TestParseSince_UnknownUnit(t *testing.T) {
	_, _, err := ParseSince("5z")
	if err == nil {
		t.Error("ParseSince with unknown unit should error")
	}
}
