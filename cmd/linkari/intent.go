package main

// EPIC-154 F1: Intent data model functions.
// Provides profileToIntentLookup, backfillIntentFromProfile, and deriveProfileFromIntent.
// These functions are called at server start (backfill) and on each POST /share (dual-write).

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

// validIntents is the complete set of accepted intent values.
var validIntents = map[string]bool{
	"score":      true,
	"capture":    true,
	"transcribe": true,
}

// profileMapping holds the intent and inferred tags for a profile.
type profileMapping struct {
	Intent       string
	InferredTags []string
}

// profileIntentMap maps exact profile names to their intent mapping.
var profileIntentMap = map[string]profileMapping{
	"eng":     {"score", []string{"domain:eng"}},
	"life":    {"score", []string{"domain:personal"}},
	"travel":  {"score", []string{"domain:travel"}},
	"fashion": {"score", []string{"domain:fashion"}},
	"music":   {"score", []string{"domain:music"}},
	"finance": {"score", []string{"domain:finance"}},
	"dining":  {"score", []string{"domain:dining"}},
}

// profileToIntentLookup maps a profile string to (intent, inferredTags).
// Exact match is tried first; then a 5-char prefix match for ginit_* and vnote_* prefixes.
// Returns ok=false for unknown profiles; callers should default to "score" on miss.
func profileToIntentLookup(profile string) (intent string, inferredTags []string, ok bool) {
	if m, found := profileIntentMap[profile]; found {
		return m.Intent, m.InferredTags, true
	}
	// Prefix match for ginit_* → capture/jira
	if strings.HasPrefix(profile, "ginit") {
		return "capture", []string{"jira"}, true
	}
	// Prefix match for vnote_* → transcribe
	if strings.HasPrefix(profile, "vnote") {
		return "transcribe", []string{}, true
	}
	return "", nil, false
}

// backfillIntentFromProfile runs at server start when any queue rows have intent=NULL.
// It maps profile → (intent, inferred_tags) via profileToIntentLookup.
// Unknown profiles are treated as intent="score" with empty inferred_tags.
// The entire backfill runs in a single transaction; rolls back on any row failure.
// Idempotent: rows with intent already set are skipped.
func backfillIntentFromProfile(db *sql.DB) (rowsUpdated int, err error) {
	rows, err := db.Query(`SELECT id, profile FROM queue WHERE intent IS NULL`)
	if err != nil {
		return 0, err
	}
	type row struct {
		id      int64
		profile string
	}
	var toUpdate []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.profile); err != nil {
			rows.Close()
			return 0, err
		}
		toUpdate = append(toUpdate, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(toUpdate) == 0 {
		return 0, nil
	}

	slog.Info("intent_migration_start", "rows_to_migrate", len(toUpdate))
	start := time.Now()

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			slog.Error(
				"intent_migration_failed",
				"error_class", "intent_migration_failed",
				"rows_updated", rowsUpdated,
				"error", err,
			)
		}
	}()

	stmt, err := tx.Prepare(`UPDATE queue SET intent = ?, inferred_tags = ? WHERE id = ? AND intent IS NULL`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for _, r := range toUpdate {
		intent, inferredTags, ok := profileToIntentLookup(r.profile)
		if !ok {
			intent = "score"
			inferredTags = []string{}
		}
		tagsJSON, _ := json.Marshal(inferredTags)
		res, execErr := stmt.Exec(intent, string(tagsJSON), r.id)
		if execErr != nil {
			err = execErr
			return rowsUpdated, err
		}
		n, _ := res.RowsAffected()
		rowsUpdated += int(n)
	}

	if commitErr := tx.Commit(); commitErr != nil {
		err = commitErr
		return rowsUpdated, err
	}

	slog.Info(
		"intent_migration_complete",
		"rows_updated", rowsUpdated,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return rowsUpdated, nil
}

// actionIntentMap maps known action IDs to (intent, tagSig) for backward compat.
// Used when action is present but intent is absent in POST /share.
var actionIntentMap = map[string]struct {
	Intent string
	TagSig string
}{
	"uinit_eng":     {"score", "domain:eng"},
	"uinit_life":    {"score", "domain:personal"},
	"uinit_travel":  {"score", "domain:travel"},
	"uinit_fashion": {"score", "domain:fashion"},
	"uinit_music":   {"score", "domain:music"},
	"uinit_finance": {"score", "domain:finance"},
	"uinit_dining":  {"score", "domain:dining"},
	"ginit_eng":     {"capture", "jira"},
	"ginit_auto":    {"capture", "jira"},
	"vnote_auto":    {"transcribe", ""},
}

// deriveIntentFromAction maps a legacy action ID to (intent, tagSig).
// Exact match first; then ginit_* → capture/jira and vnote_* → transcribe prefix fallback.
// Always returns a safe default ("score", "", false) for unknown actions.
func deriveIntentFromAction(action string) (intent, tagSig string, ok bool) {
	if m, found := actionIntentMap[action]; found {
		return m.Intent, m.TagSig, true
	}
	if strings.HasPrefix(action, "ginit") {
		return "capture", "jira", true
	}
	if strings.HasPrefix(action, "vnote") {
		return "transcribe", "", true
	}
	return "score", "", false
}

// deriveProfileFromIntent maps intent + user tags back to a legacy profile string.
// Used by enqueueItem to keep the profile column populated during the soak window.
func deriveProfileFromIntent(intent string, userTags []string) string {
	// Check user tags for domain: prefix hints first.
	for _, tag := range userTags {
		if strings.HasPrefix(tag, "domain:") {
			domain := strings.TrimPrefix(tag, "domain:")
			if _, found := profileIntentMap[domain]; found {
				return domain
			}
		}
	}
	// Fall back to intent-based default profile.
	switch intent {
	case "capture":
		return "ginit"
	case "transcribe":
		return "vnote"
	default: // "score" or empty
		return "eng"
	}
}
