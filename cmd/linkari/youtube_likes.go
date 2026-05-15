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
	autoEnqueue  bool // EPIC-098 F3: gate for queue.Enqueue() calls (future use)
}

func (s *YouTubeLikedSource) Name() string       { return "yt_liked" }
func (s *YouTubeLikedSource) AuthDeps() []string { return []string{"google_youtube"} }

// Start polls the Liked Videos playlist every hour until ctx is cancelled.
func (s *YouTubeLikedSource) Start(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
	const interval = 1 * time.Hour
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
		// EPIC-098 F3: pass autoEnqueue flag (currently always true for yt_liked)
		syncLikedVideosAsync("default", q, s.events, s.clientID, s.clientSecret, s.autoEnqueue)
	}
}

// syncLikedVideosAsync fetches the Liked Videos playlist and enqueues unseen
// videos for scoring. Runs in a goroutine; errors are logged, not returned.
// EPIC-098 F3: autoEnqueue gates queue.Enqueue() calls.
func syncLikedVideosAsync(profile string, q *Queue, events *EventLogger, clientID, clientSecret string, autoEnqueue bool) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("syncLikedVideosAsync panic", "recover", r)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	start := time.Now()

	if events != nil {
		_ = events.Emit("source_start", map[string]interface{}{
			"source":  "yt_liked",
			"profile": profile,
		})
	}

	ts, err := youtubeTokenSource(ctx, profile, q, clientID, clientSecret)
	if err != nil {
		slog.Warn("syncLikedVideosAsync: auth error",
			"event_type", "likedvideos_api_error",
			"source", "yt_liked",
			"profile", profile,
			"error_class", "auth_error",
			"error", err,
		)
		if events != nil {
			_ = events.Emit("likedvideos_api_error", map[string]interface{}{
				"source":      "yt_liked",
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
					"source", "yt_liked",
					"profile", profile,
					"page", pageNum,
					"error_class", errClass,
					"error", err,
				)
				if events != nil {
					_ = events.Emit("likedvideos_quota_exhausted", map[string]interface{}{
						"source":      "yt_liked",
						"profile":     profile,
						"page":        pageNum,
						"error_class": errClass,
					})
				}
			} else {
				slog.Warn("syncLikedVideosAsync: API error",
					"event_type", "likedvideos_api_error",
					"source", "yt_liked",
					"profile", profile,
					"error_class", errClass,
					"error", err,
				)
				if events != nil {
					_ = events.Emit("likedvideos_api_error", map[string]interface{}{
						"source":      "yt_liked",
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
				"source":     "yt_liked",
				"profile":    profile,
				"page":       pageNum,
				"item_count": len(items),
			})
		}

		if len(items) == 0 && pageNum == 1 {
			if events != nil {
				_ = events.Emit("likedvideos_empty", map[string]interface{}{
					"source":  "yt_liked",
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
					_ = events.Emit("source_item_skipped", map[string]interface{}{
						"source":   "yt_liked",
						"profile":  profile,
						"video_id": item.VideoID,
						"reason":   "already_seen",
					})
				}
				continue
			}

			videoURL := "https://www.youtube.com/watch?v=" + item.VideoID

			// EPIC-098 F3: dedup write happens BEFORE the enqueue gate
			if autoEnqueue {
				req := &ShareRequest{
					URL:     videoURL,
					Type:    "url",
					Profile: profile,
					Title:   item.Title,
					Action:  "uinit_auto",
				}
				rowID, err := q.Enqueue(req)
				if err != nil {
					slog.Warn("syncLikedVideosAsync: enqueue failed", "video_id", item.VideoID, "error", err)
					continue
				}
				_ = q.MarkContentSeen("yt_liked", item.VideoID, rowID)

				enqueued++
				if events != nil {
					_ = events.Emit("source_item_enqueued", map[string]interface{}{
						"source":       "yt_liked",
						"profile":      profile,
						"video_id":     item.VideoID,
						"queue_row_id": rowID,
					})
				}
			} else {
				// Observe-only mode: track dedup but don't enqueue
				_ = q.MarkContentSeen("yt_liked", item.VideoID, 0)
				if events != nil {
					_ = events.Emit("yt_enqueue_skipped_by_config", map[string]interface{}{
						"source":   "yt_liked",
						"video_id": item.VideoID,
					})
				}
				slog.Debug("yt_enqueue_skipped_by_config", "source", "yt_liked", "video_id", item.VideoID)
			}
		}

		if next == "" {
			break
		}
		nextPageToken = next
	}

	durMS := time.Since(start).Milliseconds()
	slog.Info("syncLikedVideosAsync: complete",
		"event_type", "source_complete",
		"source", "yt_liked",
		"profile", profile,
		"enqueued", enqueued,
		"skipped", skipped,
		"duration_ms", durMS,
	)
	if events != nil {
		_ = events.Emit("source_complete", map[string]interface{}{
			"source":      "yt_liked",
			"profile":     profile,
			"enqueued":    enqueued,
			"skipped":     skipped,
			"duration_ms": durMS,
		})
	}
}
