package main

// EPIC-149 M1: Share-time tag server persistence contract tests.
// CT-1 through CT-8 assert the desired behavior of user_tags on POST /share.
// Written before implementation (TDD); tests compile and pass once M2-M4 land.

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// CT-1: user_tags accepted in JSON POST /share.
// Verifies: tags_persisted=true in response, user_tags persisted on queue row,
// and both tags present in the inventory table.
func TestShareTags_CT1_JSON(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://example.com/article","user_tags":["tech","ai"]}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")

	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-1: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ShareResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("CT-1: decode response: %v", err)
	}
	if resp.TagsPersisted == nil || !*resp.TagsPersisted {
		t.Errorf("CT-1: expected tags_persisted=true, got %v", resp.TagsPersisted)
	}
	if resp.ID == 0 {
		t.Fatal("CT-1: expected non-zero queue ID in response")
	}

	// Verify user_tags stored on queue row.
	var gotJSON string
	if err := q.db.QueryRow("SELECT user_tags FROM queue WHERE id = ?", resp.ID).Scan(&gotJSON); err != nil {
		t.Fatalf("CT-1: query user_tags: %v", err)
	}
	var tags []string
	if err := json.Unmarshal([]byte(gotJSON), &tags); err != nil {
		t.Fatalf("CT-1: unmarshal user_tags %q: %v", gotJSON, err)
	}
	if len(tags) != 2 || tags[0] != "tech" || tags[1] != "ai" {
		t.Errorf("CT-1: queue row user_tags = %v, want [tech ai]", tags)
	}

	// Verify both tags appear in the inventory table.
	for _, name := range []string{"tech", "ai"} {
		var useCount int
		if err := q.db.QueryRow("SELECT use_count FROM tags WHERE name = ?", name).Scan(&useCount); err != nil {
			t.Errorf("CT-1: tags inventory missing %q: %v", name, err)
			continue
		}
		if useCount < 1 {
			t.Errorf("CT-1: tags inventory use_count for %q = %d, want >= 1", name, useCount)
		}
	}
}

// CT-2: user_tags accepted in multipart POST /share.
// Verifies: tags_persisted=true when user_tags sent as a JSON-array field in
// a multipart request alongside an audio file part.
func TestShareTags_CT2_Multipart(t *testing.T) {
	stubBackend := installHaikuJSONStub(t)

	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	router.SetScoringBackend(stubBackend)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("action", "vnote_auto")
	mw.WriteField("user_tags", `["podcast","notes"]`)
	part, _ := mw.CreateFormFile("audio", "test.m4a")
	part.Write([]byte("fake-audio-bytes"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/share", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test-token")

	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-2: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ShareResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("CT-2: decode response: %v", err)
	}
	if resp.TagsPersisted == nil || !*resp.TagsPersisted {
		t.Errorf("CT-2: expected tags_persisted=true, got %v", resp.TagsPersisted)
	}
}

// CT-3: Missing user_tags field is backward compatible.
// Verifies: 200 response, tags_persisted absent (nil) in JSON output.
func TestShareTags_CT3_BackwardCompat(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://example.com/article"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")

	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-3: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ShareResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("CT-3: decode response: %v", err)
	}
	if resp.TagsPersisted != nil {
		t.Errorf("CT-3: expected tags_persisted absent/nil, got %v", *resp.TagsPersisted)
	}

	// Raw JSON must not include "tags_persisted" key at all.
	raw := w.Body.Bytes()
	if bytes.Contains(raw, []byte("tags_persisted")) {
		t.Errorf("CT-3: response JSON contains tags_persisted key: %s", raw)
	}
}

// CT-4: Tag normalization  -  trim whitespace and lowercase.
func TestShareTags_CT4_Normalization(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"  Tech  ", "tech"},
		{"AI", "ai"},
		{"\tGo\n", "go"},
		{"Machine Learning", "machine learning"},
		{"already-normalized", "already-normalized"},
	}
	for _, tc := range cases {
		got := normalizeTag(tc.input)
		if got != tc.want {
			t.Errorf("CT-4: normalizeTag(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// CT-5: validateUserTags returns an error for any empty tag.
func TestShareTags_CT5_RejectsEmpty(t *testing.T) {
	if err := validateUserTags([]string{""}); err == nil {
		t.Error("CT-5: expected error for empty tag, got nil")
	}
	if err := validateUserTags([]string{"valid", ""}); err == nil {
		t.Error("CT-5: expected error for slice containing empty tag, got nil")
	}
	if err := validateUserTags([]string{"valid"}); err != nil {
		t.Errorf("CT-5: unexpected error for valid tag: %v", err)
	}
}

// CT-6: validateUserTags returns an error for any tag exceeding 50 chars.
func TestShareTags_CT6_RejectsTooLong(t *testing.T) {
	long := strings.Repeat("a", 51)
	if err := validateUserTags([]string{long}); err == nil {
		t.Errorf("CT-6: expected error for %d-char tag, got nil", len(long))
	}
	exactly50 := strings.Repeat("b", 50)
	if err := validateUserTags([]string{exactly50}); err != nil {
		t.Errorf("CT-6: unexpected error for 50-char tag: %v", err)
	}
}

// CT-7: Tags inventory upserts correctly  -  two shares with the same tag yield
// use_count=2 in the tags table, not two separate rows.
func TestShareTags_CT7_InventoryUpsert(t *testing.T) {
	q := newTestQueue(t)

	id1, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://a.example.com"})
	if err != nil {
		t.Fatalf("CT-7: enqueue id1: %v", err)
	}
	id2, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://b.example.com"})
	if err != nil {
		t.Fatalf("CT-7: enqueue id2: %v", err)
	}

	if err := q.persistUserTags(id1, []string{"tech"}); err != nil {
		t.Fatalf("CT-7: persistUserTags id1: %v", err)
	}
	if err := q.persistUserTags(id2, []string{"tech"}); err != nil {
		t.Fatalf("CT-7: persistUserTags id2: %v", err)
	}

	var useCount int
	if err := q.db.QueryRow("SELECT use_count FROM tags WHERE name = ?", "tech").Scan(&useCount); err != nil {
		t.Fatalf("CT-7: query use_count: %v", err)
	}
	if useCount != 2 {
		t.Errorf("CT-7: use_count = %d, want 2", useCount)
	}

	// Confirm only one row exists for "tech" (no duplicate insertion).
	var rowCount int
	if err := q.db.QueryRow("SELECT COUNT(*) FROM tags WHERE name = ?", "tech").Scan(&rowCount); err != nil {
		t.Fatalf("CT-7: count rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("CT-7: tag row count = %d, want 1 (upsert dedup)", rowCount)
	}
}

// CT-8: Tag persist failure does not block share.
// Drops the tags table to force persistUserTags to fail, then verifies the
// share is still enqueued and the response carries tags_persisted=false.
func TestShareTags_CT8_PersistFailureNonBlocking(t *testing.T) {
	q := newTestQueue(t)
	q.db.Exec("DROP TABLE IF EXISTS tags")

	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://example.com/article","user_tags":["tech"]}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")

	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-8: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ShareResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("CT-8: decode response: %v", err)
	}
	if resp.Status == "error" {
		t.Errorf("CT-8: share failed when tags persist failed: %s", resp.Message)
	}
	if resp.TagsPersisted == nil || *resp.TagsPersisted {
		t.Errorf("CT-8: expected tags_persisted=false on persist failure, got %v", resp.TagsPersisted)
	}
	if resp.ID == 0 {
		t.Error("CT-8: expected non-zero queue ID (share must still be enqueued)")
	}
}
