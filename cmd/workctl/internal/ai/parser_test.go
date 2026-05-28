package ai

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture %s: %v", name, err)
	}
	return path
}

// Fixtures use PascalCase keys to match Go struct field names (no JSON tags on models).
const jiraFixture = `{
  "metadata": {
    "query": {"mode":"UserMode","email":"alice@example.com","start_date":"2025-01-01","end_date":"2025-06-30","timezone":"UTC"},
    "execution": {"timestamp":"2025-07-01T00:00:00Z","workctl_version":"1.0.0"}
  },
  "data": [
    {
      "ID":"10001","Key":"PROJ-1","URL":"https://jira.example.com/browse/PROJ-1",
      "ProjectKey":"PROJ","Assignee":"Alice","IssueType":"Story",
      "Fields":{"Summary":"Build feature","Created":"2025-01-15T10:00:00.000+0000","Updated":"2025-02-01T12:00:00.000+0000","Resolved":"2025-02-01T12:00:00.000+0000","Status":{"Name":"Done"}}
    }
  ],
  "count": 1
}`

const confluenceFixture = `{
  "metadata": {
    "query": {"mode":"UserMode","email":"alice@example.com","start_date":"2025-01-01","end_date":"2025-06-30","timezone":"UTC"},
    "execution": {"timestamp":"2025-07-01T00:00:00Z","workctl_version":"1.0.0"}
  },
  "data": [
    {
      "ID":"20001","Title":"Design Doc","URL":"https://wiki.example.com/pages/20001",
      "SpaceKey":"ENG","SpaceName":"Engineering","Creator":"Alice",
      "CreatedDate":"2025-03-01T09:00:00.000Z","LastModifiedDate":"2025-03-15T14:00:00.000Z"
    }
  ],
  "count": 1
}`

const githubFixture = `{
  "metadata": {
    "query": {"mode":"GitHubMode","github_user":"alice-gh","start_date":"2025-01-01","end_date":"2025-06-30","timezone":"UTC"},
    "execution": {"timestamp":"2025-07-01T00:00:00Z","workctl_version":"1.0.0","github_api_strategy":"auto"}
  },
  "data": [
    {
      "EventID":"E1","EventType":"PullRequestEvent","ActorLogin":"alice-gh",
      "Repository":"org/repo","Timestamp":"2025-04-10T16:00:00Z",
      "Description":"Opened PR #42","URL":"https://github.com/org/repo/pull/42","Public":true
    }
  ],
  "count": 1
}`

func TestParseJiraExport(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "jira.json", jiraFixture)

	exp, err := parseJiraExport(path)
	if err != nil {
		t.Fatalf("parseJiraExport: %v", err)
	}
	if exp.Source != SourceJira {
		t.Errorf("source = %q, want %q", exp.Source, SourceJira)
	}
	if len(exp.Jira) != 1 {
		t.Fatalf("len(Jira) = %d, want 1", len(exp.Jira))
	}
	if exp.Jira[0].Key != "PROJ-1" {
		t.Errorf("issue key = %q, want PROJ-1", exp.Jira[0].Key)
	}
	if exp.Metadata.Query.Email != "alice@example.com" {
		t.Errorf("metadata email = %q, want alice@example.com", exp.Metadata.Query.Email)
	}
}

func TestParseConfluenceExport(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "confluence.json", confluenceFixture)

	exp, err := parseConfluenceExport(path)
	if err != nil {
		t.Fatalf("parseConfluenceExport: %v", err)
	}
	if exp.Source != SourceConfluence {
		t.Errorf("source = %q, want %q", exp.Source, SourceConfluence)
	}
	if len(exp.Confluence) != 1 {
		t.Fatalf("len(Confluence) = %d, want 1", len(exp.Confluence))
	}
	if exp.Confluence[0].Title != "Design Doc" {
		t.Errorf("title = %q, want Design Doc", exp.Confluence[0].Title)
	}
}

func TestParseGitHubExport(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "github.json", githubFixture)

	exp, err := parseGitHubExport(path)
	if err != nil {
		t.Fatalf("parseGitHubExport: %v", err)
	}
	if exp.Source != SourceGitHub {
		t.Errorf("source = %q, want %q", exp.Source, SourceGitHub)
	}
	if len(exp.GitHub) != 1 {
		t.Fatalf("len(GitHub) = %d, want 1", len(exp.GitHub))
	}
	if exp.GitHub[0].ActorLogin != "alice-gh" {
		t.Errorf("actor = %q, want alice-gh", exp.GitHub[0].ActorLogin)
	}
}

func TestParseMissingFile(t *testing.T) {
	_, err := parseJiraExport("/nonexistent/jira.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "bad.json", `{not valid json}`)

	_, err := parseJiraExport(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseEmptyData(t *testing.T) {
	dir := t.TempDir()
	content := `{"metadata":{"query":{"start_date":"2025-01-01","end_date":"2025-06-30","timezone":"UTC"},"execution":{"timestamp":"2025-07-01T00:00:00Z","workctl_version":"1.0.0"}},"data":[],"count":0}`
	path := writeFixture(t, dir, "empty.json", content)

	exp, err := parseJiraExport(path)
	if err != nil {
		t.Fatalf("parseJiraExport empty: %v", err)
	}
	if len(exp.Jira) != 0 {
		t.Errorf("len(Jira) = %d, want 0", len(exp.Jira))
	}
}

func TestExtractDateRange(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "jira.json", jiraFixture)

	exp, err := parseJiraExport(path)
	if err != nil {
		t.Fatalf("parseJiraExport: %v", err)
	}

	dr, err := extractDateRange(exp.Metadata)
	if err != nil {
		t.Fatalf("extractDateRange: %v", err)
	}
	if dr.Start.Year() != 2025 || dr.Start.Month() != 1 || dr.Start.Day() != 1 {
		t.Errorf("start = %v, want 2025-01-01", dr.Start)
	}
	if dr.End.Year() != 2025 || dr.End.Month() != 6 || dr.End.Day() != 30 {
		t.Errorf("end = %v, want 2025-06-30", dr.End)
	}
}

func TestExtractDateRangeInvalid(t *testing.T) {
	dir := t.TempDir()
	content := `{"metadata":{"query":{"start_date":"not-a-date","end_date":"2025-06-30","timezone":"UTC"},"execution":{"timestamp":"2025-07-01T00:00:00Z","workctl_version":"1.0.0"}},"data":[],"count":0}`
	path := writeFixture(t, dir, "bad_date.json", content)

	exp, err := parseJiraExport(path)
	if err != nil {
		t.Fatalf("parseJiraExport: %v", err)
	}

	_, err = extractDateRange(exp.Metadata)
	if err == nil {
		t.Fatal("expected error for invalid date")
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "exists.json", "{}")

	if !fileExists(path) {
		t.Error("fileExists returned false for existing file")
	}
	if fileExists(filepath.Join(dir, "nope.json")) {
		t.Error("fileExists returned true for missing file")
	}
	if fileExists(dir) {
		t.Error("fileExists returned true for directory")
	}
}
