// EPIC-019 M6: YouTube subscription feed background worker.
//
// watchSubscriptionsAsync polls the authenticated user's YouTube subscriptions,
// fetches recent uploads from each channel's uploads playlist, and enqueues
// new videos for scoring. Emits structured observability events and optionally
// fires a daily push digest summarising worth-watching vs. skipped counts.
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

// ytSubscription represents a single YouTube subscription channel.
type ytSubscription struct {
	ChannelID string
	Title     string
}

// Injectable API seams — replaced in tests.
var execYouTubeSubscriptionsList = execYouTubeSubscriptionsListReal
var execYouTubeChannelsList = execYouTubeChannelsListReal
var execYouTubePlaylistItemsList = execYouTubePlaylistItemsListReal

// execYouTubeSubscriptionsListReal fetches all subscription channels for the
// authenticated user via subscriptions.list(mine=true).
func execYouTubeSubscriptionsListReal(ctx context.Context, ts oauth2.TokenSource) ([]ytSubscription, error) {
	svc, err := youtube.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("youtube service: %w", err)
	}

	var subs []ytSubscription
	pageToken := ""
	for {
		call := svc.Subscriptions.List([]string{"snippet"}).
			Mine(true).
			MaxResults(50)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return nil, err
		}
		for _, s := range resp.Items {
			subs = append(subs, ytSubscription{
				ChannelID: s.Snippet.ResourceId.ChannelId,
				Title:     s.Snippet.Title,
			})
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return subs, nil
}

// execYouTubeChannelsListReal fetches the uploads playlist ID for each channel.
// Returns a map of channelID → uploadsPlaylistID.
func execYouTubeChannelsListReal(ctx context.Context, ts oauth2.TokenSource, channelIDs []string) (map[string]string, error) {
	if len(channelIDs) == 0 {
		return nil, nil
	}
	svc, err := youtube.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("youtube service: %w", err)
	}

	result := make(map[string]string, len(channelIDs))
	// YouTube API allows up to 50 IDs per channels.list call.
	for i := 0; i < len(channelIDs); i += 50 {
		end := i + 50
		if end > len(channelIDs) {
			end = len(channelIDs)
		}
		batch := channelIDs[i:end]
		resp, err := svc.Channels.List([]string{"contentDetails"}).
			Id(batch...).
			MaxResults(50).
			Do()
		if err != nil {
			return nil, err
		}
		for _, ch := range resp.Items {
			result[ch.Id] = ch.ContentDetails.RelatedPlaylists.Uploads
		}
	}
	return result, nil
}

// execYouTubePlaylistItemsListReal fetches the most recent 10 items from an
// uploads playlist (no pagination — we only want recent videos).
func execYouTubePlaylistItemsListReal(ctx context.Context, ts oauth2.TokenSource, playlistID string) ([]ytPlaylistItem, error) {
	svc, err := youtube.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("youtube service: %w", err)
	}

	resp, err := svc.PlaylistItems.List([]string{"snippet"}).
		PlaylistId(playlistID).
		MaxResults(10).
		Do()
	if err != nil {
		return nil, err
	}

	var items []ytPlaylistItem
	for _, it := range resp.Items {
		items = append(items, ytPlaylistItem{
			VideoID: it.Snippet.ResourceId.VideoId,
			Title:   it.Snippet.Title,
		})
	}
	return items, nil
}

// watchSubscriptionsAsync polls all subscription channels and enqueues new
// videos for scoring. Designed to run as a periodic background worker.
// EPIC-019 M6 + M10.
func watchSubscriptionsAsync(profile string, q *Queue, events *EventLogger) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("watchSubscriptionsAsync panic", "recover", r)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	start := time.Now()

	if events != nil {
		_ = events.Emit("subscription_poll_start", map[string]interface{}{
			"profile": profile,
		})
	}

	ts, err := youtubeTokenSource(ctx, profile, q)
	if err != nil {
		slog.Warn("watchSubscriptionsAsync: auth error",
			"event_type", "subscriptions_api_error",
			"profile", profile,
			"error_class", "auth_error",
			"error", err,
		)
		if events != nil {
			_ = events.Emit("subscriptions_api_error", map[string]interface{}{
				"profile":     profile,
				"error_class": "auth_error",
				"error":       err.Error(),
			})
		}
		return
	}

	// Step 1: fetch all subscription channels.
	subs, err := execYouTubeSubscriptionsList(ctx, ts)
	if err != nil {
		errClass := "api_error"
		if isQuotaExhausted(err) {
			errClass = "quota_exceeded"
		}
		slog.Warn("watchSubscriptionsAsync: subscriptions.list failed",
			"event_type", "subscriptions_quota_exceeded",
			"profile", profile,
			"error_class", errClass,
		)
		if events != nil {
			evType := "subscriptions_api_error"
			if errClass == "quota_exceeded" {
				evType = "subscriptions_quota_exceeded"
			}
			_ = events.Emit(evType, map[string]interface{}{
				"profile":     profile,
				"error_class": errClass,
				"error":       err.Error(),
			})
		}
		return
	}

	if len(subs) == 0 {
		if events != nil {
			_ = events.Emit("subscription_digest_empty", map[string]interface{}{
				"profile": profile,
			})
		}
		return
	}

	// Step 2: fetch uploads playlist IDs for all channels.
	channelIDs := make([]string, len(subs))
	for i, s := range subs {
		channelIDs[i] = s.ChannelID
	}
	uploadsPlaylists, err := execYouTubeChannelsList(ctx, ts, channelIDs)
	if err != nil {
		slog.Warn("watchSubscriptionsAsync: channels.list failed",
			"event_type", "subscriptions_api_error",
			"profile", profile,
			"error_class", "channels_list_error",
			"error", err,
		)
		if events != nil {
			_ = events.Emit("subscriptions_api_error", map[string]interface{}{
				"profile":     profile,
				"error_class": "channels_list_error",
				"error":       err.Error(),
			})
		}
		return
	}

	// Step 3: for each channel, fetch recent uploads and enqueue new videos.
	var totalEnqueued, totalSkipped int
	channelsPolled := 0

	for _, sub := range subs {
		uploadsID, ok := uploadsPlaylists[sub.ChannelID]
		if !ok || uploadsID == "" {
			continue
		}

		items, err := execYouTubePlaylistItemsList(ctx, ts, uploadsID)
		if err != nil {
			slog.Warn("watchSubscriptionsAsync: playlistItems.list failed",
				"event_type", "subscriptions_api_error",
				"profile", profile,
				"channel_id", sub.ChannelID,
				"error_class", "playlist_items_error",
				"error", err,
			)
			if events != nil {
				_ = events.Emit("subscriptions_api_error", map[string]interface{}{
					"profile":     profile,
					"error_class": "playlist_items_error",
					"error":       err.Error(),
				})
			}
			// Per-channel error: log and continue to next channel.
			continue
		}

		channelsPolled++
		newVideoCount := 0

		for _, item := range items {
			if item.VideoID == "" {
				continue
			}
			known, err := q.IsMonitoredVideoKnown(item.VideoID)
			if err != nil {
				slog.Warn("watchSubscriptionsAsync: DB check failed", "video_id", item.VideoID, "error", err)
				continue
			}
			if known {
				totalSkipped++
				continue
			}

			if err := q.InsertMonitoredVideo(sub.ChannelID, item.VideoID, time.Now().Unix()); err != nil {
				slog.Warn("watchSubscriptionsAsync: insert failed", "video_id", item.VideoID, "error", err)
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
				slog.Warn("watchSubscriptionsAsync: enqueue failed", "video_id", item.VideoID, "error", err)
				continue
			}

			newVideoCount++
			totalEnqueued++

			if events != nil {
				_ = events.Emit("subscription_video_enqueued", map[string]interface{}{
					"profile":      profile,
					"channel_id":   sub.ChannelID,
					"video_id":     item.VideoID,
					"queue_row_id": rowID,
				})
			}
		}

		if events != nil {
			_ = events.Emit("subscription_channel_discovered", map[string]interface{}{
				"profile":         profile,
				"channel_id":      sub.ChannelID,
				"new_video_count": newVideoCount,
			})
		}
	}

	durMS := time.Since(start).Milliseconds()
	slog.Info("watchSubscriptionsAsync: complete",
		"event_type", "subscription_poll_complete",
		"profile", profile,
		"channels_polled", channelsPolled,
		"videos_enqueued", totalEnqueued,
		"videos_skipped", totalSkipped,
		"duration_ms", durMS,
	)
	if events != nil {
		_ = events.Emit("subscription_poll_complete", map[string]interface{}{
			"profile":         profile,
			"channels_polled": channelsPolled,
			"videos_enqueued": totalEnqueued,
			"videos_skipped":  totalSkipped,
			"duration_ms":     durMS,
		})
	}

	// Step 4: daily digest if there are any scored videos today.
	worthWatching, skipped, err := q.CountScoredMonitoredVideosToday(profile)
	if err != nil {
		slog.Warn("watchSubscriptionsAsync: CountScoredMonitoredVideosToday failed", "error", err)
	} else if worthWatching+skipped > 0 {
		body := fmt.Sprintf("%d worth watching, %d skipped", worthWatching, skipped)
		if err := q.EnqueueSubscriptionDigest(profile, body, worthWatching, skipped); err != nil {
			slog.Warn("watchSubscriptionsAsync: EnqueueSubscriptionDigest failed", "error", err)
		} else if events != nil {
			_ = events.Emit("subscription_digest_sent", map[string]interface{}{
				"profile":       profile,
				"worth_watching": worthWatching,
				"skipped":       skipped,
			})
		}
	} else if events != nil {
		_ = events.Emit("subscription_digest_empty", map[string]interface{}{
			"profile": profile,
		})
	}
}
