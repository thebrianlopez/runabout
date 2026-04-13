package api

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// testdataDir returns the absolute path to the testdata directory.
func testdataDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata")
}

// LoadFixture reads a testdata fixture file and returns its contents as a string.
// Fixtures are stored in internal/api/testdata/.
func LoadFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(testdataDir(), name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("LoadFixture(%q): %v", name, err)
	}
	return string(data)
}

// TestFixture groups pre-built testdata filenames for common API response shapes.
var TestFixture = struct {
	JiraIssues      string
	ConfluencePages string
	GitHubEvents    string
}{
	JiraIssues:      "jira_issues.json",
	ConfluencePages: "confluence_pages.json",
	GitHubEvents:    "github_events.json",
}
