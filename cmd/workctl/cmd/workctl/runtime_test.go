package main

// runtime_test.go covers functions that require a real XDG-redirected filesystem
// or the global `resolved` state. All tests are self-contained and use t.TempDir()
// or t.Setenv() to avoid touching real user state.

import (
	"context"
	"testing"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/config"
)

// ---------------------------------------------------------------------------
// runCacheStats / runCacheClear — uses XDG_CACHE_HOME redirect
// ---------------------------------------------------------------------------

func TestRunCacheStats_NoDB(t *testing.T) {
	// Redirect cache to an empty temp dir — no DB file exists → early return.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	cmd := cacheClearCmd() // any cmd with registered flags will do
	err := runCacheStats(cmd, nil)
	if err != nil {
		t.Fatalf("runCacheStats with no DB: unexpected error: %v", err)
	}
}

func TestRunCacheStats_WithDB(t *testing.T) {
	// Redirect cache to a fresh temp dir — cache.Open will create a new DB.
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	cmd := cacheStatsCmd()
	err := runCacheStats(cmd, nil)
	if err != nil {
		t.Fatalf("runCacheStats with fresh DB: unexpected error: %v", err)
	}
}

func TestRunCacheClear_NoDB(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	cmd := cacheClearCmd()
	err := runCacheClear(cmd, nil)
	if err != nil {
		t.Fatalf("runCacheClear with no DB: unexpected error: %v", err)
	}
}

func TestRunCacheClear_WithDB(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	cmd := cacheClearCmd()
	err := runCacheClear(cmd, nil)
	if err != nil {
		t.Fatalf("runCacheClear with empty DB: unexpected error: %v", err)
	}
}

func TestRunCacheClear_WithSource(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	cmd := cacheClearCmd()
	if err := cmd.Flags().Set("source", "jira"); err != nil {
		t.Fatalf("Set source: %v", err)
	}
	if err := runCacheClear(cmd, nil); err != nil {
		t.Fatalf("runCacheClear --source jira: %v", err)
	}
}

func TestRunCacheClear_OlderThan(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	cmd := cacheClearCmd()
	if err := cmd.Flags().Set("older-than", "7d"); err != nil {
		t.Fatalf("Set older-than: %v", err)
	}
	if err := runCacheClear(cmd, nil); err != nil {
		t.Fatalf("runCacheClear --older-than 7d: %v", err)
	}
}

func TestRunCacheClear_InvalidOlderThan(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	cmd := cacheClearCmd()
	if err := cmd.Flags().Set("older-than", "bogus"); err != nil {
		t.Fatalf("Set older-than: %v", err)
	}
	if err := runCacheClear(cmd, nil); err == nil {
		t.Error("runCacheClear --older-than bogus: expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// FetchTrends — multi-period with all sources disabled (no API calls)
// ---------------------------------------------------------------------------

func TestFetchTrends_AllSourcesDisabled(t *testing.T) {
	rc := &config.ResolvedConfig{
		GitHubUser: "testuser",
		StartDate:  "2025-01-01",
		EndDate:    "2025-01-31",
		Jira:       false,
		Confluence: false,
		GitHub:     false,
	}
	periods := []Period{
		{Label: "P1", Start: "2025-01-01", End: "2025-01-31"},
		{Label: "P2", Start: "2025-02-01", End: "2025-02-28"},
	}

	ctx := context.Background()
	ts, err := FetchTrends(ctx, rc, periods)
	if err != nil {
		t.Fatalf("FetchTrends: unexpected error: %v", err)
	}
	if ts == nil {
		t.Fatal("expected non-nil TrendSet")
	}
	if len(ts.Periods) != 2 {
		t.Errorf("TrendSet.Periods len = %d, want 2", len(ts.Periods))
	}
	// Verify period labels are applied
	if ts.Periods[0].Period != "P1" {
		t.Errorf("Periods[0].Period = %q, want P1", ts.Periods[0].Period)
	}
}

// ---------------------------------------------------------------------------
// versionCmd RunE
// ---------------------------------------------------------------------------

func TestVersionCmd_RunE(t *testing.T) {
	cmd := versionCmd()
	// Execute the RunE directly (bypasses PersistentPreRunE which needs config).
	err := cmd.RunE(cmd, nil)
	if err != nil {
		t.Fatalf("versionCmd RunE: unexpected error: %v", err)
	}
}

func TestVersionCmd_RunE_JSON(t *testing.T) {
	cmd := versionCmd()
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("Set json: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err != nil {
		t.Fatalf("versionCmd RunE --json: %v", err)
	}
}

// ---------------------------------------------------------------------------
// initCmd RunE — redirected via XDG_CONFIG_HOME
// ---------------------------------------------------------------------------

func TestInitCmd_RunE_CreatesConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cmd := initCmd()
	err := cmd.RunE(cmd, nil)
	if err != nil {
		t.Fatalf("initCmd RunE: unexpected error: %v", err)
	}
}

func TestInitCmd_RunE_AlreadyExists(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cmd := initCmd()
	// First run creates it.
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("first initCmd RunE: %v", err)
	}
	// Second run without --force should error.
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Error("second initCmd RunE: expected error (already exists), got nil")
	}
}

func TestInitCmd_RunE_Force(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cmd := initCmd()
	// Create first.
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("first initCmd RunE: %v", err)
	}
	// Re-run with --force.
	if err := cmd.Flags().Set("force", "true"); err != nil {
		t.Fatalf("Set force: %v", err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("initCmd RunE --force: %v", err)
	}
}
