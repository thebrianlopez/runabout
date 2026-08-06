package main

import (
	"context"
	"log/slog"
	"time"
)

func replayShareRequest(it QueueItem) *ShareRequest {
	return &ShareRequest{
		Type:       it.Type,
		Action:     it.Action,
		Text:       it.Text,
		URL:        it.URL,
		Profile:    it.Profile,
		Intent:     it.Intent,
		QueueRowID: it.ID,
		Enter:      true,
	}
}

// StartReplay runs a background goroutine that replays pending queue items
// through the router when the tmux session becomes available.
//
// The goroutine runs until ctx is cancelled. In production ctx is the serve
// command's context, so the replay loop exits cleanly on shutdown instead of
// running for the lifetime of the process; this also makes the loop testable
// (integration tests that spin up serveCmd cancel the context and the goroutine
// returns rather than leaking a ticker goroutine  -  see the leakcheck gate).
func StartReplay(ctx context.Context, q *Queue, router *Router, srv *Server, tmux *TmuxRunner, interval time.Duration, debug bool) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if !tmux.serverRunning() {
				slog.Debug("replay: tmux server not running, skipping")
				continue
			}

			items, err := q.Pending()
			if err != nil {
				slog.Warn("replay pending query failed", "error", err)
				continue
			}
			if len(items) == 0 {
				continue
			}

			slog.Info(
				"replay batch",
				"event_type", "replay_batch",
				"pending_count", len(items),
			)
			for _, it := range items {
				if ac := router.LookupAction(it.Action); ac != nil && ac.Kind == KindCapture {
					srv.wg.Add(1)
					go srv.captureAsync(context.Background(), it.ID, ac)
					continue
				}
				req := replayShareRequest(it)
				result, err := router.Route(req)
				if err != nil {
					slog.Error(
						"replay item failed",
						"event_type", "replay_result",
						"id", it.ID,
						"error", err.Error(),
					)
					q.MarkFailed(it.ID)
					continue
				}
				q.MarkRelayed(it.ID)
				slog.Info(
					"replay item relayed",
					"event_type", "replay_result",
					"id", it.ID,
					"type", it.Type,
					"result", result,
				)
			}
		}
	}()
}
