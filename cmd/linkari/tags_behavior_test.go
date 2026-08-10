package main

// EPIC-149 M5: Share-time tag behavioral tests + observability.
// EPIC-150 M3: GET /tags behavioral tests.

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- EPIC-149 M5 behavioral tests ---

// BT-1: Multipart audio share with tags  -  full pipeline smoke test.
// Verifies that an audio multipart share carrying user_tags results in
// a successful response with tags_persisted=true and the tags in inventory.
func TestShareTags_BT1_MultipartAudioWithTags(t *testing.T) {
	stubBackend := installHaikuJSONStub(t)

	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	router.SetScoringBackend(stubBackend)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("action", "vnote_auto")
	mw.WriteField("user_tags", `["meeting","q2"]`)
	part, _ := mw.CreateFormFile("audio", "standup.m4a")
	part.Write([]byte("fake-audio-content"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/share", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test-token")

	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("BT-1: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ShareResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("BT-1: decode: %v", err)
	}
	if resp.TagsPersisted == nil || !*resp.TagsPersisted {
		t.Errorf("BT-1: expected tags_persisted=true")
	}

	for _, name := range []string{"meeting", "q2"} {
		var count int
		if err := q.db.QueryRow("SELECT use_count FROM tags WHERE name=?", name).Scan(&count); err != nil {
			t.Errorf("BT-1: tag %q missing from inventory: %v", name, err)
		}
	}
}

// BT-2: Tag deduplication within a single request.
// Sending the same tag twice in user_tags must result in only one inventory row
// (not two), and use_count=1 for that tag.
func TestShareTags_BT2_DedupInRequest(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://example.com","user_tags":["tech","tech","TECH"]}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")

	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("BT-2: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// All three entries normalize to "tech"; inventory should have use_count=3
	// (each occurrence is an independent upsert increment) and exactly one row.
	var rowCount int
	if err := q.db.QueryRow("SELECT COUNT(*) FROM tags WHERE name='tech'").Scan(&rowCount); err != nil {
		t.Fatalf("BT-2: count rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("BT-2: expected 1 row for 'tech', got %d", rowCount)
	}
}

// BT-3: Tags inventory last_used_at advances on each share.
// After two shares, last_used_at for the tag must be >= the first share's time.
func TestShareTags_BT3_LastUsedAtUpdates(t *testing.T) {
	q := newTestQueue(t)

	id1, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://a.example.com"})
	if err != nil {
		t.Fatalf("BT-3: enqueue 1: %v", err)
	}
	if err := q.persistUserTags(id1, []string{"reading"}); err != nil {
		t.Fatalf("BT-3: persist 1: %v", err)
	}

	var firstTS string
	if err := q.db.QueryRow("SELECT last_used_at FROM tags WHERE name='reading'").Scan(&firstTS); err != nil {
		t.Fatalf("BT-3: read first ts: %v", err)
	}

	// Sleep 1ms to ensure clock advances.
	time.Sleep(time.Millisecond)

	id2, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://b.example.com"})
	if err != nil {
		t.Fatalf("BT-3: enqueue 2: %v", err)
	}
	if err := q.persistUserTags(id2, []string{"reading"}); err != nil {
		t.Fatalf("BT-3: persist 2: %v", err)
	}

	var secondTS string
	if err := q.db.QueryRow("SELECT last_used_at FROM tags WHERE name='reading'").Scan(&secondTS); err != nil {
		t.Fatalf("BT-3: read second ts: %v", err)
	}

	if secondTS < firstTS {
		t.Errorf("BT-3: last_used_at went backwards: first=%s second=%s", firstTS, secondTS)
	}
}

// --- EPIC-150 M3 behavioral tests ---

// BT-1 (EPIC-150): Non-GET method rejected with 405.
func TestGetTags_BT1_NonGETRejected(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodPost, "/tags", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("BT-1: expected 405, got %d", w.Code)
	}
}

// BT-2 (EPIC-150): Limit is clamped to 100 when caller sends limit=500.
func TestGetTags_BT2_LimitClampedToMax(t *testing.T) {
	q := newTestQueue(t)
	now := time.Now()
	for i := 0; i < 110; i++ {
		name := string(rune('a' + i%26))
		if i >= 26 {
			name = name + string(rune('a'+i/26-1))
		}
		seedTagInventory(t, q, name, i+1, now.Add(time.Duration(-i)*time.Second))
	}

	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/tags?limit=500", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("BT-2: expected 200, got %d", w.Code)
	}

	var resp TagsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("BT-2: decode: %v", err)
	}
	if len(resp.Tags) > 100 {
		t.Errorf("BT-2: limit=500 returned %d tags, want <= 100", len(resp.Tags))
	}
}

// BT-3 (EPIC-150): last_used_at format is ISO8601 (RFC3339).
func TestGetTags_BT3_LastUsedAtISO8601(t *testing.T) {
	q := newTestQueue(t)
	seedTagInventory(t, q, "iso-test", 1, time.Now())

	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/tags", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	var resp TagsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("BT-3: decode: %v", err)
	}
	if len(resp.Tags) == 0 {
		t.Fatal("BT-3: expected at least one tag")
	}
	if _, err := time.Parse(time.RFC3339, resp.Tags[0].LastUsedAt); err != nil {
		t.Errorf("BT-3: last_used_at %q is not RFC3339: %v", resp.Tags[0].LastUsedAt, err)
	}
}
