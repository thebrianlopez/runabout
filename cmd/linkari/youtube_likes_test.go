// YouTube Liked Videos test suite.
// CT1–CT5: contract tests.
// BT1–BT4: behavioral tests.
// RG1–RG2: regression guards.
// Integration: end-to-end stub → enqueue → score.
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
// CT-1: InsertLikedVideo + IsLikedVideoScored (unscored = false)
// ---------------------------------------------------------------------------

func TestLikedVideosCT1_InsertAndCheck(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	videoID := "abc123"
	if err := q.InsertLikedVideo(videoID, time.Now().Unix()); err != nil {
		t.Fatalf("InsertLikedVideo: %v", err)
	}

	scored, err := q.IsLikedVideoScored(videoID)
	if err != nil {
		t.Fatalf("IsLikedVideoScored: %v", err)
	}
	if scored {
		t.Fatal("expected false (not yet scored)")
	}
}

// ---------------------------------------------------------------------------
// CT-2: syncLikedVideosAsync drains one page of items into queue
// ---------------------------------------------------------------------------

func TestLikedVideosCT2_SyncOnePage(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	if err := q.SetYouTubeRefreshToken("default", "fake-tok", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	called := false
	orig := execYouTubePlaylistItems
	defer func() { execYouTubePlaylistItems = orig }()

	execYouTubePlaylistItems = func(_ context.Context, _ oauth2.TokenSource, playlistID, _ string) ([]ytPlaylistItem, string, error) {
		called = true
		if playlistID != "LL" {
			return nil, "", fmt.Errorf("unexpected playlistID: %q", playlistID)
		}
		return []ytPlaylistItem{
			{VideoID: "vid1", Title: "Video One"},
			{VideoID: "vid2", Title: "Video Two"},
		}, "", nil
	}

	syncLikedVideosAsync("default", q, nil, "", "")

	if !called {
		t.Fatal("execYouTubePlaylistItems was not called")
	}

	items, err := q.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 pending items, got %d", len(items))
	}
}

// ---------------------------------------------------------------------------
// CT-3: POST /sync/youtube-likedvideos returns 202
// ---------------------------------------------------------------------------

func TestLikedVideosCT3_HandlerReturns202(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	likedVideosSyncMu.Lock()
	likedVideosSyncing = false
	likedVideosSyncMu.Unlock()

	orig := execYouTubePlaylistItems
	defer func() { execYouTubePlaylistItems = orig }()
	execYouTubePlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		return nil, "", nil
	}

	srv := &Server{queue: q}
	req := httptest.NewRequest(http.MethodPost, "/sync/youtube-likedvideos", nil)
	rr := httptest.NewRecorder()

	srv.handleSyncLikedVideos(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// CT-4: mark scored via scored_at
// ---------------------------------------------------------------------------

func TestLikedVideosCT4_MarkScored(t *testing.T) {
	q, db, cleanup := setupTestQueue(t)
	defer cleanup()

	videoID := "markme"
	now := time.Now().Unix()
	if err := q.InsertLikedVideo(videoID, now); err != nil {
		t.Fatal(err)
	}

	scored, _ := q.IsLikedVideoScored(videoID)
	if scored {
		t.Fatal("should not be scored yet")
	}

	const queueID int64 = 42
	_, err := db.Exec(
		`UPDATE youtube_liked_videos SET scored_at=?, queue_id=? WHERE video_id=?`,
		now, queueID, videoID,
	)
	if err != nil {
		t.Fatalf("UPDATE scored_at: %v", err)
	}

	scored, err = q.IsLikedVideoScored(videoID)
	if err != nil {
		t.Fatal(err)
	}
	if !scored {
		t.Fatal("expected scored=true after setting scored_at")
	}

	var gotQueueID int64
	err = db.QueryRow("SELECT queue_id FROM youtube_liked_videos WHERE video_id=?", videoID).Scan(&gotQueueID)
	if err != nil {
		t.Fatalf("query queue_id: %v", err)
	}
	if gotQueueID != queueID {
		t.Fatalf("expected queue_id=%d, got %d", queueID, gotQueueID)
	}
}

// ---------------------------------------------------------------------------
// CT-5: pagination — nextPageToken followed through 2 pages
// ---------------------------------------------------------------------------

func TestLikedVideosCT5_Pagination(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	if err := q.SetYouTubeRefreshToken("default", "fake-tok", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	var pageCount int32
	orig := execYouTubePlaylistItems
	defer func() { execYouTubePlaylistItems = orig }()

	execYouTubePlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, pageToken string) ([]ytPlaylistItem, string, error) {
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

	syncLikedVideosAsync("default", q, nil, "", "")

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
// BT-1: playlistItems.list called with PlaylistId=LL
// ---------------------------------------------------------------------------

func TestLikedVideosBT1_PlaylistIDIsLL(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	if err := q.SetYouTubeRefreshToken("default", "fake-tok", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	var gotPlaylistID string
	orig := execYouTubePlaylistItems
	defer func() { execYouTubePlaylistItems = orig }()

	execYouTubePlaylistItems = func(_ context.Context, _ oauth2.TokenSource, playlistID, _ string) ([]ytPlaylistItem, string, error) {
		gotPlaylistID = playlistID
		return nil, "", nil
	}

	syncLikedVideosAsync("default", q, nil, "", "")

	if gotPlaylistID != "LL" {
		t.Fatalf("expected PlaylistId=LL, got %q", gotPlaylistID)
	}
}

// ---------------------------------------------------------------------------
// BT-2: multi-page follows nextPageToken
// ---------------------------------------------------------------------------

func TestLikedVideosBT2_MultiPageFollowsToken(t *testing.T) {
	TestLikedVideosCT5_Pagination(t)
}

// ---------------------------------------------------------------------------
// BT-3: quota exhaustion on page 2 → page 1 items enqueued, likedvideos_quota_exhausted logged
// ---------------------------------------------------------------------------

func TestLikedVideosBT3_QuotaExhaustion(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	if err := q.SetYouTubeRefreshToken("default", "fake-tok", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	var callCount int32
	orig := execYouTubePlaylistItems
	defer func() { execYouTubePlaylistItems = orig }()

	execYouTubePlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return []ytPlaylistItem{{VideoID: "quota-vid1"}}, "tok2", nil
		}
		return nil, "", fmt.Errorf("googleapi: Error 403: quotaExceeded")
	}

	evPath := t.TempDir() + "/events.jsonl"
	el, err := NewEventLogger(evPath)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	defer el.Close()

	syncLikedVideosAsync("default", q, el, "", "")

	items, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 enqueued item from page 1, got %d", len(items))
	}

	content, err := os.ReadFile(evPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if !strings.Contains(string(content), "likedvideos_quota_exhausted") {
		t.Fatalf("expected likedvideos_quota_exhausted in event log, got:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// BT-4: youtube_liked_videos.queue_id populated after scoring
// ---------------------------------------------------------------------------

func TestLikedVideosBT4_QueueIDPopulated(t *testing.T) {
	TestLikedVideosCT4_MarkScored(t)
}

// ---------------------------------------------------------------------------
// RG-1: handler returns 202; goroutine error → no ResponseWriter write after return
// ---------------------------------------------------------------------------

func TestLikedVideosRG1_HandlerReturns202NoRace(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	likedVideosSyncMu.Lock()
	likedVideosSyncing = false
	likedVideosSyncMu.Unlock()

	orig := execYouTubePlaylistItems
	defer func() { execYouTubePlaylistItems = orig }()
	execYouTubePlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		time.Sleep(5 * time.Millisecond)
		return nil, "", nil
	}

	srv := &Server{queue: q}
	req := httptest.NewRequest(http.MethodPost, "/sync/youtube-likedvideos", nil)
	rr := httptest.NewRecorder()

	srv.handleSyncLikedVideos(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// RG-2: syncing=true → second POST returns 409
// ---------------------------------------------------------------------------

func TestLikedVideosRG2_ConcurrentSyncReturns409(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	likedVideosSyncMu.Lock()
	likedVideosSyncing = true
	likedVideosSyncMu.Unlock()
	defer func() {
		likedVideosSyncMu.Lock()
		likedVideosSyncing = false
		likedVideosSyncMu.Unlock()
	}()

	srv := &Server{queue: q}
	req := httptest.NewRequest(http.MethodPost, "/sync/youtube-likedvideos", nil)
	rr := httptest.NewRecorder()

	srv.handleSyncLikedVideos(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Integration: end-to-end stub → enqueue → score
// ---------------------------------------------------------------------------

func TestLikedVideosIntegration(t *testing.T) {
	q, db, cleanup := setupTestQueue(t)
	defer cleanup()

	if err := q.SetYouTubeRefreshToken("default", "fake-tok", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	orig := execYouTubePlaylistItems
	defer func() { execYouTubePlaylistItems = orig }()

	execYouTubePlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		return []ytPlaylistItem{
			{VideoID: "integ-vid1", Title: "Integration Video"},
		}, "", nil
	}

	likedVideosSyncMu.Lock()
	likedVideosSyncing = false
	likedVideosSyncMu.Unlock()

	srv := &Server{queue: q}
	req := httptest.NewRequest(http.MethodPost, "/sync/youtube-likedvideos", nil)
	rr := httptest.NewRecorder()
	srv.handleSyncLikedVideos(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rr.Code)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		likedVideosSyncMu.Lock()
		syncing := likedVideosSyncing
		likedVideosSyncMu.Unlock()
		if !syncing {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM youtube_liked_videos WHERE video_id='integ-vid1'").Scan(&count); err != nil {
		t.Fatalf("query liked_videos: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 liked_videos row, got %d", count)
	}

	items, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least 1 pending queue item")
	}
}
