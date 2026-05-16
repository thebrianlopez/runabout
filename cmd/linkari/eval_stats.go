package main

import (
	"fmt"
	"sort"
)

// sortedProfileIDs returns the registered profile IDs in alphabetical order.
func sortedProfileIDs() []string {
	ids := make([]string, 0, len(validProfileIDs))
	for id := range validProfileIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// StatsResult holds corpus coverage statistics from RunEvalStats.
type StatsResult struct {
	Profiles map[string]int `json:"profiles"` // fixture count per profile (all 7 appear, even with 0)
	Total    int            `json:"total"`    // total fixture count
	Missing  []string       `json:"missing"`  // profiles with 0 fixtures
}

// RunEvalStats counts fixtures per profile in dir and identifies missing coverage.
// All 7 registered profiles appear in Profiles, even if count is 0.
func RunEvalStats(dir string) (*StatsResult, error) {
	entries, err := loadFixtures(dir)
	if err != nil {
		return nil, fmt.Errorf("stats_no_fixtures: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("stats_no_fixtures: no fixture JSON files found in %s", dir)
	}

	result := &StatsResult{Profiles: make(map[string]int)}
	for p := range validProfileIDs {
		result.Profiles[p] = 0
	}
	for _, f := range entries {
		if validProfileIDs[f.Profile] {
			result.Profiles[f.Profile]++
			result.Total++
		}
	}
	for p, count := range result.Profiles {
		if count == 0 {
			result.Missing = append(result.Missing, p)
		}
	}
	return result, nil
}
