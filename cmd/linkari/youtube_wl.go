// EPIC-018 M4: YouTube Watch Later auto-sync.
//
// syncWatchLaterAsync pages through the authenticated user's Watch Later
// playlist (ID="WL") via the YouTube Data API v3, enqueues any unseen
// videos for scoring, and emits structured observability events.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

// ytPlaylistItem represents a single item from a YouTube playlist page.
type ytPlaylistItem struct {
	VideoID string
	Title   string
}

// execYouTubePlaylistItems is the injectable seam for testing.
// In production it calls execYouTubePlaylistItemsReal.
var execYouTubePlaylistItems = execYouTubePlaylistItemsReal

// execYouTubePlaylistItemsReal fetches one page of items from a YouTube playlist
// using the Data API v3 (playlistItems.list).
func execYouTubePlaylistItemsReal(ctx context.Context, ts oauth2.TokenSource, playlistID, pageToken string) (items []ytPlaylistItem, nextPageToken string, err error) {
	svc, err := youtube.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, "", fmt.Errorf("youtube service init: %w", err)
	}

	call := svc.PlaylistItems.List([]string{"snippet"}).
		PlaylistId(playlistID).
		MaxResults(50)
	if pageToken != "" {
		call = call.PageToken(pageToken)
	}

	resp, err := call.Do()
	if err != nil {
		return nil, "", err
	}

	for _, it := range resp.Items {
		items = append(items, ytPlaylistItem{
			VideoID: it.Snippet.ResourceId.VideoId,
			Title:   it.Snippet.Title,
		})
	}
	return items, resp.NextPageToken, nil
}

// syncWatchLaterAsync fetches the Watch Later playlist and enqueues unseen
// videos for scoring. Runs in a goroutine; errors are logged, not returned.
// EPIC-018 M4 + M8.
func syncWatchLaterAsync(profile string, q *Queue, events *EventLogger, clientID, clientSecret string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("syncWatchLaterAsync panic", "recover", r)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	start := time.Now()

	if events != nil {
		_ = events.Emit("watchlater_sync_start", map[string]interface{}{
			"profile": profile,
		})
	}

	ts, err := youtubeTokenSource(ctx, profile, q, clientID, clientSecret)
	if err != nil {
		slog.Warn("syncWatchLaterAsync: auth error",
			"event_type", "watchlater_api_error",
			"profile", profile,
			"error_class", "auth_error",
			"error", err,
		)
		if events != nil {
			_ = events.Emit("watchlater_api_error", map[string]interface{}{
				"profile":     profile,
				"error_class": "auth_error",
				"error":       err.Error(),
			})
		}
		return
	}

	var enqueued, skipped int
	pageNum := 0
	nextPageToken := ""

	for {
		pageNum++
		items, next, err := execYouTubePlaylistItems(ctx, ts, "WL", nextPageToken)
		if err != nil {
			errClass := "api_error"
			if isQuotaExhausted(err) {
				errClass = "quota_exhausted"
				slog.Warn("syncWatchLaterAsync: quota exhausted",
					"event_type", "watchlater_quota_exhausted",
					"profile", profile,
					"page", pageNum,
					"error_class", errClass,
					"error", err,
				)
				if events != nil {
					_ = events.Emit("watchlater_quota_exhausted", map[string]interface{}{
						"profile":     profile,
						"page":        pageNum,
						"error_class": errClass,
					})
				}
			} else {
				slog.Warn("syncWatchLaterAsync: API error",
					"event_type", "watchlater_api_error",
					"profile", profile,
					"error_class", errClass,
					"error", err,
				)
				if events != nil {
					_ = events.Emit("watchlater_api_error", map[string]interface{}{
						"profile":     profile,
						"error_class": errClass,
						"error":       err.Error(),
					})
				}
			}
			break
		}

		if events != nil {
			_ = events.Emit("watchlater_page_fetched", map[string]interface{}{
				"profile":    profile,
				"page":       pageNum,
				"item_count": len(items),
			})
		}

		if len(items) == 0 && pageNum == 1 {
			if events != nil {
				_ = events.Emit("watchlater_empty", map[string]interface{}{
					"profile": profile,
				})
			}
			break
		}

		for _, item := range items {
			if item.VideoID == "" {
				continue
			}
			isNew, err := q.IsNewContent("yt_watch_later", item.VideoID)
			if err != nil {
				slog.Warn("syncWatchLaterAsync: DB check failed", "video_id", item.VideoID, "error", err)
				continue
			}
			if !isNew {
				skipped++
				if events != nil {
					_ = events.Emit("watchlater_video_skipped", map[string]interface{}{
						"profile":  profile,
						"video_id": item.VideoID,
						"reason":   "already_seen",
					})
				}
				continue
			}

			videoURL := "https://www.youtube.com/watch?v=" + item.VideoID
			req := &ShareRequest{
				URL:     videoURL,
				Type:    "url",
				Profile: profile,
				Title:   item.Title,
				Action:  "default",
			}
			rowID, err := q.Enqueue(req)
			if err != nil {
				slog.Warn("syncWatchLaterAsync: enqueue failed", "video_id", item.VideoID, "error", err)
				continue
			}
			_ = q.MarkContentSeen("yt_watch_later", item.VideoID, rowID)

			enqueued++
			if events != nil {
				_ = events.Emit("watchlater_video_enqueued", map[string]interface{}{
					"profile":      profile,
					"video_id":     item.VideoID,
					"queue_row_id": rowID,
				})
			}
		}

		if next == "" {
			break
		}
		nextPageToken = next
	}

	durMS := time.Since(start).Milliseconds()
	slog.Info("syncWatchLaterAsync: complete",
		"event_type", "watchlater_sync_complete",
		"profile", profile,
		"enqueued", enqueued,
		"skipped", skipped,
		"duration_ms", durMS,
	)
	if events != nil {
		_ = events.Emit("watchlater_sync_complete", map[string]interface{}{
			"profile":     profile,
			"enqueued":    enqueued,
			"skipped":     skipped,
			"duration_ms": durMS,
		})
	}
}

// isQuotaExhausted returns true when err looks like a YouTube API 403 quota error.
func isQuotaExhausted(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return containsAny(s, "quotaExceeded", "rateLimitExceeded", "403")
}

// containsAny returns true if s contains any of the given substrings.
func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
