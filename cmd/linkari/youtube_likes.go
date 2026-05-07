// YouTube Liked Videos auto-sync.
//
// syncLikedVideosAsync pages through the authenticated user's Liked Videos
// playlist (ID="LL") via the YouTube Data API v3, enqueues any unseen
// videos for scoring, and emits structured observability events.
// Mirrors syncWatchLaterAsync in youtube_wl.go exactly.
package main

import (
	"context"
	"log/slog"
	"time"
)

// YouTubeLikedSource wraps syncLikedVideosAsync behind the ContentSource interface.
type YouTubeLikedSource struct {
	clientID     string
	clientSecret string
	events       *EventLogger
}

func (s *YouTubeLikedSource) Name() string { return "yt_liked" }

// Start polls the Liked Videos playlist every hour until ctx is cancelled.
func (s *YouTubeLikedSource) Start(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
	const interval = 1 * time.Hour
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
		syncLikedVideosAsync("default", q, s.events, s.clientID, s.clientSecret)
	}
}

// syncLikedVideosAsync fetches the Liked Videos playlist and enqueues unseen
// videos for scoring. Runs in a goroutine; errors are logged, not returned.
func syncLikedVideosAsync(profile string, q *Queue, events *EventLogger, clientID, clientSecret string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("syncLikedVideosAsync panic", "recover", r)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	start := time.Now()

	if events != nil {
		_ = events.Emit("likedvideos_sync_start", map[string]interface{}{
			"profile": profile,
		})
	}

	ts, err := youtubeTokenSource(ctx, profile, q, clientID, clientSecret)
	if err != nil {
		slog.Warn("syncLikedVideosAsync: auth error",
			"event_type", "likedvideos_api_error",
			"profile", profile,
			"error_class", "auth_error",
			"error", err,
		)
		if events != nil {
			_ = events.Emit("likedvideos_api_error", map[string]interface{}{
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
		items, next, err := execYouTubePlaylistItems(ctx, ts, "LL", nextPageToken)
		if err != nil {
			errClass := "api_error"
			if isQuotaExhausted(err) {
				errClass = "quota_exhausted"
				slog.Warn("syncLikedVideosAsync: quota exhausted",
					"event_type", "likedvideos_quota_exhausted",
					"profile", profile,
					"page", pageNum,
					"error_class", errClass,
					"error", err,
				)
				if events != nil {
					_ = events.Emit("likedvideos_quota_exhausted", map[string]interface{}{
						"profile":     profile,
						"page":        pageNum,
						"error_class": errClass,
					})
				}
			} else {
				slog.Warn("syncLikedVideosAsync: API error",
					"event_type", "likedvideos_api_error",
					"profile", profile,
					"error_class", errClass,
					"error", err,
				)
				if events != nil {
					_ = events.Emit("likedvideos_api_error", map[string]interface{}{
						"profile":     profile,
						"error_class": errClass,
						"error":       err.Error(),
					})
				}
			}
			break
		}

		if events != nil {
			_ = events.Emit("likedvideos_page_fetched", map[string]interface{}{
				"profile":    profile,
				"page":       pageNum,
				"item_count": len(items),
			})
		}

		if len(items) == 0 && pageNum == 1 {
			if events != nil {
				_ = events.Emit("likedvideos_empty", map[string]interface{}{
					"profile": profile,
				})
			}
			break
		}

		for _, item := range items {
			if item.VideoID == "" {
				continue
			}
			isNew, err := q.IsNewContent("yt_liked", item.VideoID)
			if err != nil {
				slog.Warn("syncLikedVideosAsync: DB check failed", "video_id", item.VideoID, "error", err)
				continue
			}
			if !isNew {
				skipped++
				if events != nil {
					_ = events.Emit("likedvideos_video_skipped", map[string]interface{}{
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
				slog.Warn("syncLikedVideosAsync: enqueue failed", "video_id", item.VideoID, "error", err)
				continue
			}
			_ = q.MarkContentSeen("yt_liked", item.VideoID, rowID)

			enqueued++
			if events != nil {
				_ = events.Emit("likedvideos_video_enqueued", map[string]interface{}{
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
	slog.Info("syncLikedVideosAsync: complete",
		"event_type", "likedvideos_sync_complete",
		"profile", profile,
		"enqueued", enqueued,
		"skipped", skipped,
		"duration_ms", durMS,
	)
	if events != nil {
		_ = events.Emit("likedvideos_sync_complete", map[string]interface{}{
			"profile":     profile,
			"enqueued":    enqueued,
			"skipped":     skipped,
			"duration_ms": durMS,
		})
	}
}
