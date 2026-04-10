package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedHistoryQueue inserts a deterministic mix of pending/scored/archived
// rows for /queue and /archive cursor + filter testing. Returns the server
// mux ready to serve. Rows are inserted oldest-first, so id DESC yields the
// reverse order.
func seedHistoryQueue(t *testing.T) (http.Handler, *Queue) {
	t.Helper()
	q := newTestQueue(t)

	// Insert 5 scored (default), 3 archived (default), 2 archived (eng),
	// 2 pending. 12 rows total; ids 1..12.
	mustEnq := func(profile string) int64 {
		id, err := q.Enqueue(&ShareRequest{URL: "https://example.com/" + profile, Type: "url", Profile: profile})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	for i := 0; i < 5; i++ {
		id := mustEnq("default")
		if err := q.UpdateScore(id, 70+i, "tag", "verdict body long enough to pass sanity check", "slug"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		id := mustEnq("default")
		if err := q.UpdateScore(id, 80, "tag", "verdict body long enough", "slug"); err != nil {
			t.Fatal(err)
		}
		if err := q.Archive(id); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		id := mustEnq("eng")
		if err := q.UpdateScore(id, 90, "tag", "verdict body long enough", "slug"); err != nil {
			t.Fatal(err)
		}
		if err := q.Archive(id); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		mustEnq("default")
	}
	// EPIC-057: 2 ginit-sourced scored rows for ?type= filter testing.
	for i := 0; i < 2; i++ {
		id, err := q.EnqueueScored(&ShareRequest{
			Action:  "ginit_eng",
			Profile: "eng",
			Type:    "text",
			Text:    fmt.Sprintf("PROJ-%d", 100+i),
		}, "workspace_bootstrapped")
		if err != nil {
			t.Fatal(err)
		}
		_ = id
	}

	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)
	return srv.Mux(), q
}

func doGet(t *testing.T, mux http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func decodeItems(t *testing.T, w *httptest.ResponseRecorder) []QueueItem {
	t.Helper()
	var items []QueueItem
	if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
		t.Fatalf("decode: %v (body=%q)", err, w.Body.String())
	}
	return items
}

func TestArchiveBackCompatProfileOnly(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	w := doGet(t, mux, "/archive?profile=default", "test-token")
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	items := decodeItems(t, w)
	if len(items) != 3 {
		t.Errorf("want 3 default-archived, got %d", len(items))
	}
	for _, it := range items {
		if it.Status != "archived" || it.Profile != "default" {
			t.Errorf("leaked row: %+v", it)
		}
	}
}

func TestArchiveStatusScored(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	w := doGet(t, mux, "/archive?status=scored&limit=10", "test-token")
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	items := decodeItems(t, w)
	// 5 url-scored + 2 ginit auto-scored = 7 total scored rows.
	if len(items) != 7 {
		t.Errorf("want 7 scored, got %d", len(items))
	}
	for _, it := range items {
		if it.Status != "scored" {
			t.Errorf("want scored, got %q", it.Status)
		}
	}
}

func TestArchiveCursorBeforeID(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	// ids 6..8 are the default-archived rows; before_id=8 limit=5 → ids<8 archived = 6,7
	w := doGet(t, mux, "/archive?status=archived&before_id=8&limit=5", "test-token")
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	items := decodeItems(t, w)
	for _, it := range items {
		if it.ID >= 8 {
			t.Errorf("cursor violated: id=%d", it.ID)
		}
	}
	if len(items) >= 2 && items[0].ID <= items[1].ID {
		t.Errorf("expected id DESC, got %d then %d", items[0].ID, items[1].ID)
	}
}

func TestArchiveInvalidStatus(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	w := doGet(t, mux, "/archive?status=bogus", "test-token")
	if w.Code != 400 {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestArchiveInvalidLimit(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	for _, l := range []string{"-1", "0", "500", "abc"} {
		w := doGet(t, mux, "/archive?limit="+l, "test-token")
		if w.Code != 400 {
			t.Errorf("limit=%s want 400, got %d", l, w.Code)
		}
	}
}

func TestArchiveInvalidBeforeID(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	w := doGet(t, mux, "/archive?before_id=abc", "test-token")
	if w.Code != 400 {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestArchiveUnauthorized(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	w := doGet(t, mux, "/archive", "")
	if w.Code != 401 {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestArchiveEmptyEncodesArray(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	// before_id=1 → nothing
	w := doGet(t, mux, "/archive?before_id=1", "test-token")
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	body := w.Body.String()
	if body == "null\n" || body == "null" {
		t.Errorf("empty result encoded as null, want []: %q", body)
	}
	items := decodeItems(t, w)
	if len(items) != 0 {
		t.Errorf("want 0 items, got %d", len(items))
	}
}

func TestQueueStatusPending(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	w := doGet(t, mux, "/queue?status=pending&limit=5", "test-token")
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	items := decodeItems(t, w)
	if len(items) != 2 {
		t.Errorf("want 2 pending, got %d", len(items))
	}
}

func TestQueueCursor(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	w := doGet(t, mux, "/queue?status=archived&before_id=50", "test-token")
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	items := decodeItems(t, w)
	for _, it := range items {
		if it.ID >= 50 {
			t.Errorf("cursor violated: %d", it.ID)
		}
		if it.Status != "archived" {
			t.Errorf("want archived, got %q", it.Status)
		}
	}
}

func TestQueueStatusAll(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	w := doGet(t, mux, "/queue?status=all&limit=100", "test-token")
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	items := decodeItems(t, w)
	if len(items) != 14 {
		t.Errorf("want 14, got %d", len(items))
	}
}

// EPIC-057: ?type=jira returns only ginit_* rows; ?type=url excludes them.
func TestArchiveTypeFilterJira(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	w := doGet(t, mux, "/archive?status=scored&type=jira&limit=50", "test-token")
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	items := decodeItems(t, w)
	if len(items) != 2 {
		t.Errorf("want 2 jira-scored rows, got %d", len(items))
	}
	for _, it := range items {
		if it.Action != "ginit_eng" {
			t.Errorf("type=jira leaked non-ginit row: action=%q", it.Action)
		}
	}
}

func TestArchiveTypeFilterURL(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	w := doGet(t, mux, "/archive?status=scored&type=url&limit=50", "test-token")
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	items := decodeItems(t, w)
	// 5 scored url rows (default profile), 0 ginit
	if len(items) != 5 {
		t.Errorf("want 5 url-scored rows, got %d", len(items))
	}
	for _, it := range items {
		if it.Action == "ginit_eng" {
			t.Errorf("type=url should exclude ginit rows: %+v", it)
		}
	}
}

func TestArchiveTypeFilterInvalid(t *testing.T) {
	mux, _ := seedHistoryQueue(t)
	w := doGet(t, mux, "/archive?type=bogus", "test-token")
	if w.Code != 400 {
		t.Errorf("want 400 for invalid type, got %d", w.Code)
	}
}

var _ = fmt.Sprintf
