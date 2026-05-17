package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// ClaudeStatsClient reads activity data from ~/.claude/stats-cache.json.
//
// Deprecated: EventsClient provides richer session-level data for post-2026-01-23
// date ranges. ClaudeStatsClient is retained as a fallback when the events store
// has no session summaries for the requested period (pre-events-era data).
type ClaudeStatsClient struct {
	statsPath string
}

// NewClaudeStatsClient returns a client reading from the default stats cache path.
func NewClaudeStatsClient() *ClaudeStatsClient {
	home, _ := os.UserHomeDir()
	return &ClaudeStatsClient{
		statsPath: filepath.Join(home, ".claude", "stats-cache.json"),
	}
}

// newClaudeStatsClientAt returns a client reading from a custom path (for testing).
func newClaudeStatsClientAt(path string) *ClaudeStatsClient {
	return &ClaudeStatsClient{statsPath: path}
}

// statsCache mirrors the on-disk JSON structure of ~/.claude/stats-cache.json.
type statsCache struct {
	Version          int                     `json:"version"`
	LastComputedDate string                  `json:"lastComputedDate"`
	DailyActivity    []dailyActivityEntry    `json:"dailyActivity"`
	DailyModelTokens []dailyModelTokensEntry `json:"dailyModelTokens"`
}

type dailyActivityEntry struct {
	Date          string `json:"date"`
	MessageCount  int    `json:"messageCount"`
	SessionCount  int    `json:"sessionCount"`
	ToolCallCount int    `json:"toolCallCount"`
}

type dailyModelTokensEntry struct {
	Date          string         `json:"date"`
	TokensByModel map[string]int `json:"tokensByModel"`
}

// GetActivity returns AI activity records for [startDate, endDate] (YYYY-MM-DD).
// Returns an empty slice (not an error) when the stats file is absent or malformed (NF4).
func (c *ClaudeStatsClient) GetActivity(startDate, endDate string) ([]models.AIActivity, error) {
	data, err := os.ReadFile(c.statsPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read stats cache: %w", err)
	}

	var sc statsCache
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, nil // NF4: malformed JSON → return empty, no error
	}

	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return nil, fmt.Errorf("invalid start date %q: %w", startDate, err)
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return nil, fmt.Errorf("invalid end date %q: %w", endDate, err)
	}

	// Build token totals by date (sum across all models).
	tokensByDate := make(map[string]int, len(sc.DailyModelTokens))
	for _, entry := range sc.DailyModelTokens {
		for _, count := range entry.TokensByModel {
			tokensByDate[entry.Date] += count
		}
	}

	// Filter dailyActivity by date range and join with token totals.
	var result []models.AIActivity
	for _, entry := range sc.DailyActivity {
		t, err := time.Parse("2006-01-02", entry.Date)
		if err != nil {
			continue
		}
		if t.Before(start) || t.After(end) {
			continue
		}
		result = append(result, models.AIActivity{
			Date:          entry.Date,
			MessageCount:  entry.MessageCount,
			SessionCount:  entry.SessionCount,
			ToolCallCount: entry.ToolCallCount,
			TokensUsed:    tokensByDate[entry.Date],
		})
	}
	return result, nil
}
