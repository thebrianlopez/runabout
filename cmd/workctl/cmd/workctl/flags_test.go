package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestRegisterIdentityFlags verifies expected flags are registered.
func TestRegisterIdentityFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	registerIdentityFlags(cmd)

	for _, name := range []string{"email", "project-keys", "space-keys", "github-user", "github-api"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("registerIdentityFlags: expected flag %q to be registered", name)
		}
	}
}

// TestRegisterDateFlags verifies --start and --timezone are registered but NOT --end.
func TestRegisterDateFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	registerDateFlags(cmd)

	for _, name := range []string{"start", "timezone"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("registerDateFlags: expected flag %q to be registered", name)
		}
	}
	if cmd.Flags().Lookup("end") != nil {
		t.Error("registerDateFlags: should NOT register --end (commands register it individually)")
	}
}

// TestRegisterSourceToggleFlags verifies source toggle flags.
func TestRegisterSourceToggleFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	registerSourceToggleFlags(cmd)

	for _, name := range []string{"jira", "confluence", "github"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("registerSourceToggleFlags: expected flag %q to be registered", name)
		}
	}
}

// TestRegisterJiraFilterFlags verifies Jira filter flags.
func TestRegisterJiraFilterFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	registerJiraFilterFlags(cmd)

	for _, name := range []string{"jira-status", "jira-type", "jira-priority"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("registerJiraFilterFlags: expected flag %q to be registered", name)
		}
	}
}

// TestRegisterConfluenceFilterFlags verifies Confluence filter flags.
func TestRegisterConfluenceFilterFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	registerConfluenceFilterFlags(cmd)

	for _, name := range []string{"confluence-type", "confluence-hydrate"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("registerConfluenceFilterFlags: expected flag %q to be registered", name)
		}
	}
}

// TestRegisterGitHubFilterFlags verifies GitHub filter flags.
func TestRegisterGitHubFilterFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	registerGitHubFilterFlags(cmd)

	for _, name := range []string{"github-repos", "github-enrich"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("registerGitHubFilterFlags: expected flag %q to be registered", name)
		}
	}
}

// TestRegisterAllFetchFlags verifies the convenience function registers all 17 shared flags.
func TestRegisterAllFetchFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	registerAllFetchFlags(cmd)

	expected := []string{
		"email", "project-keys", "space-keys", "github-user", "github-api",
		"start", "timezone",
		"jira", "confluence", "github",
		"jira-status", "jira-type", "jira-priority",
		"confluence-type", "confluence-hydrate",
		"github-repos", "github-enrich",
	}
	for _, name := range expected {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("registerAllFetchFlags: expected flag %q to be registered", name)
		}
	}
}

// TestRegisterReportOutputFlags verifies --output and --format registration.
func TestRegisterReportOutputFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	registerReportOutputFlags(cmd, "md")

	if cmd.Flags().Lookup("output") == nil {
		t.Error("registerReportOutputFlags: expected flag 'output' to be registered")
	}
	if cmd.Flags().Lookup("format") == nil {
		t.Error("registerReportOutputFlags: expected flag 'format' to be registered")
	}

	// Verify default format
	v, err := cmd.Flags().GetString("format")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "md" {
		t.Errorf("expected default format 'md', got %q", v)
	}

	// Test with different default
	cmd2 := &cobra.Command{Use: "test2"}
	registerReportOutputFlags(cmd2, "json")
	v2, _ := cmd2.Flags().GetString("format")
	if v2 != "json" {
		t.Errorf("expected default format 'json', got %q", v2)
	}
}

// TestGetStringIfExists_Unregistered returns empty and false for missing flags.
func TestGetStringIfExists_Unregistered(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}

	val, set := getStringIfExists(cmd, "nonexistent")
	if val != "" {
		t.Errorf("expected empty string, got %q", val)
	}
	if set {
		t.Error("expected set=false for unregistered flag")
	}
}

// TestGetBoolIfExists_Unregistered returns false and false for missing flags.
func TestGetBoolIfExists_Unregistered(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}

	val, set := getBoolIfExists(cmd, "nonexistent")
	if val {
		t.Error("expected false value for unregistered flag")
	}
	if set {
		t.Error("expected set=false for unregistered flag")
	}
}

// TestGetStringIfExists_Registered returns value and Changed status.
func TestGetStringIfExists_Registered(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("foo", "default", "test flag")

	// Unset — should return default value with set=false
	val, set := getStringIfExists(cmd, "foo")
	if val != "default" {
		t.Errorf("expected 'default', got %q", val)
	}
	if set {
		t.Error("expected set=false for unmodified flag")
	}

	// Simulate user passing --foo=bar
	cmd.Flags().Set("foo", "bar")
	val, set = getStringIfExists(cmd, "foo")
	if val != "bar" {
		t.Errorf("expected 'bar', got %q", val)
	}
	if !set {
		t.Error("expected set=true after flag was changed")
	}
}

// TestBuildFlagValues_MinimalCommand verifies buildFlagValues doesn't panic on
// a command with no data-fetching flags (e.g. version, cache).
func TestBuildFlagValues_MinimalCommand(t *testing.T) {
	cmd := &cobra.Command{Use: "version"}
	// Only persistent flags that would be inherited from root
	cmd.PersistentFlags().Bool("debug", false, "")
	cmd.PersistentFlags().String("cache-ttl", "", "")

	fv := buildFlagValues(cmd)

	// All data fields should be zero-valued
	if fv.Email != "" {
		t.Errorf("expected empty email, got %q", fv.Email)
	}
	if fv.EmailSet {
		t.Error("expected EmailSet=false")
	}
	if fv.Jira {
		t.Error("expected Jira=false for unregistered flag")
	}
}

// TestBuildFlagValues_FullCommand verifies buildFlagValues populates all fields.
func TestBuildFlagValues_FullCommand(t *testing.T) {
	cmd := &cobra.Command{Use: "weekly"}
	registerAllFetchFlags(cmd)
	cmd.Flags().String("end", "", "End date")
	cmd.Flags().String("format", "md", "Output format")
	cmd.Flags().Bool("summary", false, "Summary")
	cmd.PersistentFlags().Bool("debug", false, "Debug")

	// Simulate user passing --email
	cmd.Flags().Set("email", "test@example.com")

	fv := buildFlagValues(cmd)
	if fv.Email != "test@example.com" {
		t.Errorf("expected 'test@example.com', got %q", fv.Email)
	}
	if !fv.EmailSet {
		t.Error("expected EmailSet=true")
	}
	// Jira default is true
	if !fv.Jira {
		t.Error("expected Jira=true (default)")
	}
	if fv.JiraSet {
		t.Error("expected JiraSet=false (not explicitly set)")
	}
}

// TestNoDuplicateRegistrationPanic verifies that calling registerAllFetchFlags
// once doesn't create duplicate flags that would panic cobra.
func TestNoDuplicateRegistrationPanic(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	// Should not panic
	registerAllFetchFlags(cmd)
	registerReportOutputFlags(cmd, "md")
	cmd.Flags().String("end", "", "End date")
	cmd.Flags().Bool("summary", false, "Summary")

	// Verify total flag count is reasonable
	count := 0
	cmd.Flags().VisitAll(func(f *pflag.Flag) { count++ })
	if count < 17 {
		t.Errorf("expected at least 17 flags, got %d", count)
	}
}

// TestSharedFlagDescriptionsMatchRoot verifies that the shared helpers
// produce the same descriptions as root.go's original definitions.
func TestSharedFlagDescriptionsMatchRoot(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	registerIdentityFlags(cmd)
	registerSourceToggleFlags(cmd)
	registerJiraFilterFlags(cmd)
	registerConfluenceFilterFlags(cmd)
	registerGitHubFilterFlags(cmd)

	// Spot-check key descriptions match root.go
	checks := map[string]string{
		"email":              "Jira user email address",
		"project-keys":       "Comma-separated Jira project keys (e.g., SR,ISRE)",
		"github-api":         "GitHub API strategy: auto|events|search|graphql",
		"jira":               "Enable Jira issues",
		"confluence":         "Enable Confluence articles",
		"github":             "Enable GitHub activity",
		"jira-status":        "Filter Jira issues by status (e.g., Done,In Progress)",
		"confluence-type":    "Filter Confluence content type (page, blogpost)",
		"confluence-hydrate": "Enable metadata hydration (slower but accurate)",
		"github-repos":       "Comma-separated repos for commit history (e.g. org/repo1,org/repo2)",
		"github-enrich":      "Hydrate commits with per-file diff stats (slower)",
	}

	for name, expectedUsage := range checks {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Errorf("flag %q not registered", name)
			continue
		}
		if f.Usage != expectedUsage {
			t.Errorf("flag %q usage mismatch:\n  got:  %q\n  want: %q", name, f.Usage, expectedUsage)
		}
	}
}
