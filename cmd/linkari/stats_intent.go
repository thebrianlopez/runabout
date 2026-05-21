package main

// EPIC-159 F6: Stats + Metrics Migration - Intent and Tag Dimensions.
// Provides GET /stats/intents and GET /stats/tags endpoints.
// Stats are computed from queue rows where intent IS NOT NULL, joined against feedback.
// Tag stats use user_tags only (not inferred_tags) per TDD §3.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

// IntentStatsResponse is the JSON envelope for GET /stats/intents.
type IntentStatsResponse struct {
	Intents []IntentStat `json:"intents"`
}

// IntentStat holds per-intent scoring and feedback aggregates.
type IntentStat struct {
	Intent      string  `json:"intent"`
	TotalScored int     `json:"total_scored"`
	ThumbsUp    int     `json:"thumbs_up"`
	ThumbsDown  int     `json:"thumbs_down"`
	Precision   float64 `json:"precision"`
}

// TagStatsResponse is the JSON envelope for GET /stats/tags.
type TagStatsResponse struct {
	Tags []TagStat `json:"tags"`
}

// TagStat holds per-tag scoring and feedback aggregates.
type TagStat struct {
	Tag         string  `json:"tag"`
	TotalScored int     `json:"total_scored"`
	ThumbsUp    int     `json:"thumbs_up"`
	ThumbsDown  int     `json:"thumbs_down"`
	Precision   float64 `json:"precision"`
}

// handleIntentStats serves GET /stats/intents.
// Returns per-intent aggregate stats from scored queue rows.
// EPIC-159 F6 M4.
func (s *Server) handleIntentStats(w http.ResponseWriter, r *http.Request) {
	if !s.authenticateRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	stats, err := s.queue.IntentStats()
	if err != nil {
		slog.Error("stats_query_failed",
			"error_class", "stats_query_failed",
			"endpoint", "/stats/intents",
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("stats: %v", err))
		return
	}

	resp := IntentStatsResponse{Intents: stats}
	if resp.Intents == nil {
		resp.Intents = []IntentStat{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleTagStats serves GET /stats/tags.
// Computes per-tag stats from user_tags column only (not inferred_tags).
// EPIC-159 F6 M5.
func (s *Server) handleTagStats(w http.ResponseWriter, r *http.Request) {
	if !s.authenticateRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	stats, err := s.queue.TagStats()
	if err != nil {
		slog.Error("stats_query_failed",
			"error_class", "stats_query_failed",
			"endpoint", "/stats/tags",
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("stats: %v", err))
		return
	}

	resp := TagStatsResponse{Tags: stats}
	if resp.Tags == nil {
		resp.Tags = []TagStat{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// IntentStats aggregates scoring and feedback by intent from queue rows.
// Rows without intent set are excluded.
func (q *Queue) IntentStats() ([]IntentStat, error) {
	rows, err := q.db.Query(`
		SELECT
			intent,
			COUNT(*) AS total_scored,
			SUM(CASE WHEN feedback = 'accurate' THEN 1 ELSE 0 END) AS thumbs_up,
			SUM(CASE WHEN feedback IN ('too_low', 'too_high') THEN 1 ELSE 0 END) AS thumbs_down
		FROM queue
		WHERE intent IS NOT NULL AND status IN ('archived', 'scored')
		GROUP BY intent
		ORDER BY intent
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []IntentStat
	for rows.Next() {
		var s IntentStat
		if err := rows.Scan(&s.Intent, &s.TotalScored, &s.ThumbsUp, &s.ThumbsDown); err != nil {
			return nil, err
		}
		total := s.ThumbsUp + s.ThumbsDown
		if total > 0 {
			s.Precision = float64(s.ThumbsUp) / float64(total)
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// TagStats aggregates scoring and feedback by user_tags.
// Tags are extracted from the JSON array in the user_tags column.
// inferred_tags are explicitly excluded per F6 §3.
func (q *Queue) TagStats() ([]TagStat, error) {
	// Fetch all rows with user_tags and feedback.
	rows, err := q.db.Query(`
		SELECT user_tags, feedback
		FROM queue
		WHERE intent IS NOT NULL
		  AND status IN ('archived', 'scored')
		  AND user_tags IS NOT NULL
		  AND user_tags != ''
		  AND user_tags != '[]'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type tagAgg struct {
		total    int
		thumbsUp int
		thumbsDn int
	}
	agg := make(map[string]*tagAgg)

	for rows.Next() {
		var userTagsJSON sql.NullString
		var feedback sql.NullString
		if err := rows.Scan(&userTagsJSON, &feedback); err != nil {
			return nil, err
		}
		if !userTagsJSON.Valid || userTagsJSON.String == "" {
			continue
		}
		var tags []string
		if err := json.Unmarshal([]byte(userTagsJSON.String), &tags); err != nil {
			continue // malformed - skip row
		}
		for _, tag := range tags {
			if tag == "" {
				continue
			}
			if agg[tag] == nil {
				agg[tag] = &tagAgg{}
			}
			agg[tag].total++
			if feedback.Valid {
				switch feedback.String {
				case "accurate":
					agg[tag].thumbsUp++
				case "too_low", "too_high":
					agg[tag].thumbsDn++
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]TagStat, 0, len(agg))
	for tag, a := range agg {
		s := TagStat{
			Tag:         tag,
			TotalScored: a.total,
			ThumbsUp:    a.thumbsUp,
			ThumbsDown:  a.thumbsDn,
		}
		total := s.ThumbsUp + s.ThumbsDown
		if total > 0 {
			s.Precision = float64(s.ThumbsUp) / float64(total)
		}
		result = append(result, s)
	}
	return result, nil
}
