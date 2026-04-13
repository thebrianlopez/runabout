package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/export"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

// ParsedExport holds deserialized data from a single JSON export file.
type ParsedExport struct {
	Source   Source
	Metadata export.Metadata

	Jira       []models.Issue
	Confluence []models.ConfluenceArticle
	GitHub     []models.GitHubActivity
}

// jiraJSONOutput is the typed deserialization target for Jira exports.
type jiraJSONOutput struct {
	Metadata export.Metadata `json:"metadata"`
	Data     []models.Issue  `json:"data"`
	Count    int             `json:"count"`
}

// confluenceJSONOutput is the typed deserialization target for Confluence exports.
type confluenceJSONOutput struct {
	Metadata export.Metadata            `json:"metadata"`
	Data     []models.ConfluenceArticle `json:"data"`
	Count    int                        `json:"count"`
}

// githubJSONOutput is the typed deserialization target for GitHub exports.
type githubJSONOutput struct {
	Metadata export.Metadata         `json:"metadata"`
	Data     []models.GitHubActivity `json:"data"`
	Count    int                     `json:"count"`
}

// parseJiraExport reads and deserializes a Jira JSON export file.
func parseJiraExport(path string) (*ParsedExport, error) {
	var out jiraJSONOutput
	if err := readJSONFile(path, &out); err != nil {
		return nil, fmt.Errorf("parsing jira export: %w", err)
	}
	return &ParsedExport{
		Source:   SourceJira,
		Metadata: out.Metadata,
		Jira:     out.Data,
	}, nil
}

// parseConfluenceExport reads and deserializes a Confluence JSON export file.
func parseConfluenceExport(path string) (*ParsedExport, error) {
	var out confluenceJSONOutput
	if err := readJSONFile(path, &out); err != nil {
		return nil, fmt.Errorf("parsing confluence export: %w", err)
	}
	return &ParsedExport{
		Source:     SourceConfluence,
		Metadata:   out.Metadata,
		Confluence: out.Data,
	}, nil
}

// parseGitHubExport reads and deserializes a GitHub JSON export file.
func parseGitHubExport(path string) (*ParsedExport, error) {
	var out githubJSONOutput
	if err := readJSONFile(path, &out); err != nil {
		return nil, fmt.Errorf("parsing github export: %w", err)
	}
	return &ParsedExport{
		Source:   SourceGitHub,
		Metadata: out.Metadata,
		GitHub:   out.Data,
	}, nil
}

// readJSONFile reads a file and unmarshals its contents into target.
func readJSONFile(path string, target interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("unmarshaling %s: %w", path, err)
	}
	return nil
}

// fileExists returns true if the file at path exists and is not a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

const dateFmt = "2006-01-02"

// extractDateRange parses start/end date strings from export metadata.
func extractDateRange(metadata export.Metadata) (DateRange, error) {
	start, err := time.Parse(dateFmt, metadata.Query.StartDate)
	if err != nil {
		return DateRange{}, fmt.Errorf("parsing start date %q: %w", metadata.Query.StartDate, err)
	}
	end, err := time.Parse(dateFmt, metadata.Query.EndDate)
	if err != nil {
		return DateRange{}, fmt.Errorf("parsing end date %q: %w", metadata.Query.EndDate, err)
	}
	return DateRange{Start: start, End: end}, nil
}
