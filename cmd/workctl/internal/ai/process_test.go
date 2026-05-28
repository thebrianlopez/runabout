package ai

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcessEndToEnd(t *testing.T) {
	dir := t.TempDir()
	jiraPath := filepath.Join(dir, "jira.json")
	confPath := filepath.Join(dir, "confluence.json")
	ghPath := filepath.Join(dir, "github.json")

	if err := os.WriteFile(jiraPath, []byte(jiraFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(confPath, []byte(confluenceFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ghPath, []byte(githubFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Process(ProcessOptions{
		JiraPath:       jiraPath,
		ConfluencePath: confPath,
		GitHubPath:     ghPath,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	// Identity resolved from metadata
	if result.Identity.Email != "alice@example.com" {
		t.Errorf("email = %q, want alice@example.com", result.Identity.Email)
	}
	if result.Identity.GitHubLogin != "alice-gh" {
		t.Errorf("github = %q, want alice-gh", result.Identity.GitHubLogin)
	}

	// Timeline has entries from all 3 sources
	if len(result.Timeline.Entries) != 3 {
		t.Errorf("timeline entries = %d, want 3", len(result.Timeline.Entries))
	}
	if len(result.Timeline.Sources) != 3 {
		t.Errorf("timeline sources = %d, want 3", len(result.Timeline.Sources))
	}

	// Metrics present for all sources
	if result.Metrics.Jira == nil {
		t.Error("expected non-nil Jira metrics")
	}
	if result.Metrics.Confluence == nil {
		t.Error("expected non-nil Confluence metrics")
	}
	if result.Metrics.GitHub == nil {
		t.Error("expected non-nil GitHub metrics")
	}

	// Data quality: all sources present
	if result.Metrics.DataQuality.PartialData {
		t.Error("expected PartialData=false with all sources")
	}
}

func TestProcessSingleSource(t *testing.T) {
	dir := t.TempDir()
	jiraPath := filepath.Join(dir, "jira.json")
	if err := os.WriteFile(jiraPath, []byte(jiraFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Process(ProcessOptions{
		JiraPath: jiraPath,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if result.Metrics.Jira == nil {
		t.Error("expected Jira metrics")
	}
	if result.Metrics.Confluence != nil {
		t.Error("expected nil Confluence metrics")
	}
	if result.Metrics.GitHub != nil {
		t.Error("expected nil GitHub metrics")
	}
	if !result.Metrics.DataQuality.PartialData {
		t.Error("expected PartialData=true with single source")
	}
}

func TestProcessNoSources(t *testing.T) {
	_, err := Process(ProcessOptions{})
	if err == nil {
		t.Fatal("expected error for no sources")
	}
}

func TestProcessMissingFilesSkipped(t *testing.T) {
	dir := t.TempDir()
	jiraPath := filepath.Join(dir, "jira.json")
	if err := os.WriteFile(jiraPath, []byte(jiraFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	// Confluence and GitHub paths point to nonexistent files
	result, err := Process(ProcessOptions{
		JiraPath:       jiraPath,
		ConfluencePath: filepath.Join(dir, "missing_confluence.json"),
		GitHubPath:     filepath.Join(dir, "missing_github.json"),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if result.Metrics.Jira == nil {
		t.Error("expected Jira metrics from available file")
	}
	if result.Metrics.Confluence != nil || result.Metrics.GitHub != nil {
		t.Error("expected nil metrics for missing files")
	}
}

func TestProcessIdentityFromOptions(t *testing.T) {
	dir := t.TempDir()
	jiraPath := filepath.Join(dir, "jira.json")
	if err := os.WriteFile(jiraPath, []byte(jiraFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Process(ProcessOptions{
		JiraPath: jiraPath,
		Identity: &UserIdentity{
			Email:       "override@example.com",
			DisplayName: "Override User",
		},
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if result.Identity.Email != "override@example.com" {
		t.Errorf("email = %q, want override@example.com", result.Identity.Email)
	}
	if result.Identity.DisplayName != "Override User" {
		t.Errorf("name = %q, want Override User", result.Identity.DisplayName)
	}
}

func TestProcessInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, []byte("{invalid}"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Process(ProcessOptions{JiraPath: badPath})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
