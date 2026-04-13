package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGetActivity_FileAbsent(t *testing.T) {
	c := newClaudeStatsClientAt("/nonexistent/stats-cache.json")
	activity, err := c.GetActivity("2026-01-01", "2026-01-07")
	if err != nil {
		t.Errorf("GetActivity should not error on absent file, got: %v", err)
	}
	if len(activity) != 0 {
		t.Errorf("GetActivity should return empty slice on absent file, got %d entries", len(activity))
	}
}

func TestGetActivity_MalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats-cache.json")
	if err := os.WriteFile(path, []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	c := newClaudeStatsClientAt(path)
	activity, err := c.GetActivity("2026-01-01", "2026-01-07")
	if err != nil {
		t.Errorf("GetActivity should not error on malformed JSON (NF4), got: %v", err)
	}
	if len(activity) != 0 {
		t.Errorf("GetActivity should return empty slice on malformed JSON, got %d entries", len(activity))
	}
}

func TestGetActivity_Normal(t *testing.T) {
	cache := statsCache{
		Version:          2,
		LastComputedDate: "2026-02-16",
		DailyActivity: []dailyActivityEntry{
			{Date: "2026-02-09", MessageCount: 100, SessionCount: 5, ToolCallCount: 50},
			{Date: "2026-02-10", MessageCount: 200, SessionCount: 8, ToolCallCount: 80},
			{Date: "2026-02-11", MessageCount: 150, SessionCount: 6, ToolCallCount: 60},
		},
		DailyModelTokens: []dailyModelTokensEntry{
			{Date: "2026-02-09", TokensByModel: map[string]int{
				"claude-sonnet-4-5": 38234,
				"claude-haiku-4-5":  5000,
			}},
			{Date: "2026-02-10", TokensByModel: map[string]int{
				"claude-sonnet-4-5": 42000,
			}},
			// 2026-02-11 has no token entry → TokensUsed should be 0
		},
	}

	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatalf("marshal test data: %v", err)
	}
	path := filepath.Join(t.TempDir(), "stats-cache.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	c := newClaudeStatsClientAt(path)
	activity, err := c.GetActivity("2026-02-09", "2026-02-10")
	if err != nil {
		t.Fatalf("GetActivity error: %v", err)
	}
	if len(activity) != 2 {
		t.Fatalf("len(activity) = %d, want 2", len(activity))
	}

	// Verify 2026-02-09
	a0 := activity[0]
	if a0.Date != "2026-02-09" {
		t.Errorf("activity[0].Date = %q, want 2026-02-09", a0.Date)
	}
	if a0.MessageCount != 100 {
		t.Errorf("activity[0].MessageCount = %d, want 100", a0.MessageCount)
	}
	if a0.SessionCount != 5 {
		t.Errorf("activity[0].SessionCount = %d, want 5", a0.SessionCount)
	}
	if a0.ToolCallCount != 50 {
		t.Errorf("activity[0].ToolCallCount = %d, want 50", a0.ToolCallCount)
	}
	// Tokens: 38234 + 5000 = 43234
	if a0.TokensUsed != 43234 {
		t.Errorf("activity[0].TokensUsed = %d, want 43234", a0.TokensUsed)
	}

	// Verify 2026-02-10
	a1 := activity[1]
	if a1.TokensUsed != 42000 {
		t.Errorf("activity[1].TokensUsed = %d, want 42000", a1.TokensUsed)
	}
}

func TestGetActivity_DateFiltering(t *testing.T) {
	cache := statsCache{
		DailyActivity: []dailyActivityEntry{
			{Date: "2026-02-01", MessageCount: 10},
			{Date: "2026-02-05", MessageCount: 20},
			{Date: "2026-02-10", MessageCount: 30},
			{Date: "2026-02-15", MessageCount: 40},
		},
	}

	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatalf("marshal test data: %v", err)
	}
	path := filepath.Join(t.TempDir(), "stats-cache.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	c := newClaudeStatsClientAt(path)
	activity, err := c.GetActivity("2026-02-05", "2026-02-10")
	if err != nil {
		t.Fatalf("GetActivity error: %v", err)
	}
	// Only 2026-02-05 and 2026-02-10 should be included
	if len(activity) != 2 {
		t.Fatalf("len(activity) = %d, want 2", len(activity))
	}
	if activity[0].Date != "2026-02-05" {
		t.Errorf("activity[0].Date = %q, want 2026-02-05", activity[0].Date)
	}
	if activity[1].Date != "2026-02-10" {
		t.Errorf("activity[1].Date = %q, want 2026-02-10", activity[1].Date)
	}
}

func TestGetActivity_MissingTokenEntry(t *testing.T) {
	// Day with activity but no token data → TokensUsed should be 0
	cache := statsCache{
		DailyActivity: []dailyActivityEntry{
			{Date: "2026-02-09", MessageCount: 100, SessionCount: 5, ToolCallCount: 50},
		},
		DailyModelTokens: []dailyModelTokensEntry{}, // empty
	}

	data, _ := json.Marshal(cache)
	path := filepath.Join(t.TempDir(), "stats-cache.json")
	os.WriteFile(path, data, 0o600)

	c := newClaudeStatsClientAt(path)
	activity, err := c.GetActivity("2026-02-09", "2026-02-09")
	if err != nil {
		t.Fatalf("GetActivity error: %v", err)
	}
	if len(activity) != 1 {
		t.Fatalf("len(activity) = %d, want 1", len(activity))
	}
	if activity[0].TokensUsed != 0 {
		t.Errorf("TokensUsed = %d, want 0 when no token data", activity[0].TokensUsed)
	}
}

func TestGetActivity_MultipleModelsPerDay(t *testing.T) {
	// Multiple models for one day → sum should be total
	cache := statsCache{
		DailyActivity: []dailyActivityEntry{
			{Date: "2026-02-09", MessageCount: 50},
		},
		DailyModelTokens: []dailyModelTokensEntry{
			{Date: "2026-02-09", TokensByModel: map[string]int{
				"claude-sonnet-4-6": 10000,
				"claude-haiku-4-5":  5000,
				"claude-opus-4-6":   2000,
			}},
		},
	}

	data, _ := json.Marshal(cache)
	path := filepath.Join(t.TempDir(), "stats-cache.json")
	os.WriteFile(path, data, 0o600)

	c := newClaudeStatsClientAt(path)
	activity, err := c.GetActivity("2026-02-09", "2026-02-09")
	if err != nil {
		t.Fatalf("GetActivity error: %v", err)
	}
	if len(activity) != 1 {
		t.Fatalf("len(activity) = %d, want 1", len(activity))
	}
	// 10000 + 5000 + 2000 = 17000
	if activity[0].TokensUsed != 17000 {
		t.Errorf("TokensUsed = %d, want 17000 (sum of all models)", activity[0].TokensUsed)
	}
}
