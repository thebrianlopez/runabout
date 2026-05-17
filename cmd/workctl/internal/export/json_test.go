package export

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// ---------------------------------------------------------------------------
// modeToString
// ---------------------------------------------------------------------------

func TestModeToString(t *testing.T) {
	tests := []struct {
		mode models.QueryMode
		want string
	}{
		{models.UserMode, "UserMode"},
		{models.ProjectMode, "ProjectMode"},
		{models.SpaceMode, "SpaceMode"},
		{models.MixedMode, "MixedMode"},
		{models.GitHubMode, "GitHubMode"},
		{models.QueryMode(999), "Unknown"},
	}
	for _, tt := range tests {
		got := modeToString(tt.mode)
		if got != tt.want {
			t.Errorf("modeToString(%v) = %q, want %q", tt.mode, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// countRecords
// ---------------------------------------------------------------------------

func TestCountRecords(t *testing.T) {
	tests := []struct {
		name string
		data interface{}
		want int
	}{
		{"nil issues", []models.Issue(nil), 0},
		{"empty issues", []models.Issue{}, 0},
		{"two issues", []models.Issue{{}, {}}, 2},
		{"three articles", []models.ConfluenceArticle{{}, {}, {}}, 3},
		{"one github activity", []models.GitHubActivity{{}}, 1},
		{"unknown type returns 0", "not-a-slice", 0},
		{"int type returns 0", 42, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countRecords(tt.data)
			if got != tt.want {
				t.Errorf("countRecords(%T) = %d, want %d", tt.data, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// WriteToJSON
// ---------------------------------------------------------------------------

func TestWriteToJSON(t *testing.T) {
	cfg := &models.QueryConfig{
		Mode:      models.ProjectMode,
		StartDate: "2025-01-01",
		EndDate:   "2025-12-31",
		TimeZone:  "UTC",
	}

	t.Run("writes issues and round-trips correctly", func(t *testing.T) {
		dir := t.TempDir()
		outPath := filepath.Join(dir, "jira.json")

		issues := []models.Issue{
			{Key: "SR-1"},
			{Key: "SR-2"},
		}

		if err := WriteToJSON(issues, outPath, cfg); err != nil {
			t.Fatalf("WriteToJSON() error = %v", err)
		}

		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}

		var out JSONOutput
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}

		if out.Count != 2 {
			t.Errorf("Count = %d, want 2", out.Count)
		}
		if out.Metadata.Query.Mode != "ProjectMode" {
			t.Errorf("Mode = %q, want ProjectMode", out.Metadata.Query.Mode)
		}
		if out.Metadata.Query.StartDate != "2025-01-01" {
			t.Errorf("StartDate = %q, want 2025-01-01", out.Metadata.Query.StartDate)
		}
		if out.Metadata.Execution.Timestamp == "" {
			t.Error("Timestamp should not be empty")
		}
	})

	t.Run("writes confluence articles", func(t *testing.T) {
		dir := t.TempDir()
		outPath := filepath.Join(dir, "confluence.json")

		articles := []models.ConfluenceArticle{{Title: "Doc 1"}, {Title: "Doc 2"}, {Title: "Doc 3"}}

		if err := WriteToJSON(articles, outPath, cfg); err != nil {
			t.Fatalf("WriteToJSON() error = %v", err)
		}

		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}

		var out JSONOutput
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}

		if out.Count != 3 {
			t.Errorf("Count = %d, want 3", out.Count)
		}
	})

	t.Run("writes github activities", func(t *testing.T) {
		dir := t.TempDir()
		outPath := filepath.Join(dir, "github.json")

		activities := []models.GitHubActivity{{EventType: "PullRequestEvent"}}

		if err := WriteToJSON(activities, outPath, cfg); err != nil {
			t.Fatalf("WriteToJSON() error = %v", err)
		}

		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}

		var out JSONOutput
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}

		if out.Count != 1 {
			t.Errorf("Count = %d, want 1", out.Count)
		}
	})

	t.Run("fails on bad path", func(t *testing.T) {
		err := WriteToJSON([]models.Issue{}, "/nonexistent/dir/file.json", cfg)
		if err == nil {
			t.Error("WriteToJSON() expected error for bad path, got nil")
		}
	})
}
