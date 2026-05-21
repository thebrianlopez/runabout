package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTagsAPIServer creates a test server with a real queue for GET /tags tests.
func newTagsAPIServer(t *testing.T) (http.Handler, *Queue) {
	t.Helper()
	q := newTestQueue(t)
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)
	return srv.Mux(), q
}

// getTags calls GET /tags with the test token and decodes the TagsResponse.
func getTags(t *testing.T, mux http.Handler, query string) (int, []TagItem) {
	t.Helper()
	path := "/tags"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var resp TagsResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	return w.Code, resp.Tags
}

// seedTags calls persistUserTags N times per tag to build up use_count.
func seedTags(t *testing.T, q *Queue, tags map[string]int) {
	t.Helper()
	for name, count := range tags {
		for i := 0; i < count; i++ {
			id, err := q.Enqueue(&ShareRequest{Type: "text", Text: "seed"})
			if err != nil {
				t.Fatalf("enqueue for seed: %v", err)
			}
			if err := q.persistUserTags(id, []string{name}); err != nil {
				t.Fatalf("persistUserTags seed: %v", err)
			}
		}
	}
}

// --- EPIC-150 BT: behavioral tests ---

// BT-1: Non-GET method returns 405 (Go 1.22 method-specific mux pattern).
func TestTagsAPIBT1_NonGETMethodRejected(t *testing.T) {
	mux, _ := newTagsAPIServer(t)
	req := httptest.NewRequest(http.MethodPost, "/tags", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for POST /tags", w.Code)
	}
}

// BT-2: limit query param is clamped to 100.
func TestTagsAPIBT2_LimitClampedTo100(t *testing.T) {
	mux, q := newTagsAPIServer(t)
	for i := 0; i < 105; i++ {
		id, _ := q.Enqueue(&ShareRequest{Type: "text", Text: "x"})
		q.persistUserTags(id, []string{tagName(i)}) //nolint:errcheck
	}

	code, items := getTags(t, mux, "limit=999")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(items) != 100 {
		t.Errorf("got %d items with limit=999, want 100 (clamped)", len(items))
	}
}

// BT-3: last_used_at values are RFC3339 (ISO8601) timestamps.
func TestTagsAPIBT3_LastUsedAtISO8601(t *testing.T) {
	mux, q := newTagsAPIServer(t)
	id, _ := q.Enqueue(&ShareRequest{Type: "text", Text: "x"})
	q.persistUserTags(id, []string{"ts-test"}) //nolint:errcheck

	_, items := getTags(t, mux, "")
	if len(items) == 0 {
		t.Fatal("expected at least one tag")
	}
	for _, item := range items {
		if _, err := time.Parse(time.RFC3339, item.LastUsedAt); err != nil {
			t.Errorf("last_used_at %q is not RFC3339: %v", item.LastUsedAt, err)
		}
	}
}

func tagName(i int) string {
	return "tag-" + string(rune('a'+i%26)) + "-" + string(rune('0'+i/26))
}
