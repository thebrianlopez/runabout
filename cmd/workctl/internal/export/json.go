package export

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// Metadata wraps all metadata for JSON output
type Metadata struct {
	Query     QueryMetadata     `json:"query"`
	Execution ExecutionMetadata `json:"execution"`
	Filters   FiltersMetadata   `json:"filters,omitempty"`
}

// QueryMetadata contains query configuration details
type QueryMetadata struct {
	Mode         string   `json:"mode"`
	Email        string   `json:"email,omitempty"`
	ProjectKeys  []string `json:"project_keys,omitempty"`
	SpaceKeys    []string `json:"space_keys,omitempty"`
	GitHubUser   string   `json:"github_user,omitempty"`
	GitHubRepos  []string `json:"github_repos,omitempty"`
	GitHubEnrich bool     `json:"github_enrich,omitempty"`
	StartDate    string   `json:"start_date"`
	EndDate      string   `json:"end_date"`
	TimeZone     string   `json:"timezone"`
}

// ExecutionMetadata contains execution context
type ExecutionMetadata struct {
	Timestamp         string `json:"timestamp"`                     // ISO 8601 timestamp
	WorkctlVersion    string `json:"workctl_version"`               // Build version
	GitHubAPIStrategy string `json:"github_api_strategy,omitempty"` // API strategy used
}

// FiltersMetadata contains optional filter configuration
type FiltersMetadata struct {
	JiraStatus        []string `json:"jira_status,omitempty"`
	JiraType          []string `json:"jira_type,omitempty"`
	JiraPriority      []string `json:"jira_priority,omitempty"`
	ConfluenceType    string   `json:"confluence_type,omitempty"`
	ConfluenceHydrate bool     `json:"confluence_hydrate,omitempty"`
	GitHubAPIStrategy string   `json:"github_api_strategy,omitempty"`
}

// JSONOutput wraps data with metadata for JSON export
type JSONOutput struct {
	Metadata Metadata    `json:"metadata"`
	Data     interface{} `json:"data"`
	Count    int         `json:"count"`
}

// WriteToJSON writes data to a JSON file with metadata wrapper
func WriteToJSON(data interface{}, filePath string, cfg *models.QueryConfig) error {
	// Build metadata from config
	metadata := Metadata{
		Query: QueryMetadata{
			Mode:         modeToString(cfg.Mode),
			Email:        cfg.Email,
			ProjectKeys:  cfg.ProjectKeys,
			SpaceKeys:    cfg.SpaceKeys,
			GitHubUser:   cfg.GitHubUser,
			GitHubRepos:  cfg.GitHubRepos,
			GitHubEnrich: cfg.GitHubEnrich,
			StartDate:    cfg.StartDate,
			EndDate:      cfg.EndDate,
			TimeZone:     cfg.TimeZone,
		},
		Execution: ExecutionMetadata{
			Timestamp:         time.Now().UTC().Format(time.RFC3339),
			WorkctlVersion:    "1.0.0", // TODO: Inject via ldflags at build time
			GitHubAPIStrategy: cfg.GitHubAPIStrategy,
		},
		Filters: FiltersMetadata{
			JiraStatus:        cfg.JiraStatus,
			JiraType:          cfg.JiraType,
			JiraPriority:      cfg.JiraPriority,
			ConfluenceType:    cfg.ConfluenceType,
			ConfluenceHydrate: cfg.ConfluenceHydrate,
			GitHubAPIStrategy: cfg.GitHubAPIStrategy,
		},
	}

	// Count records based on data type
	count := countRecords(data)

	// Create JSON output wrapper
	output := JSONOutput{
		Metadata: metadata,
		Data:     data,
		Count:    count,
	}

	// Create output file with owner-only permissions (contains PII)
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create JSON file: %w", err)
	}
	defer file.Close()

	// Use JSON encoder with pretty printing
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")

	// Write JSON to file
	if err := encoder.Encode(output); err != nil {
		return fmt.Errorf("failed to encode JSON: %w", err)
	}

	return nil
}

// modeToString converts QueryMode to string for JSON output
func modeToString(mode models.QueryMode) string {
	switch mode {
	case models.UserMode:
		return "UserMode"
	case models.ProjectMode:
		return "ProjectMode"
	case models.SpaceMode:
		return "SpaceMode"
	case models.MixedMode:
		return "MixedMode"
	case models.GitHubMode:
		return "GitHubMode"
	default:
		return "Unknown"
	}
}

// countRecords returns the count of records in the data slice
func countRecords(data interface{}) int {
	switch v := data.(type) {
	case []models.Issue:
		return len(v)
	case []models.ConfluenceArticle:
		return len(v)
	case []models.GitHubActivity:
		return len(v)
	default:
		return 0
	}
}
