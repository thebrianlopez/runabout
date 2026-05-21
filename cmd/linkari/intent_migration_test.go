package main

// EPIC-154 M1: Intent data model + schema migration contract tests.
// CT-1 through CT-10 assert the F1 intent migration contracts.
// Written before implementation (TDD gate).

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// CT-1: All existing rows have intent populated after backfill.
func TestIntentMigration_CT1_BackfillSetsIntent(t *testing.T) {
	q := newTestQueue(t)

	// Insert a pre-migration row with only profile set; intent NULL.
	q.db.Exec(`INSERT INTO queue (url, text, type, action, profile, status, queued_at)
		VALUES ('https://ct1.example.com', '', 'url', 'uinit_eng', 'eng', 'pending', '2026-01-01T00:00:00Z')`)

	// Force intent NULL (Enqueue now sets it; bypass via direct SQL).
	q.db.Exec(`UPDATE queue SET intent = NULL WHERE url = 'https://ct1.example.com'`)

	n, err := backfillIntentFromProfile(q.db)
	if err != nil {
		t.Fatalf("CT-1: backfill error: %v", err)
	}
	if n == 0 {
		t.Error("CT-1: expected at least 1 row updated, got 0")
	}

	var nullCount int
	q.db.QueryRow(`SELECT COUNT(*) FROM queue WHERE intent IS NULL`).Scan(&nullCount)
	if nullCount != 0 {
		t.Errorf("CT-1: %d rows still have intent=NULL after backfill", nullCount)
	}
}

// CT-2: inferred_tags and user_tags stored in separate columns.
func TestIntentMigration_CT2_SeparateColumns(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://ct2.example.com","user_tags":["jira"],"intent":"score"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-2: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp ShareResponse
	json.NewDecoder(w.Body).Decode(&resp)

	var userTagsJSON, inferredTagsJSON string
	q.db.QueryRow(`SELECT user_tags, inferred_tags FROM queue WHERE id = ?`, resp.ID).
		Scan(&userTagsJSON, &inferredTagsJSON)

	// user_tags must not be empty (jira was sent)
	if userTagsJSON == "" || userTagsJSON == "[]" || userTagsJSON == "null" {
		t.Errorf("CT-2: user_tags empty in DB, got %q", userTagsJSON)
	}
	// Columns must be distinct storage - user_tags and inferred_tags are never the same slice
	if userTagsJSON == inferredTagsJSON && userTagsJSON != "" && userTagsJSON != "[]" {
		t.Errorf("CT-2: user_tags == inferred_tags, columns merged: %q", userTagsJSON)
	}
	// intent must be set
	var intent string
	q.db.QueryRow(`SELECT intent FROM queue WHERE id = ?`, resp.ID).Scan(&intent)
	if intent != "score" {
		t.Errorf("CT-2: intent = %q, want score", intent)
	}
}

// CT-3: POST /share with intent field accepted and persisted.
func TestIntentMigration_CT3_IntentFieldAccepted(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://ct3.example.com","intent":"score","user_tags":["domain:eng"]}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-3: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp ShareResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID == 0 {
		t.Fatal("CT-3: expected non-zero queue ID")
	}

	var intent string
	q.db.QueryRow(`SELECT intent FROM queue WHERE id = ?`, resp.ID).Scan(&intent)
	if intent != "score" {
		t.Errorf("CT-3: intent = %q, want score", intent)
	}
}

// CT-4: Rollback leaves profile + user_tags intact.
// Simulates rollback by dropping the new columns and verifying original data survives.
func TestIntentMigration_CT4_RollbackPreservesData(t *testing.T) {
	q := newTestQueue(t)

	// Insert a row with profile and user_tags set.
	id, _ := q.Enqueue(&ShareRequest{
		Type:     "url",
		URL:      "https://ct4.example.com",
		UserTags: []string{"tech"},
	})
	q.persistUserTags(id, []string{"tech"})

	origProfile := "eng"
	q.db.Exec(`UPDATE queue SET profile = ? WHERE id = ?`, origProfile, id)

	// Simulate rollback: drop new columns.
	q.db.Exec(`ALTER TABLE queue DROP COLUMN intent`)
	q.db.Exec(`ALTER TABLE queue DROP COLUMN inferred_tags`)

	var gotProfile string
	var gotUserTags string
	q.db.QueryRow(`SELECT profile, user_tags FROM queue WHERE id = ?`, id).
		Scan(&gotProfile, &gotUserTags)

	if gotProfile != origProfile {
		t.Errorf("CT-4: profile = %q after rollback, want %q", gotProfile, origProfile)
	}
	if !strings.Contains(gotUserTags, "tech") {
		t.Errorf("CT-4: user_tags = %q after rollback, want to contain 'tech'", gotUserTags)
	}
}

// CT-5: profileToIntentLookup covers all 7 base profiles.
func TestIntentMigration_CT5_ProfileLookupBaseProfiles(t *testing.T) {
	profiles := []string{"eng", "life", "travel", "fashion", "music", "finance", "dining"}
	for _, p := range profiles {
		intent, tags, ok := profileToIntentLookup(p)
		if !ok {
			t.Errorf("CT-5: profileToIntentLookup(%q) ok=false", p)
			continue
		}
		if intent == "" {
			t.Errorf("CT-5: profileToIntentLookup(%q) intent empty", p)
		}
		_ = tags // inferred_tags may be empty for some profiles
	}
}

// CT-6: profileToIntentLookup covers ginit prefix.
func TestIntentMigration_CT6_ProfileLookupGinitPrefix(t *testing.T) {
	intent, tags, ok := profileToIntentLookup("ginit_eng")
	if !ok {
		t.Fatal("CT-6: profileToIntentLookup(ginit_eng) ok=false")
	}
	if intent != "capture" {
		t.Errorf("CT-6: intent = %q, want capture", intent)
	}
	found := false
	for _, tag := range tags {
		if tag == "jira" {
			found = true
		}
	}
	if !found {
		t.Errorf("CT-6: inferred_tags = %v, want to contain 'jira'", tags)
	}
}

// CT-7: profileToIntentLookup covers vnote prefix.
func TestIntentMigration_CT7_ProfileLookupVnotePrefix(t *testing.T) {
	intent, tags, ok := profileToIntentLookup("vnote_auto")
	if !ok {
		t.Fatal("CT-7: profileToIntentLookup(vnote_auto) ok=false")
	}
	if intent != "transcribe" {
		t.Errorf("CT-7: intent = %q, want transcribe", intent)
	}
	if len(tags) != 0 {
		t.Errorf("CT-7: inferred_tags = %v, want empty", tags)
	}
}

// CT-8: Unknown profile backfills as score with empty inferred_tags.
func TestIntentMigration_CT8_UnknownProfileFallback(t *testing.T) {
	intent, tags, ok := profileToIntentLookup("unknown_xyz")
	// ok=false is acceptable; what matters is that callers handle it gracefully
	// and the backfill uses "score" as the default.
	if ok {
		// If lookup returned a match, it must be "score".
		if intent != "score" {
			t.Errorf("CT-8: unexpected intent %q for unknown profile", intent)
		}
		_ = tags
	}
	// Verify backfill treats unknown profiles as score.
	q := newTestQueue(t)
	q.db.Exec(`INSERT INTO queue (url, text, type, action, profile, status, queued_at)
		VALUES ('https://ct8.example.com', '', 'url', '', 'unknown_xyz', 'pending', '2026-01-01T00:00:00Z')`)
	q.db.Exec(`UPDATE queue SET intent = NULL WHERE profile = 'unknown_xyz'`)

	backfillIntentFromProfile(q.db)

	var backfilledIntent string
	q.db.QueryRow(`SELECT intent FROM queue WHERE profile = 'unknown_xyz'`).Scan(&backfilledIntent)
	if backfilledIntent != "score" {
		t.Errorf("CT-8: backfill set intent=%q for unknown profile, want score", backfilledIntent)
	}
}

// CT-9: Backfill is idempotent - running twice produces same result.
func TestIntentMigration_CT9_BackfillIdempotent(t *testing.T) {
	q := newTestQueue(t)
	q.db.Exec(`INSERT INTO queue (url, text, type, action, profile, status, queued_at)
		VALUES ('https://ct9.example.com', '', 'url', 'uinit_eng', 'eng', 'pending', '2026-01-01T00:00:00Z')`)
	q.db.Exec(`UPDATE queue SET intent = NULL WHERE url = 'https://ct9.example.com'`)

	n1, _ := backfillIntentFromProfile(q.db)
	n2, _ := backfillIntentFromProfile(q.db)

	if n2 != 0 {
		t.Errorf("CT-9: second backfill updated %d rows, want 0 (idempotent)", n2)
	}
	_ = n1
}

// CT-10: intent_unknown error on invalid intent value in POST /share.
func TestIntentMigration_CT10_InvalidIntentRejected(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://ct10.example.com","intent":"invalid"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("CT-10: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "intent") {
		t.Errorf("CT-10: error body %q should mention 'intent'", w.Body.String())
	}
}

// BT-2: Dual-write keeps profile in sync when intent is set.
func TestIntentMigration_BT2_DualWriteProfile(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://bt2.example.com","intent":"score","user_tags":["domain:eng"]}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("BT-2: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp ShareResponse
	json.NewDecoder(w.Body).Decode(&resp)

	var profile string
	q.db.QueryRow(`SELECT profile FROM queue WHERE id = ?`, resp.ID).Scan(&profile)
	if profile == "" {
		t.Errorf("BT-2: profile is empty; dual-write must keep profile populated during soak")
	}
}

// BT-3: inferred_tags_malformed does not crash the server.
func TestIntentMigration_BT3_MalformedInferredTagsNoCrash(t *testing.T) {
	q := newTestQueue(t)

	// Insert a row with malformed inferred_tags JSON.
	q.db.Exec(`INSERT INTO queue (url, text, type, action, profile, intent, inferred_tags, status, queued_at)
		VALUES ('https://bt3.example.com', '', 'url', '', 'eng', 'score', 'not-json', 'archived', '2026-01-01T00:00:00Z')`)

	var rowID int64
	q.db.QueryRow(`SELECT id FROM queue WHERE url = 'https://bt3.example.com'`).Scan(&rowID)

	// Loading the row should not panic - ListArchivedCursor is the primary read path.
	items, err := q.ListArchivedCursorTyped("eng", "archived", "", 0, 10, nil)
	if err != nil {
		t.Fatalf("BT-3: ListArchivedCursorTyped error: %v", err)
	}
	_ = items // malformed row should be present but InferredTags treated as []
}

// RG-1: Backfill leaves user_tags intact.
func TestIntentMigration_RG1_BackfillPreservesUserTags(t *testing.T) {
	q := newTestQueue(t)

	id, _ := q.Enqueue(&ShareRequest{Type: "url", URL: "https://rg1.example.com"})
	q.persistUserTags(id, []string{"preserved-tag"})
	q.db.Exec(`UPDATE queue SET intent = NULL, profile = 'eng' WHERE id = ?`, id)

	backfillIntentFromProfile(q.db)

	var userTagsJSON string
	q.db.QueryRow(`SELECT user_tags FROM queue WHERE id = ?`, id).Scan(&userTagsJSON)
	if !strings.Contains(userTagsJSON, "preserved-tag") {
		t.Errorf("RG-1: user_tags = %q after backfill, want to contain 'preserved-tag'", userTagsJSON)
	}
}

// RG-2: Enqueue never writes the same slice reference to both user_tags and inferred_tags.
// This is a compile-time + read invariant: user_tags is from req.UserTags, inferred_tags from lookup.
func TestIntentMigration_RG2_UserTagsInferredTagsNeverMerged(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	// Share with a user tag that won't match the system-inferred tags from "eng" profile.
	// eng profile infers ["domain:eng"]; user sends ["my-bookmark"] - distinct sources.
	body := `{"type":"url","url":"https://rg2.example.com","user_tags":["my-bookmark"],"action":"uinit_eng"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("RG-2: expected 200, got %d", w.Code)
	}
	var resp ShareResponse
	json.NewDecoder(w.Body).Decode(&resp)

	var userTagsJSON, inferredTagsJSON sql.NullString
	q.db.QueryRow(`SELECT user_tags, inferred_tags FROM queue WHERE id = ?`, resp.ID).
		Scan(&userTagsJSON, &inferredTagsJSON)

	// user_tags must contain the user-sent tag.
	if !strings.Contains(userTagsJSON.String, "my-bookmark") {
		t.Errorf("RG-2: user_tags=%q, want 'my-bookmark'", userTagsJSON.String)
	}
	// inferred_tags must NOT contain the user-sent tag (they're separate sources).
	if inferredTagsJSON.Valid && strings.Contains(inferredTagsJSON.String, "my-bookmark") {
		t.Errorf("RG-2: inferred_tags=%q contains user tag 'my-bookmark'; columns merged", inferredTagsJSON.String)
	}
}
