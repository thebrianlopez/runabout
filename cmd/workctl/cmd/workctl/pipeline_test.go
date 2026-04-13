package main

import (
	"context"
	"strings"
	"testing"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/config"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/insights"
)

// TestFetchReportData_AllServicesDisabled verifies that FetchReportData returns a
// valid ReportData with non-nil Signals when all fetch sources are disabled.
// No API calls are made; fetchDataForPeriod returns empty slices.
func TestFetchReportData_AllServicesDisabled(t *testing.T) {
	rc := &config.ResolvedConfig{
		GitHubUser: "testuser", // required to satisfy DetermineQueryMode
		StartDate:  "2025-01-01",
		EndDate:    "2025-01-31",
		Jira:       false,
		Confluence: false,
		GitHub:     false,
	}

	ctx := context.Background()
	rd, err := FetchReportData(ctx, rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rd == nil {
		t.Fatal("expected non-nil ReportData")
	}
	if rd.Signals == nil {
		t.Fatal("expected non-nil Signals")
	}
	if rd.Generated.IsZero() {
		t.Fatal("expected non-zero Generated time")
	}
}

// TestFetchReportData_PeriodFormatting verifies that Period is set from
// rc.StartDate/EndDate via insights.FormatPeriod.
func TestFetchReportData_PeriodFormatting(t *testing.T) {
	rc := &config.ResolvedConfig{
		GitHubUser: "testuser",
		StartDate:  "2025-03-01",
		EndDate:    "2025-03-31",
		GitHub:     false,
	}

	ctx := context.Background()
	rd, err := FetchReportData(ctx, rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := insights.FormatPeriod("2025-03-01", "2025-03-31")
	if rd.Period != want {
		t.Errorf("Period = %q, want %q", rd.Period, want)
	}
}

// TestFetchReportData_NoQueryMode verifies that FetchReportData returns an error
// when no query mode can be determined (no email, project-keys, space-keys, github-user).
func TestFetchReportData_NoQueryMode(t *testing.T) {
	rc := &config.ResolvedConfig{
		StartDate: "2025-01-01",
		EndDate:   "2025-01-31",
		// Email, GitHubUser, ProjectKeys, SpaceKeys all empty
	}

	ctx := context.Background()
	_, err := FetchReportData(ctx, rc)
	if err == nil {
		t.Fatal("expected error when no query mode configured")
	}
	if !strings.Contains(err.Error(), "must specify") {
		t.Errorf("error = %q, want to contain 'must specify'", err.Error())
	}
}

// TestFetchReportData_LocalSignalsNilWhenDisabled verifies that Signals.ShellActivity
// and Signals.AIActivity are nil when --shell=false and --ai-stats=false.
func TestFetchReportData_LocalSignalsNilWhenDisabled(t *testing.T) {
	rc := &config.ResolvedConfig{
		GitHubUser: "testuser",
		StartDate:  "2025-01-01",
		EndDate:    "2025-01-31",
		Jira:       false,
		Confluence: false,
		GitHub:     false,
		Shell:      false,
		AIStats:    false,
	}

	ctx := context.Background()
	rd, err := FetchReportData(ctx, rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rd.Signals.ShellActivity != nil {
		t.Error("expected ShellActivity to be nil when Shell=false")
	}
	if rd.Signals.AIActivity != nil {
		t.Error("expected AIActivity to be nil when AIStats=false")
	}
}

// TestFetchReportData_LocalSignalsSetWhenEnabled verifies that Signals.ShellActivity
// and Signals.AIActivity are non-nil when --shell=true and --ai-stats=true,
// even when no local files exist (graceful empty).
func TestFetchReportData_LocalSignalsSetWhenEnabled(t *testing.T) {
	rc := &config.ResolvedConfig{
		GitHubUser: "testuser",
		StartDate:  "2025-01-01",
		EndDate:    "2025-01-31",
		Jira:       false,
		Confluence: false,
		GitHub:     false,
		Shell:      true,
		AIStats:    true,
	}

	ctx := context.Background()
	rd, err := FetchReportData(ctx, rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rd.Signals.ShellActivity == nil {
		t.Error("expected ShellActivity to be non-nil when Shell=true")
	}
	if rd.Signals.AIActivity == nil {
		t.Error("expected AIActivity to be non-nil when AIStats=true")
	}
}
