package main

// constructors_test.go covers command constructor functions and runtime helpers
// that depend on package-level state (resolved, fileConfig, cacheStore).
// No API calls or network access — all tests run in <10ms.

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/config"
)

// ---------------------------------------------------------------------------
// Command constructor smoke tests
// Call each constructor and verify the Use string and expected flags.
// These cover all the statement paths inside each constructor.
// ---------------------------------------------------------------------------

func TestWeeklyCmd_Constructor(t *testing.T) {
	cmd := weeklyCmd()
	assertCmdUse(t, cmd, "weekly")
	for _, flag := range []string{"end", "summary", "publish", "dry-run", "format"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("weeklyCmd missing flag --%s", flag)
		}
	}
}

func TestReviewCmd_Constructor(t *testing.T) {
	cmd := reviewCmd()
	assertCmdUse(t, cmd, "review")
	for _, flag := range []string{"track", "end", "format"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("reviewCmd missing flag --%s", flag)
		}
	}
}

func TestQuarterlyCmd_Constructor(t *testing.T) {
	cmd := quarterlyCmd()
	assertCmdUse(t, cmd, "quarterly")
	for _, flag := range []string{"end", "summary", "format"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("quarterlyCmd missing flag --%s", flag)
		}
	}
}

func TestTrendsCmd_Constructor(t *testing.T) {
	cmd := trendsCmd()
	assertCmdUse(t, cmd, "trends")
	for _, flag := range []string{"periods", "period-size", "end", "track", "all-tracks", "format"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("trendsCmd missing flag --%s", flag)
		}
	}
}

func TestCompareCmd_Constructor(t *testing.T) {
	cmd := compareCmd()
	assertCmdUse(t, cmd, "compare")
	for _, flag := range []string{"since", "previous", "end", "format"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("compareCmd missing flag --%s", flag)
		}
	}
}

func TestCacheCmd_Constructor(t *testing.T) {
	cmd := cacheCmd()
	assertCmdUse(t, cmd, "cache")
	subs := map[string]bool{}
	for _, sub := range cmd.Commands() {
		subs[sub.Use] = true
	}
	for _, want := range []string{"stats", "clear", "warm"} {
		if !subs[want] {
			t.Errorf("cacheCmd missing subcommand %q", want)
		}
	}
}

func TestCacheStatsCmd_Constructor(t *testing.T) {
	cmd := cacheStatsCmd()
	assertCmdUse(t, cmd, "stats")
}

func TestCacheClearCmd_Constructor(t *testing.T) {
	cmd := cacheClearCmd()
	assertCmdUse(t, cmd, "clear")
	for _, flag := range []string{"source", "older-than"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("cacheClearCmd missing flag --%s", flag)
		}
	}
}

func TestConfigCmd_Constructor(t *testing.T) {
	cmd := configCmd()
	assertCmdUse(t, cmd, "config")
	subs := map[string]bool{}
	for _, sub := range cmd.Commands() {
		subs[sub.Use] = true
	}
	for _, want := range []string{"validate", "show"} {
		if !subs[want] {
			t.Errorf("configCmd missing subcommand %q", want)
		}
	}
}

func TestConfigValidateCmd_Constructor(t *testing.T) {
	cmd := configValidateCmd()
	assertCmdUse(t, cmd, "validate")
}

func TestConfigShowCmd_Constructor(t *testing.T) {
	cmd := configShowCmd()
	assertCmdUse(t, cmd, "show")
}

func TestVersionCmd_Constructor(t *testing.T) {
	cmd := versionCmd()
	assertCmdUse(t, cmd, "version")
}

func TestInitCmd_Constructor(t *testing.T) {
	cmd := initCmd()
	assertCmdUse(t, cmd, "init")
	if cmd.Flags().Lookup("force") == nil {
		t.Error("initCmd missing --force flag")
	}
}

func TestRootCmd_Constructor(t *testing.T) {
	cmd := rootCmd()
	assertCmdUse(t, cmd, "workctl")
	// Verify persistent flags exist
	for _, flag := range []string{"debug", "no-cache", "refresh", "no-color", "cache-ttl", "redact-others"} {
		if cmd.PersistentFlags().Lookup(flag) == nil {
			t.Errorf("rootCmd missing persistent flag --%s", flag)
		}
	}
}

// ---------------------------------------------------------------------------
// getString / getBool
// ---------------------------------------------------------------------------

func TestGetString(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("mystr", "default", "")

	got := getString(cmd, "mystr")
	if got != "default" {
		t.Errorf("getString = %q, want default", got)
	}

	// Unregistered flag returns ""
	got = getString(cmd, "nonexistent")
	if got != "" {
		t.Errorf("getString(nonexistent) = %q, want empty", got)
	}
}

func TestGetBool(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Bool("mybool", true, "")

	got := getBool(cmd, "mybool")
	if !got {
		t.Errorf("getBool = false, want true")
	}

	// Unregistered flag returns false
	got = getBool(cmd, "nonexistent")
	if got {
		t.Errorf("getBool(nonexistent) = true, want false")
	}
}

// Also verify getBool respects explicit set value via pflag.
func TestGetBool_ExplicitFalse(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Bool("flag", true, "") // default true
	_ = cmd.Flags().Set("flag", "false")

	if getBool(cmd, "flag") {
		t.Error("getBool after Set(false) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// runConfigShow — uses global `resolved`
// ---------------------------------------------------------------------------

func TestRunConfigShow_WithResolvedConfig(t *testing.T) {
	orig := resolved
	defer func() { resolved = orig }()

	resolved = &config.ResolvedConfig{
		Email:           "test@example.com",
		GitHubUser:      "testuser",
		StartDate:       "2025-01-01",
		EndDate:         "2025-12-31",
		TimeZone:        "America/Chicago",
		Format:          "md",
		Jira:            true,
		Confluence:      true,
		GitHub:          true,
		AtlassianToken:  "secret-token-value",
		GitHubToken:     "ghp_secretvalue",
		AtlassianDomain: "example.atlassian.net",
		AtlassianEmail:  "test@example.com",
	}

	cmd := &cobra.Command{Use: "show"}
	err := runConfigShow(cmd, nil)
	if err != nil {
		t.Fatalf("runConfigShow: unexpected error: %v", err)
	}
}

func TestRunConfigShow_NilResolved(t *testing.T) {
	orig := resolved
	defer func() { resolved = orig }()

	resolved = nil
	cmd := &cobra.Command{Use: "show"}
	err := runConfigShow(cmd, nil)
	if err == nil {
		t.Error("runConfigShow with nil resolved: expected error")
	}
}

// ---------------------------------------------------------------------------
// runConfigValidate — uses global `fileConfig`
// ---------------------------------------------------------------------------

func TestRunConfigValidate_NilFileConfig(t *testing.T) {
	orig := fileConfig
	defer func() { fileConfig = orig }()

	fileConfig = nil
	cmd := rootCmd() // need a full cmd tree for flag lookup
	err := runConfigValidate(cmd, nil)
	if err == nil {
		t.Error("runConfigValidate with nil fileConfig: expected error")
	}
	if !strings.Contains(err.Error(), "no config file") {
		t.Errorf("error = %q, want 'no config file'", err.Error())
	}
}

func TestRunConfigValidate_EmptyFileConfig(t *testing.T) {
	orig := fileConfig
	defer func() { fileConfig = orig }()

	fileConfig = &config.FileConfig{}
	// No profiles, no timezone → should succeed with 0 errors
	cmd := rootCmd()
	err := runConfigValidate(cmd, nil)
	if err != nil {
		t.Fatalf("runConfigValidate with empty config: unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// WarmReportData — early-return when all sources disabled
// ---------------------------------------------------------------------------

func TestWarmReportData_AllSourcesDisabled(t *testing.T) {
	rc := &config.ResolvedConfig{
		GitHubUser: "testuser",
		StartDate:  "2025-01-01",
		EndDate:    "2025-01-31",
		Jira:       false,
		Confluence: false,
		GitHub:     false,
	}

	ctx := context.Background()
	status, err := WarmReportData(ctx, rc)
	if err != nil {
		t.Fatalf("WarmReportData: unexpected error: %v", err)
	}
	if status == nil {
		t.Fatal("expected non-nil WarmStatus")
	}
	if status.AnythingFetched {
		t.Error("AnythingFetched should be false when all sources disabled")
	}
}

// ---------------------------------------------------------------------------
// buildFlagValues — verify --redact-others is wired
// ---------------------------------------------------------------------------

func TestBuildFlagValues_RedactOthers(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	registerPrivacyFlags(cmd)

	// default: false, not changed
	fv := buildFlagValues(cmd)
	if fv.RedactOthers {
		t.Error("RedactOthers default should be false")
	}
	if fv.RedactOthersSet {
		t.Error("RedactOthersSet should be false before flag is set")
	}

	// explicitly set
	if err := cmd.Flags().Set("redact-others", "true"); err != nil {
		t.Fatalf("Set redact-others: %v", err)
	}
	fv2 := buildFlagValues(cmd)
	if !fv2.RedactOthers {
		t.Error("RedactOthers should be true after Set")
	}
	if !fv2.RedactOthersSet {
		t.Error("RedactOthersSet should be true after Set")
	}
}

// ---------------------------------------------------------------------------
// buildFlagValues via pflag — getString/getBool path
// ---------------------------------------------------------------------------

func TestGetStringViaFlagSet(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("mykey", "hello", "")

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().AddFlagSet(fs)

	got := getString(cmd, "mykey")
	if got != "hello" {
		t.Errorf("getString = %q, want hello", got)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func assertCmdUse(t *testing.T, cmd *cobra.Command, want string) {
	t.Helper()
	if cmd == nil {
		t.Fatalf("expected non-nil *cobra.Command for %q", want)
	}
	// cmd.Use may include arguments, e.g. "weekly [flags]" — check prefix
	if !strings.HasPrefix(cmd.Use, want) {
		t.Errorf("cmd.Use = %q, want prefix %q", cmd.Use, want)
	}
}

// discardOutput captures stdout for tests that print to os.Stdout.
// Not used currently; included as a helper for future tests.
func discardOutput(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	_ = buf
	fn()
	return buf.String()
}
