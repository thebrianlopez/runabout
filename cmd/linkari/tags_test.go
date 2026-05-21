package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- helpers ---

func newTagsServer(t *testing.T) (http.Handler, *Queue) {
	t.Helper()
	q := newTestQueue(t)
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)
	return srv.Mux(), q
}

func shareJSON(t *testing.T, mux http.Handler, body string) (int, ShareResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/share", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var resp ShareResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	return w.Code, resp
}

func shareMultipartWithFile(t *testing.T, mux http.Handler, fields map[string]string) (int, ShareResponse) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	// The handler requires a file part; provide a minimal dummy audio file.
	fw, err := mw.CreateFormFile("audio", "test.m4a")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = fw.Write([]byte("dummy audio content"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/share", &buf)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var resp ShareResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	return w.Code, resp
}

// --- EPIC-149 CT: contract tests ---

// CT-1: user_tags accepted in JSON POST /share
func TestTagsCT1_JSONUserTags(t *testing.T) {
	mux, q := newTagsServer(t)
	code, resp := shareJSON(t, mux, `{"type":"text","text":"hello","user_tags":["work","reading"]}`)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.ID == 0 {
		t.Fatal("expected non-zero queue ID")
	}
	if resp.TagsPersisted == nil || !*resp.TagsPersisted {
		t.Errorf("tags_persisted = %v, want true", resp.TagsPersisted)
	}

	var stored string
	if err := q.db.QueryRow("SELECT user_tags FROM queue WHERE id = ?", resp.ID).Scan(&stored); err != nil {
		t.Fatalf("query user_tags: %v", err)
	}
	var got []string
	if err := json.Unmarshal([]byte(stored), &got); err != nil {
		t.Fatalf("unmarshal stored tags: %v", err)
	}
	want := []string{"work", "reading"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("stored tags = %v, want %v", got, want)
	}
}

// CT-2: user_tags accepted in multipart POST /share
func TestTagsCT2_MultipartUserTags(t *testing.T) {
	mux, q := newTagsServer(t)
	code, resp := shareMultipartWithFile(t, mux, map[string]string{
		"user_tags": `["work","reading"]`,
	})

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.ID == 0 {
		t.Fatal("expected non-zero queue ID")
	}
	if resp.TagsPersisted == nil || !*resp.TagsPersisted {
		t.Errorf("tags_persisted = %v, want true", resp.TagsPersisted)
	}

	var stored string
	if err := q.db.QueryRow("SELECT user_tags FROM queue WHERE id = ?", resp.ID).Scan(&stored); err != nil {
		t.Fatalf("query user_tags: %v", err)
	}
	if stored == "" || stored == "[]" {
		t.Errorf("expected user_tags to be stored, got %q", stored)
	}
}

// CT-3: missing user_tags is backward compatible
func TestTagsCT3_MissingUserTagsBackwardCompat(t *testing.T) {
	mux, q := newTagsServer(t)
	code, resp := shareJSON(t, mux, `{"type":"text","text":"hello"}`)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.TagsPersisted != nil {
		t.Errorf("tags_persisted should be nil when no user_tags sent, got %v", *resp.TagsPersisted)
	}

	// Verify queue row has empty user_tags (not an error)
	if resp.ID > 0 {
		var stored string
		if err := q.db.QueryRow("SELECT user_tags FROM queue WHERE id = ?", resp.ID).Scan(&stored); err != nil && err != sql.ErrNoRows {
			t.Fatalf("query: %v", err)
		}
		// empty string is fine
	}
}

// CT-4: tag normalization (trim + lowercase)
func TestTagsCT4_TagNormalization(t *testing.T) {
	if got := normalizeTag("  WORK  "); got != "work" {
		t.Errorf("normalizeTag(' WORK ') = %q, want %q", got, "work")
	}
	if got := normalizeTag("Reading"); got != "reading" {
		t.Errorf("normalizeTag('Reading') = %q, want %q", got, "reading")
	}
	if got := normalizeTag("AI/ML"); got != "ai/ml" {
		t.Errorf("normalizeTag('AI/ML') = %q, want %q", got, "ai/ml")
	}

	// Also verify normalization happens end-to-end via the share endpoint.
	mux, q := newTagsServer(t)
	code, resp := shareJSON(t, mux, `{"type":"text","text":"hello","user_tags":["  WORK  ","Reading"]}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	var stored string
	if err := q.db.QueryRow("SELECT user_tags FROM queue WHERE id = ?", resp.ID).Scan(&stored); err != nil {
		t.Fatalf("query: %v", err)
	}
	var got []string
	if err := json.Unmarshal([]byte(stored), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got[0] != "work" || got[1] != "reading" {
		t.Errorf("stored normalized tags = %v, want [work reading]", got)
	}
}

// CT-5: tag validation rejects empty (after normalization)
func TestTagsCT5_EmptyTagRejected(t *testing.T) {
	if err := validateUserTags([]string{"work", ""}); err == nil {
		t.Error("expected error for empty tag, got nil")
	}

	mux, _ := newTagsServer(t)
	// Whitespace-only tag normalizes to "" and should be rejected.
	code, _ := shareJSON(t, mux, `{"type":"text","text":"hello","user_tags":["work",""]}`)
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for empty tag", code)
	}
}

// CT-6: tag validation rejects tags > 50 chars
func TestTagsCT6_TooLongTagRejected(t *testing.T) {
	longTag := strings.Repeat("a", 51)
	if err := validateUserTags([]string{longTag}); err == nil {
		t.Error("expected error for 51-char tag, got nil")
	}
	if err := validateUserTags([]string{strings.Repeat("a", 50)}); err != nil {
		t.Errorf("expected no error for 50-char tag, got %v", err)
	}

	mux, _ := newTagsServer(t)
	code, _ := shareJSON(t, mux, fmt.Sprintf(`{"type":"text","text":"hello","user_tags":["%s"]}`, longTag))
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for >50 char tag", code)
	}
}

// CT-7: tags inventory upsert with deduplication (use_count increments)
func TestTagsCT7_TagsInventoryUpsert(t *testing.T) {
	q := newTestQueue(t)

	// First share: insert tags
	id1, err := q.Enqueue(&ShareRequest{Type: "text", Text: "hello"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.persistUserTags(id1, []string{"work", "reading"}); err != nil {
		t.Fatalf("persistUserTags: %v", err)
	}

	// Second share: same tags → use_count should increment
	id2, err := q.Enqueue(&ShareRequest{Type: "text", Text: "world"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.persistUserTags(id2, []string{"work"}); err != nil {
		t.Fatalf("persistUserTags: %v", err)
	}

	var workCount, readingCount int
	if err := q.db.QueryRow("SELECT use_count FROM tags WHERE name = 'work'").Scan(&workCount); err != nil {
		t.Fatalf("query work: %v", err)
	}
	if err := q.db.QueryRow("SELECT use_count FROM tags WHERE name = 'reading'").Scan(&readingCount); err != nil {
		t.Fatalf("query reading: %v", err)
	}
	if workCount != 2 {
		t.Errorf("work use_count = %d, want 2", workCount)
	}
	if readingCount != 1 {
		t.Errorf("reading use_count = %d, want 1", readingCount)
	}
}

// CT-8: tag persist failure does not block share (share still queued, tags_persisted=false)
func TestTagsCT8_PersistFailureNonFatal(t *testing.T) {
	q := newTestQueue(t)
	// Drop the tags table to force persistUserTags to fail.
	if _, err := q.db.Exec("DROP TABLE tags"); err != nil {
		t.Fatalf("drop tags: %v", err)
	}

	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)
	mux := srv.Mux()

	code, resp := shareJSON(t, mux, `{"type":"text","text":"hello","user_tags":["work"]}`)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (share must not be blocked)", code)
	}
	if resp.TagsPersisted == nil || *resp.TagsPersisted {
		t.Errorf("tags_persisted = %v, want false on persist failure", resp.TagsPersisted)
	}
	// Share row must exist.
	if resp.ID == 0 {
		t.Error("expected non-zero queue ID  -  share must be enqueued even on tag persist failure")
	}
}

// --- EPIC-149 BT: behavioral tests ---

// BT-1: multipart audio share with tags persists correctly
func TestTagsBT1_MultipartAudioWithTags(t *testing.T) {
	mux, q := newTagsServer(t)
	code, resp := shareMultipartWithFile(t, mux, map[string]string{
		"user_tags": `["podcast","learning"]`,
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.TagsPersisted == nil || !*resp.TagsPersisted {
		t.Errorf("tags_persisted = %v, want true", resp.TagsPersisted)
	}

	var stored string
	if err := q.db.QueryRow("SELECT user_tags FROM queue WHERE id = ?", resp.ID).Scan(&stored); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !strings.Contains(stored, "podcast") {
		t.Errorf("expected 'podcast' in stored tags, got %q", stored)
	}
}

// BT-2: duplicate tags in request are stored as-is (dedup is caller responsibility)
func TestTagsBT2_DuplicateTagsInRequest(t *testing.T) {
	q := newTestQueue(t)
	id, _ := q.Enqueue(&ShareRequest{Type: "text", Text: "hello"})
	// Sending ["work","work"] - the DB upsert handles dedup at the tags inventory level.
	if err := q.persistUserTags(id, []string{"work", "work"}); err != nil {
		t.Fatalf("persistUserTags with duplicate: %v", err)
	}
	var count int
	if err := q.db.QueryRow("SELECT use_count FROM tags WHERE name = 'work'").Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	// Each occurrence increments use_count  -  2 for "work","work"
	if count != 2 {
		t.Errorf("use_count = %d for duplicate tags in one call, want 2", count)
	}
}

// BT-3: tags inventory last_used_at is updated on subsequent shares
func TestTagsBT3_LastUsedAtUpdated(t *testing.T) {
	q := newTestQueue(t)
	id1, _ := q.Enqueue(&ShareRequest{Type: "text", Text: "hello"})
	if err := q.persistUserTags(id1, []string{"work"}); err != nil {
		t.Fatalf("first persist: %v", err)
	}
	var first string
	if err := q.db.QueryRow("SELECT last_used_at FROM tags WHERE name = 'work'").Scan(&first); err != nil {
		t.Fatalf("query first: %v", err)
	}

	id2, _ := q.Enqueue(&ShareRequest{Type: "text", Text: "world"})
	if err := q.persistUserTags(id2, []string{"work"}); err != nil {
		t.Fatalf("second persist: %v", err)
	}
	var second string
	if err := q.db.QueryRow("SELECT last_used_at FROM tags WHERE name = 'work'").Scan(&second); err != nil {
		t.Fatalf("query second: %v", err)
	}

	// last_used_at must be a non-empty RFC3339 timestamp.
	if second == "" {
		t.Error("last_used_at should not be empty after second persist")
	}
	// Both should be valid timestamps (second >= first, though may be equal if fast).
	if second < first {
		t.Errorf("last_used_at went backwards: first=%s second=%s", first, second)
	}
}
