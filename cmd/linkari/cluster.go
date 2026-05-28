package main

// EPIC-072 M6: Tag-based cluster detection using Jaccard similarity
// on topic_tags extracted by Haiku scoring.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// ClusterGroup represents a detected cluster of related queue items.
type ClusterGroup struct {
	ID        int64    `json:"id"`
	Profile   string   `json:"profile"`
	Theme     string   `json:"theme"`
	Synthesis string   `json:"synthesis,omitempty"`
	FormedAt  string   `json:"formed_at"`
	ItemCount int      `json:"item_count"`
	Tags      []string `json:"tags"`
	ItemIDs   []int64  `json:"item_ids"`
}

// parseTags parses a JSON array string into a string slice.
func parseTags(raw string) []string {
	if raw == "" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		// Fall back to comma-separated for legacy data.
		for _, t := range strings.Split(raw, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tags = append(tags, t)
			}
		}
	}
	return tags
}

// jaccardSimilarity computes the Jaccard index between two tag sets.
// Returns 0 when both sets are empty.
func jaccardSimilarity(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	setA := make(map[string]bool, len(a))
	for _, t := range a {
		setA[t] = true
	}
	setB := make(map[string]bool, len(b))
	for _, t := range b {
		setB[t] = true
	}

	var intersection int
	for t := range setA {
		if setB[t] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// tagIntersection returns the sorted intersection of two tag sets.
func tagIntersection(a, b []string) []string {
	setA := make(map[string]bool, len(a))
	for _, t := range a {
		setA[t] = true
	}
	var common []string
	seen := make(map[string]bool)
	for _, t := range b {
		if setA[t] && !seen[t] {
			common = append(common, t)
			seen[t] = true
		}
	}
	sort.Strings(common)
	return common
}

// buildClusters groups items using connected components on Jaccard similarity.
// Any two items with similarity >= threshold get an edge; connected components
// with >= minItems items become clusters.
func buildClusters(items []QueueItem, threshold float64, minItems int) [][]QueueItem {
	n := len(items)
	if n < minItems {
		return nil
	}

	// Parse tags for each item.
	tagSets := make([][]string, n)
	for i, it := range items {
		tagSets[i] = parseTags(it.TopicTags)
	}

	// Union-Find for connected components.
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	// Build edges.
	for i := 0; i < n; i++ {
		if len(tagSets[i]) == 0 {
			continue
		}
		for j := i + 1; j < n; j++ {
			if len(tagSets[j]) == 0 {
				continue
			}
			if jaccardSimilarity(tagSets[i], tagSets[j]) >= threshold {
				union(i, j)
			}
		}
	}

	// Collect components.
	components := make(map[int][]int)
	for i := range items {
		root := find(i)
		components[root] = append(components[root], i)
	}

	var clusters [][]QueueItem
	for _, indices := range components {
		if len(indices) < minItems {
			continue
		}
		cluster := make([]QueueItem, len(indices))
		for i, idx := range indices {
			cluster[i] = items[idx]
		}
		clusters = append(clusters, cluster)
	}
	return clusters
}

// clusterTheme generates a theme string from cluster tag intersection.
func clusterTheme(items []QueueItem) string {
	if len(items) == 0 {
		return ""
	}
	// Compute intersection of all items' tags.
	common := parseTags(items[0].TopicTags)
	for _, it := range items[1:] {
		common = tagIntersection(common, parseTags(it.TopicTags))
	}
	if len(common) == 0 {
		return "mixed"
	}
	return strings.Join(common, ", ")
}

// detectClusters runs cluster detection for a profile after scoring (EPIC-072 M6).
// Looks at recent scored/archived items within a 7-day window.
func detectClusters(ctx context.Context, q *Queue, profile string, threshold float64, minItems int) {
	if threshold <= 0 {
		threshold = 0.4
	}
	if minItems <= 0 {
		minItems = 3
	}

	since := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	items, err := q.query(
		"SELECT "+queueCols+" FROM queue WHERE profile=? AND status IN ('scored','archived') AND topic_tags!='' AND type!='audio' AND scored_at>=? ORDER BY id DESC LIMIT 100",
		profile, since,
	)
	if err != nil {
		slog.WarnContext(ctx, "detectClusters query failed", "profile", profile, "error", err)
		return
	}

	clusters := buildClusters(items, threshold, minItems)
	for _, cluster := range clusters {
		theme := clusterTheme(cluster)
		itemIDs := make([]int64, len(cluster))
		for i, it := range cluster {
			itemIDs[i] = it.ID
		}

		// Check if a similar cluster already exists (same theme + overlapping items).
		existing, _ := q.findExistingCluster(profile, theme)
		if existing != nil {
			continue // Don't create duplicate clusters.
		}

		clusterID, err := q.createCluster(profile, theme, itemIDs)
		if err != nil {
			slog.WarnContext(ctx, "createCluster failed", "profile", profile, "theme", theme, "error", err)
			continue
		}

		// Assign cluster_id to member items.
		for _, id := range itemIDs {
			q.db.Exec("UPDATE queue SET cluster_id=? WHERE id=?", clusterID, id)
		}

		slog.InfoContext(
			ctx, "cluster formed",
			"event_type", "cluster_formed",
			"profile", profile,
			"cluster_id", clusterID,
			"theme", theme,
			"item_count", len(cluster),
		)
	}
}

// createCluster inserts a new cluster row and returns its ID.
func (q *Queue) createCluster(profile, theme string, itemIDs []int64) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := q.db.Exec(
		"INSERT INTO clusters (profile, theme, formed_at, item_count) VALUES (?, ?, ?, ?)",
		profile, theme, now, len(itemIDs),
	)
	if err != nil {
		return 0, fmt.Errorf("insert cluster: %w", err)
	}
	return res.LastInsertId()
}

// findExistingCluster checks for an existing cluster with the same theme for a profile.
func (q *Queue) findExistingCluster(profile, theme string) (*ClusterGroup, error) {
	row := q.db.QueryRow(
		"SELECT id, profile, theme, COALESCE(synthesis,''), formed_at, item_count FROM clusters WHERE profile=? AND theme=? ORDER BY id DESC LIMIT 1",
		profile, theme,
	)
	var c ClusterGroup
	if err := row.Scan(&c.ID, &c.Profile, &c.Theme, &c.Synthesis, &c.FormedAt, &c.ItemCount); err != nil {
		return nil, err
	}
	return &c, nil
}

// ListClusters returns clusters for a profile.
func (q *Queue) ListClusters(profile string) ([]ClusterGroup, error) {
	sqlStr := "SELECT id, profile, theme, COALESCE(synthesis,''), formed_at, item_count FROM clusters"
	args := []any{}
	if profile != "" {
		sqlStr += " WHERE profile=?"
		args = append(args, profile)
	}
	sqlStr += " ORDER BY formed_at DESC"
	rows, err := q.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var clusters []ClusterGroup
	for rows.Next() {
		var c ClusterGroup
		if err := rows.Scan(&c.ID, &c.Profile, &c.Theme, &c.Synthesis, &c.FormedAt, &c.ItemCount); err != nil {
			return nil, err
		}
		clusters = append(clusters, c)
	}
	return clusters, rows.Err()
}

// GetClusterItems returns queue items belonging to a cluster.
func (q *Queue) GetClusterItems(clusterID int64) ([]QueueItem, error) {
	return q.query("SELECT "+queueCols+" FROM queue WHERE cluster_id=? ORDER BY id DESC", clusterID)
}
