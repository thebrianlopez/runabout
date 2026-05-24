package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// TagItem is a single entry in the tag inventory response.
type TagItem struct {
	Name       string `json:"name"`
	UseCount   int    `json:"use_count"`
	LastUsedAt string `json:"last_used_at"`
}

// TagsResponse is the envelope for GET /tags.
type TagsResponse struct {
	Tags []TagItem `json:"tags"`
}

func normalizeTag(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// validateUserTags checks that every tag is non-empty and at most 50 chars
// after normalization. Returns the first violation found.
const maxUserRationaleChars = 500

func validateUserTags(tags []string) error {
	for _, tag := range tags {
		n := normalizeTag(tag)
		if n == "" {
			return fmt.Errorf("tag must not be empty")
		}
		if len(n) > 50 {
			return fmt.Errorf("tag %q exceeds 50-character limit (%d chars)", n, len(n))
		}
	}
	return nil
}

// persistUserTags normalizes tags, writes them as a JSON array to the
// user_tags column of the queue row, and upserts each tag into the tags
// inventory table. All writes run inside a single transaction. Callers
// treat any error as non-blocking  -  the share row already exists.
func normalizeRationale(req *ShareRequest) (droppedReason string) {
	req.UserRationaleText = strings.TrimSpace(req.UserRationaleText)
	req.UserRationaleSource = strings.TrimSpace(req.UserRationaleSource)
	req.CaptureMode = strings.TrimSpace(req.CaptureMode)
	req.SourceApp = strings.TrimSpace(req.SourceApp)
	if req.UserRationaleText == "" {
		req.UserRationaleSource = ""
		req.UserRationaleDurationMS = 0
		return ""
	}
	if len(req.UserRationaleText) > maxUserRationaleChars {
		req.UserRationaleText = ""
		req.UserRationaleSource = ""
		req.UserRationaleDurationMS = 0
		return "rationale_too_long"
	}
	if req.UserRationaleSource != "typed" && req.UserRationaleSource != "voice_transcript" {
		req.UserRationaleText = ""
		req.UserRationaleSource = ""
		req.UserRationaleDurationMS = 0
		return "invalid_rationale_source"
	}
	if req.UserRationaleDurationMS < 0 {
		req.UserRationaleDurationMS = 0
	}
	return ""
}

func (q *Queue) persistUserTags(rowID int64, tags []string) error {
	normalized := make([]string, len(tags))
	for i, tag := range tags {
		normalized[i] = normalizeTag(tag)
	}

	tagsJSON, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal user_tags: %w", err)
	}

	tx, err := q.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec("UPDATE queue SET user_tags = ? WHERE id = ?", string(tagsJSON), rowID); err != nil {
		return fmt.Errorf("update queue user_tags: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, name := range normalized {
		_, err := tx.Exec(`
			INSERT INTO tags (name, use_count, last_used_at, created_at)
			VALUES (?, 1, ?, ?)
			ON CONFLICT(name) DO UPDATE SET
				use_count    = use_count + 1,
				last_used_at = excluded.last_used_at`,
			name, now, now,
		)
		if err != nil {
			return fmt.Errorf("upsert tag %q: %w", name, err)
		}
	}

	return tx.Commit()
}

// GetTags returns the tag inventory ranked by combined recency/frequency score
// (0.7 * normalized frequency + 0.3 * normalized recency). limit caps the
// result set; 0 means no limit. Returns an empty (non-nil) slice when no tags exist.
func (q *Queue) GetTags(limit int) ([]TagItem, error) {
	rows, err := q.db.Query(`SELECT name, use_count, last_used_at FROM tags`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []TagItem
	for rows.Next() {
		var t TagItem
		if err := rows.Scan(&t.Name, &t.UseCount, &t.LastUsedAt); err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rankTags(items)

	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	if items == nil {
		items = []TagItem{}
	}
	return items, nil
}

// rankTags sorts items in-place by combined score: 0.7*freq + 0.3*recency,
// both normalized to [0,1]. Ties broken by name for deterministic output.
func rankTags(items []TagItem) {
	if len(items) < 2 {
		return
	}

	maxCount := 0
	var minTS, maxTS int64
	for i, t := range items {
		if t.UseCount > maxCount {
			maxCount = t.UseCount
		}
		ts := parseTagTime(t.LastUsedAt)
		if i == 0 || ts < minTS {
			minTS = ts
		}
		if i == 0 || ts > maxTS {
			maxTS = ts
		}
	}

	tsRange := float64(maxTS - minTS)
	score := func(t TagItem) float64 {
		freq := 0.0
		if maxCount > 0 {
			freq = float64(t.UseCount) / float64(maxCount)
		}
		rec := 1.0
		if tsRange > 0 {
			rec = float64(parseTagTime(t.LastUsedAt)-minTS) / tsRange
		}
		return 0.7*freq + 0.3*rec
	}

	sort.SliceStable(items, func(i, j int) bool {
		si, sj := score(items[i]), score(items[j])
		if si != sj {
			return si > sj
		}
		return items[i].Name < items[j].Name
	})
}

func parseTagTime(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}
