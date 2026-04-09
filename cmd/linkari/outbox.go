package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// EPIC-045 M2: durable push delivery via push_outbox.

const (
	pushPollInterval = 2 * time.Second
	pushDrainLimit   = 50
	pushMaxAttempts  = 5
	pushMaxAge       = 24 * time.Hour
	pushParkBackoff  = 15 * time.Minute
)

// backoffSchedule maps attempts (after the just-failed try) to the next delay.
// Index = attempts count after increment. Dead after index 5.
var backoffSchedule = []time.Duration{
	0,                // placeholder (attempts=0 never used)
	30 * time.Second, // after first failure
	2 * time.Minute,  // after second
	10 * time.Minute, // after third
	1 * time.Hour,    // after fourth
}

// outboxMu serialises the in-process worker drain loop.
var outboxMu sync.Mutex

// StartPushWorker launches the push_outbox drain goroutine. It polls every
// pushPollInterval, drains up to pushDrainLimit pending rows per tick under
// an app-level mutex, applies backoff / park / dead-letter semantics, and
// emits JSONL telemetry events via the existing telemetry writer.
func (s *Server) StartPushWorker(ctx context.Context) {
	if s.queue == nil {
		return
	}
	go func() {
		t := time.NewTicker(pushPollInterval)
		defer t.Stop()
		prune := time.NewTicker(1 * time.Hour)
		defer prune.Stop()
		slog.InfoContext(ctx, "push outbox worker started",
			"event_type", "push_worker_start",
			"poll_interval", pushPollInterval.String(),
		)
		for {
			select {
			case <-ctx.Done():
				return
			case <-prune.C:
				if err := s.queue.PrunePushes(); err != nil {
					slog.DebugContext(ctx, "push prune failed", "error", err)
				}
			case <-t.C:
				s.drainPushOutbox(ctx)
			}
		}
	}()
}

func (s *Server) drainPushOutbox(ctx context.Context) {
	outboxMu.Lock()
	defer outboxMu.Unlock()

	items, err := s.queue.PendingPushes(pushDrainLimit)
	if err != nil {
		slog.WarnContext(ctx, "pending pushes query failed", "error", err)
		return
	}
	if len(items) == 0 {
		return
	}

	// Snapshot device token and token source once per drain tick.
	deviceToken, err := s.queue.GetDeviceToken()
	if err != nil {
		slog.WarnContext(ctx, "get device token failed", "error", err)
		return
	}

	for _, p := range items {
		age := time.Since(time.Unix(p.CreatedAt, 0))
		if age > pushMaxAge {
			_ = s.queue.MarkPushDead(p.ID, "age ceiling exceeded")
			emitPushEvent("push_outbox_dead", map[string]interface{}{
				"id": p.ID, "reason": "age_ceiling", "attempts": p.Attempts,
			})
			continue
		}
		if deviceToken == "" || s.fcmTokenSource == nil {
			_ = s.queue.ParkPush(p.ID, int64(pushParkBackoff.Seconds()))
			emitPushEvent("push_outbox_parked_missing_token", map[string]interface{}{
				"id": p.ID, "age_seconds": int64(age.Seconds()),
			})
			continue
		}
		if err := sendOutboxFCM(s, deviceToken, p.Score, p.Slug, p.Verdict, p.URL); err != nil {
			attempts := p.Attempts + 1
			if attempts >= pushMaxAttempts {
				_ = s.queue.MarkPushDead(p.ID, err.Error())
				emitPushEvent("push_outbox_dead", map[string]interface{}{
					"id": p.ID, "reason": "max_attempts", "attempts": attempts, "error": err.Error(),
				})
				continue
			}
			backoff := backoffSchedule[attempts]
			_ = s.queue.BumpPushAttempt(p.ID, int64(backoff.Seconds()), err.Error())
			slog.WarnContext(ctx, "push attempt failed",
				"event_type", "push_attempt",
				"id", p.ID,
				"attempt", attempts,
				"error", err.Error(),
				"retry_in", backoff.String(),
			)
			continue
		}
		_ = s.queue.MarkPushSent(p.ID)
		emitPushEvent("push_outbox_sent", map[string]interface{}{
			"id": p.ID, "score": p.Score, "slug": p.Slug, "attempts": p.Attempts + 1,
		})
	}
}

// emitPushEvent writes a JSONL event to the telemetry events directory using
// the same schema v2 record shape as CLI commands. This reuses writeEvent
// from telemetry.go so rotation / path resolution / user/session fields all
// stay consistent with the rest of the bus.
func emitPushEvent(eventType string, meta map[string]interface{}) {
	e := event{
		SchemaVersion: "2",
		Timestamp:     time.Now().UTC().Format("20060102T150405Z"),
		Layer:         "go_cli",
		EventType:     eventType,
		Command:       "linkari serve",
		SessionID:     "server",
		User:          "",
		CWD:           "",
		DurationMs:    0,
		ExitCode:      0,
		Metadata:      meta,
	}
	if err := writeEvent(e); err != nil {
		slog.Warn("telemetry emit failed", "event", eventType, "error", err)
	}
}

// --- actual FCM delivery (concrete oauth2 signature) ---

// sendOutboxFCM is the single production caller of the FCM HTTP v1 API.
// Body construction mirrors the deleted sendFCMPush helper.
func sendOutboxFCM(s *Server, deviceToken string, score int, slug, verdict, url string) error {
	tok, err := s.fcmTokenSource.Token()
	if err != nil {
		return fmt.Errorf("obtaining oauth2 token: %w", err)
	}

	notifBody := slug
	if verdict != "" {
		notifBody = firstSentence(verdict, 120)
	}

	var title string
	switch {
	case score >= 70:
		title = fmt.Sprintf("Worth reading — %d/100", score)
	case score >= 40:
		title = fmt.Sprintf("Maybe — %d/100", score)
	default:
		title = fmt.Sprintf("Skip it — %d/100", score)
	}

	payload := map[string]interface{}{
		"message": map[string]interface{}{
			"token": deviceToken,
			"notification": map[string]string{
				"title": title,
				"body":  notifBody,
			},
			"data": map[string]string{
				"slug":    slug,
				"verdict": verdict,
				"url":     url,
				"score":   fmt.Sprintf("%d", score),
			},
			"android": map[string]string{
				"priority": "high",
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling FCM payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, fcmEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating FCM request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending FCM request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("FCM returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
