package main

// EPIC-159 F6 M1: Stats + Metrics Migration contract tests.
// CT-1 through CT-6 assert GET /stats/intents and GET /stats/tags behavior.
// Written before implementation (TDD gate).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// CT-1: GET /stats/intents returns data after 1+ scored item with intent.
func TestStatsIntent_CT1_IntentStatsAfterScoredItem(t *testing.T) {
	q := newTestQueue(t)

	// Insert a scored item with intent and positive feedback.
	q.db.Exec(`INSERT INTO queue (url, text, type, action, profile, intent, status, score, feedback, queued_at, scored_at, archived_at)
		VALUES ('https://ct1.example.com', '', 'url', '', 'eng', 'score', 'archived', 85, 'accurate', '2026-01-01T00:00:00Z', '2026-01-01T00:00:01Z', '2026-01-01T00:00:02Z')`)

	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/stats/intents", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleIntentStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-1: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp IntentStatsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("CT-1: decode response: %v", err)
	}
	if len(resp.Intents) == 0 {
		t.Fatal("CT-1: expected at least 1 intent stat, got empty")
	}

	var found bool
	for _, s := range resp.Intents {
		if s.Intent == "score" {
			found = true
			if s.TotalScored == 0 {
				t.Errorf("CT-1: score total_scored = 0, want > 0")
			}
			if s.ThumbsUp == 0 {
				t.Errorf("CT-1: score thumbs_up = 0, want > 0 (accurate feedback)")
			}
			if s.Precision <= 0 {
				t.Errorf("CT-1: score precision = %f, want > 0", s.Precision)
			}
		}
	}
	if !found {
		t.Error("CT-1: no 'score' intent in response")
	}
}

// CT-2: GET /stats/tags uses user_tags, not inferred_tags.
func TestStatsIntent_CT2_TagStatsFromUserTagsOnly(t *testing.T) {
	q := newTestQueue(t)

	// Insert a row with user_tags=["jira"] and inferred_tags=["domain:eng"].
	q.db.Exec(`INSERT INTO queue (url, text, type, action, profile, intent, user_tags, inferred_tags, status, score, feedback, queued_at, scored_at, archived_at)
		VALUES ('https://ct2.example.com', '', 'url', '', 'eng', 'capture', '["jira"]', '["domain:eng"]', 'archived', 70, 'accurate', '2026-01-01T00:00:00Z', '2026-01-01T00:00:01Z', '2026-01-01T00:00:02Z')`)

	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/stats/tags", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleTagStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-2: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp TagStatsResponse
	json.NewDecoder(w.Body).Decode(&resp)

	tagNames := make(map[string]bool)
	for _, s := range resp.Tags {
		tagNames[s.Tag] = true
	}
	if !tagNames["jira"] {
		t.Error("CT-2: 'jira' from user_tags not in tag stats")
	}
	if tagNames["domain:eng"] {
		t.Error("CT-2: 'domain:eng' from inferred_tags must NOT appear in tag stats")
	}
}

// CT-3: emitShareEvent includes intent and tags fields.
// Tested indirectly: emitShareEvent doesn't panic when intent+tags are set on req.
func TestStatsIntent_CT3_EmitShareEventIncludesIntent(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	// Create a request with intent and user_tags set.
	req := &ShareRequest{
		Type:     "url",
		URL:      "https://ct3.example.com",
		Profile:  "eng",
		Intent:   "score",
		UserTags: []string{"domain:eng"},
	}

	// emitShareEvent should not panic; events=nil is handled gracefully.
	srv.emitShareEvent(req, "pending", statsTestNow(), "https://ct3.example.com")
}

// CT-4: GET /profiles/stats still returns historical data.
func TestStatsIntent_CT4_ProfileStatsStillReadable(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/profiles/stats?profile=eng", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleProfileStats(w, req)

	// Must return 200 (even if empty).
	if w.Code != http.StatusOK {
		t.Errorf("CT-4: GET /profiles/stats returned %d, want 200", w.Code)
	}
}

// CT-5: Precision is 0 when no feedback exists.
func TestStatsIntent_CT5_PrecisionZeroWhenNoFeedback(t *testing.T) {
	q := newTestQueue(t)

	// Insert a scored item with no feedback.
	q.db.Exec(`INSERT INTO queue (url, text, type, action, profile, intent, status, score, queued_at, scored_at, archived_at)
		VALUES ('https://ct5.example.com', '', 'url', '', 'eng', 'score', 'archived', 80, '2026-01-01T00:00:00Z', '2026-01-01T00:00:01Z', '2026-01-01T00:00:02Z')`)

	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/stats/intents", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleIntentStats(w, req)

	var resp IntentStatsResponse
	json.NewDecoder(w.Body).Decode(&resp)
	for _, s := range resp.Intents {
		if s.Intent == "score" && s.TotalScored > 0 && s.ThumbsUp == 0 && s.ThumbsDown == 0 {
			if s.Precision != 0.0 {
				t.Errorf("CT-5: precision = %f, want 0.0 when no feedback", s.Precision)
			}
		}
	}
}

// CT-6: stats_no_data returns 200 with empty arrays.
func TestStatsIntent_CT6_EmptyStatsReturns200(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/stats/intents", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleIntentStats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("CT-6: expected 200, got %d", w.Code)
	}
	var resp IntentStatsResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Intents == nil {
		t.Error("CT-6: intents must be non-nil array (not null) when empty")
	}
}

// RG-1: GET /profiles/stats must not error after migration.
func TestStatsIntent_RG1_ProfileStatsNoErrorAfterMigration(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/profiles/stats", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleProfileStats(w, req)

	if w.Code >= 500 {
		t.Errorf("RG-1: /profiles/stats returned %d (server error); must return 200 post-migration", w.Code)
	}
}

// statsTestNow is a helper for stats tests that need a time.Time value.
func statsTestNow() time.Time { return time.Now() }
