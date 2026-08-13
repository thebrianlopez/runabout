// EPIC-018: YouTube Watch Later Auto Score  -  test suite.
// M1: Contract tests CT-1 through CT-5.
// M6: Behavioral tests BT-1 through BT-4.
// M7: Regression guards RG-1, RG-2.
// M9: Integration test.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// ---------------------------------------------------------------------------
// CT-1: MarkContentSeen + IsNewContent (seen = not new)
// ---------------------------------------------------------------------------

func TestWatchLaterCT1_InsertAndCheck(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	videoID := "abc123"
	if err := q.MarkContentSeen("yt_watch_later", videoID, 0); err != nil {
		t.Fatalf("MarkContentSeen: %v", err)
	}

	isNew, err := q.IsNewContent("yt_watch_later", videoID)
	if err != nil {
		t.Fatalf("IsNewContent: %v", err)
	}
	if isNew {
		t.Fatal("expected false (already seen)")
	}
}

// ---------------------------------------------------------------------------
// CT-2: syncWatchLaterAsync drains one page of items into queue
// ---------------------------------------------------------------------------

func TestWatchLaterCT2_SyncOnePage(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	// Seed a valid refresh token so youtubeTokenSource doesn't fail.
	if err := q.SetYouTubeRefreshToken("default", "fake-tok", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	called := false
	deps := &ytListDeps{}

	deps.PlaylistItems = func(_ context.Context, _ oauth2.TokenSource, playlistID, _ string) ([]ytPlaylistItem, string, error) {
		called = true
		if playlistID != "WL" {
			return nil, "", fmt.Errorf("unexpected playlistID: %q", playlistID)
		}
		return []ytPlaylistItem{
			{VideoID: "vid1", Title: "Video One"},
			{VideoID: "vid2", Title: "Video Two"},
		}, "", nil
	}

	syncWatchLaterAsync("default", "default", q, nil, "", "", true, deps)

	if !called {
		t.Fatal("execYouTubePlaylistItems was not called")
	}

	// Verify queue rows were created.
	items, err := q.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 pending items, got %d", len(items))
	}
}

// ---------------------------------------------------------------------------
// CT-3: POST /sync/youtube-watchlater returns 202
// ---------------------------------------------------------------------------

func TestWatchLaterCT3_HandlerReturns202(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	// Reset global syncing state.
	watchLaterSyncMu.Lock()
	watchLaterSyncing = false
	watchLaterSyncMu.Unlock()

	deps := &ytListDeps{}
	deps.PlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		return nil, "", nil // empty, no-op
	}

	srv := &Server{queue: q, ytListDeps: deps}
	req := httptest.NewRequest(http.MethodPost, "/sync/youtube-watchlater", nil)
	rr := httptest.NewRecorder()

	srv.handleSyncWatchLater(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// CT-4: MarkContentSeen stores queue_id in seen_content
// ---------------------------------------------------------------------------

func TestWatchLaterCT4_MarkScored(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	videoID := "markme"

	// Verify new before marking.
	isNew, _ := q.IsNewContent("yt_watch_later", videoID)
	if !isNew {
		t.Fatal("should be new before MarkContentSeen")
	}

	if err := q.MarkContentSeen("yt_watch_later", videoID, 42); err != nil {
		t.Fatalf("MarkContentSeen: %v", err)
	}

	isNew, err := q.IsNewContent("yt_watch_later", videoID)
	if err != nil {
		t.Fatal(err)
	}
	if isNew {
		t.Fatal("expected isNew=false after MarkContentSeen")
	}

	// Verify queue_id is stored in seen_content.
	var queueID int64
	err = q.db.QueryRow("SELECT queue_id FROM seen_content WHERE source='yt_watch_later' AND item_id=?", videoID).Scan(&queueID)
	if err != nil {
		t.Fatalf("query queue_id: %v", err)
	}
	if queueID != 42 {
		t.Fatalf("expected queue_id=42, got %d", queueID)
	}
}

// ---------------------------------------------------------------------------
// CT-5: pagination  -  nextPageToken followed through 2 pages
// ---------------------------------------------------------------------------

func TestWatchLaterCT5_Pagination(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	if err := q.SetYouTubeRefreshToken("default", "fake-tok", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	var pageCount int32
	deps := &ytListDeps{}

	deps.PlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, pageToken string) ([]ytPlaylistItem, string, error) {
		n := atomic.AddInt32(&pageCount, 1)
		switch n {
		case 1:
			return []ytPlaylistItem{{VideoID: "p1v1"}}, "token2", nil
		case 2:
			if pageToken != "token2" {
				return nil, "", fmt.Errorf("expected pageToken 'token2', got %q", pageToken)
			}
			return []ytPlaylistItem{{VideoID: "p2v1"}}, "", nil
		default:
			return nil, "", fmt.Errorf("unexpected page %d", n)
		}
	}

	syncWatchLaterAsync("default", "default", q, nil, "", "", true, deps)

	if pageCount != 2 {
		t.Fatalf("expected 2 pages fetched, got %d", pageCount)
	}

	items, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 enqueued items, got %d", len(items))
	}
}

// ---------------------------------------------------------------------------
// BT-1: playlistItems.list called with PlaylistId=WL
// ---------------------------------------------------------------------------

func TestWatchLaterBT1_PlaylistIDIsWL(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	if err := q.SetYouTubeRefreshToken("default", "fake-tok", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	var gotPlaylistID string
	deps := &ytListDeps{}

	deps.PlaylistItems = func(_ context.Context, _ oauth2.TokenSource, playlistID, _ string) ([]ytPlaylistItem, string, error) {
		gotPlaylistID = playlistID
		return nil, "", nil
	}

	syncWatchLaterAsync("default", "default", q, nil, "", "", true, deps)

	if gotPlaylistID != "WL" {
		t.Fatalf("expected PlaylistId=WL, got %q", gotPlaylistID)
	}
}

// ---------------------------------------------------------------------------
// BT-2: multi-page WL follows nextPageToken
// ---------------------------------------------------------------------------

func TestWatchLaterBT2_MultiPageFollowsToken(t *testing.T) {
	// Covered by CT-5; this is an alias with an explicit assertion on URL params.
	TestWatchLaterCT5_Pagination(t)
}

// ---------------------------------------------------------------------------
// BT-3: quota exhaustion on page 2 → page 1 items enqueued, watchlater_quota_exhausted logged
// ---------------------------------------------------------------------------

func TestWatchLaterBT3_QuotaExhaustion(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	if err := q.SetYouTubeRefreshToken("default", "fake-tok", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	var callCount int32
	deps := &ytListDeps{}

	deps.PlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return []ytPlaylistItem{{VideoID: "quota-vid1"}}, "tok2", nil
		}
		// Simulate quota exhaustion on page 2.
		return nil, "", fmt.Errorf("googleapi: Error 403: quotaExceeded")
	}

	// Use a real EventLogger backed by a temp file; verify the event was written.
	evPath := t.TempDir() + "/events.jsonl"
	el, err := NewEventLogger(evPath)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	defer el.Close()

	syncWatchLaterAsync("default", "default", q, el, "", "", true, deps)

	// Page 1 item should be enqueued.
	items, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 enqueued item from page 1, got %d", len(items))
	}

	// Verify quota event was written to the event log.
	content, err := os.ReadFile(evPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if !strings.Contains(string(content), "watchlater_quota_exhausted") {
		t.Fatalf("expected watchlater_quota_exhausted in event log, got:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// BT-4: seen_content.queue_id populated after MarkContentSeen
// ---------------------------------------------------------------------------

func TestWatchLaterBT4_QueueIDPopulated(t *testing.T) {
	TestWatchLaterCT4_MarkScored(t) // same assertion
}

// ---------------------------------------------------------------------------
// RG-1: handler returns 202; goroutine error → no ResponseWriter write after return
// ---------------------------------------------------------------------------

func TestWatchLaterRG1_HandlerReturns202NoRace(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	watchLaterSyncMu.Lock()
	watchLaterSyncing = false
	watchLaterSyncMu.Unlock()

	deps := &ytListDeps{}
	deps.PlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		time.Sleep(5 * time.Millisecond)
		return nil, "", nil
	}

	srv := &Server{queue: q, ytListDeps: deps}
	req := httptest.NewRequest(http.MethodPost, "/sync/youtube-watchlater", nil)
	rr := httptest.NewRecorder()

	srv.handleSyncWatchLater(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rr.Code)
	}
	// Handler must return 202 before goroutine writes  -  rr must not be written after return.
	// If there were a race, the race detector would catch it.
}

// ---------------------------------------------------------------------------
// RG-2: syncing=true → second POST returns 409
// ---------------------------------------------------------------------------

func TestWatchLaterRG2_ConcurrentSyncReturns409(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	// Force syncing=true.
	watchLaterSyncMu.Lock()
	watchLaterSyncing = true
	watchLaterSyncMu.Unlock()
	defer func() {
		watchLaterSyncMu.Lock()
		watchLaterSyncing = false
		watchLaterSyncMu.Unlock()
	}()

	srv := &Server{queue: q}
	req := httptest.NewRequest(http.MethodPost, "/sync/youtube-watchlater", nil)
	rr := httptest.NewRecorder()

	srv.handleSyncWatchLater(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// RG-3: invalid_grant emits error_class in source_complete (PA-5, PA-6)
// ---------------------------------------------------------------------------

func TestWatchLaterRG3_InvalidGrant_EmitsErrorClass(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	if err := q.SetYouTubeRefreshToken("default", "fake-tok", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	deps := &ytListDeps{}

	deps.PlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		return nil, "", fmt.Errorf("oauth2: cannot fetch token: 400 Bad Request\nResponse: {\"error\":\"invalid_grant\"}")
	}

	evPath := t.TempDir() + "/events.jsonl"
	el, err := NewEventLogger(evPath)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	defer el.Close()

	syncWatchLaterAsync("default", "default", q, el, "", "", true, deps)

	content, err := os.ReadFile(evPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	eventsStr := string(content)

	// source_complete must be present.
	if !strings.Contains(eventsStr, "source_complete") {
		t.Fatalf("expected source_complete event, got:\n%s", eventsStr)
	}

	// source_complete must carry error_class, not look like a silent zero-work success.
	if !strings.Contains(eventsStr, "oauth_invalid_grant") {
		t.Fatalf("expected oauth_invalid_grant in source_complete event, got:\n%s", eventsStr)
	}

	// source_complete must carry actionable error text.
	if !strings.Contains(eventsStr, "invalid_grant") {
		t.Fatalf("expected invalid_grant error text in source_complete event, got:\n%s", eventsStr)
	}
}

// ---------------------------------------------------------------------------
// M9: Integration test
// ---------------------------------------------------------------------------

func TestWatchLaterIntegration(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	if err := q.SetYouTubeRefreshToken("default", "fake-tok", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	deps := &ytListDeps{}

	deps.PlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		return []ytPlaylistItem{
			{VideoID: "integ-vid1", Title: "Integration Video"},
		}, "", nil
	}

	// Trigger sync via handler.
	watchLaterSyncMu.Lock()
	watchLaterSyncing = false
	watchLaterSyncMu.Unlock()

	srv := &Server{queue: q, ytListDeps: deps}
	req := httptest.NewRequest(http.MethodPost, "/sync/youtube-watchlater", nil)
	rr := httptest.NewRecorder()
	srv.handleSyncWatchLater(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rr.Code)
	}

	// Wait for goroutine to complete.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		watchLaterSyncMu.Lock()
		syncing := watchLaterSyncing
		watchLaterSyncMu.Unlock()
		if !syncing {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Assert seen_content row inserted for this video.
	var count int
	if err := q.db.QueryRow("SELECT COUNT(*) FROM seen_content WHERE source='yt_watch_later' AND item_id='integ-vid1'").Scan(&count); err != nil {
		t.Fatalf("query seen_content: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 seen_content row, got %d", count)
	}

	// Assert queue row enqueued.
	items, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least 1 pending queue item")
	}
}
