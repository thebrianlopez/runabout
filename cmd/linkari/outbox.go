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

// idleEmitEvery is how many consecutive empty drain cycles trigger a
// push_outbox_idle_metric event. With pushPollInterval=2s this emits once
// every 60s of continuous idle state. EPIC-051 M7.
const idleEmitEvery = 30

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
		var idleCycles int
		for {
			select {
			case <-ctx.Done():
				return
			case <-prune.C:
				if err := s.queue.PrunePushes(); err != nil {
					slog.DebugContext(ctx, "push prune failed", "error", err)
				}
			case <-t.C:
				drained := s.drainPushOutbox(ctx)
				// EPIC-051 M7: idle observability. A single "drained 0 rows"
				// tick isn't interesting; a minute of silence is. Emit once
				// per idleEmitEvery consecutive empty cycles so operators
				// can answer "is the worker alive but idle?" via a query.
				if drained == 0 {
					idleCycles++
					if idleCycles%idleEmitEvery == 0 {
						emitPushEvent("push_outbox_idle_metric", map[string]interface{}{
							"idle_cycles":   idleCycles,
							"poll_interval": pushPollInterval.String(),
						})
					}
				} else {
					idleCycles = 0
				}
			}
		}
	}()
}

// drainPushOutbox runs one drain pass and returns the number of rows
// processed (attempted + completed). A return of 0 means the outbox was
// idle this tick — used by StartPushWorker to emit push_outbox_idle_metric.
func (s *Server) drainPushOutbox(ctx context.Context) int {
	outboxMu.Lock()
	defer outboxMu.Unlock()

	items, err := s.queue.PendingPushes(pushDrainLimit)
	if err != nil {
		slog.WarnContext(ctx, "pending pushes query failed", "error", err)
		return 0
	}
	if len(items) == 0 {
		return 0
	}

	// Snapshot device token and token source once per drain tick.
	deviceToken, err := s.queue.GetDeviceToken()
	if err != nil {
		slog.WarnContext(ctx, "get device token failed", "error", err)
		return 0
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
		if err := sendOutboxFCM(s, deviceToken, p.Score, p.Slug, p.Verdict, p.URL, p.Profile, p.GapSummary, p.ContentType, p.ClassifySource); err != nil {
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
	return len(items)
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

// emitShareActionResolved writes a share_action_resolved event to the
// telemetry events directory. EPIC-052: the single auditable record of how
// any ingress path resolved its (action, profile) pair. Emit BEFORE the
// queue DB write so failed inserts still produce the provenance trail (the
// non-functional requirement in the epic).
//
// Schema (matches the epic's event block verbatim):
//
//	{
//	  "event": "share_action_resolved",
//	  "ts": "<utc>",
//	  "received_action":  "uinit_auto",
//	  "received_profile": "",
//	  "resolved_action":  "uinit_auto",
//	  "resolved_profile": "travel",
//	  "resolution_reason": "domain_heuristic",
//	  "url": "https://example.com",
//	  "queue_id": 276
//	}
//
// queueID should be 0 at emit-time (queue row has not been written yet) and
// can be reconciled post-hoc by joining on url + ts window. The field is
// carried so that a future chokepoint which knows the row id ahead of time
// (e.g. a UUIDv7-keyed insert) can populate it without schema churn.
func emitShareActionResolved(res ShareResolution, url string, queueID int64) {
	emitPushEvent("share_action_resolved", map[string]interface{}{
		"received_action":   res.ReceivedAction,
		"received_profile":  res.ReceivedProfile,
		"resolved_action":   res.ResolvedAction,
		"resolved_profile":  res.ResolvedProfile,
		"resolution_reason": res.Reason,
		"url":               url,
		"queue_id":          queueID,
	})
}

// --- actual FCM delivery (concrete oauth2 signature) ---

// sendOutboxFCM is the single production caller of the FCM HTTP v1 API.
// EPIC-061: profile parameter added to include auto-classified profile in
// the FCM data payload so the Android client can display it.
// EPIC-071 M3: contentType parameter added — "voice_note" triggers a
// different notification title/body and includes content_type in the data map.
// EPIC-077 M6: classifySource parameter added — included in FCM data payload
// so the Android client can surface classification provenance in debug views.
func sendOutboxFCM(s *Server, deviceToken string, score int, slug, verdict, url, profile, gapSummary, contentType, classifySource string) error {
	tok, err := s.fcmTokenSource.Token()
	if err != nil {
		return fmt.Errorf("obtaining oauth2 token: %w", err)
	}

	var title, notifBody string
	if contentType == "voice_note" {
		// EPIC-071 M3: voice notes get a synopsis-oriented notification.
		title = "Voice note transcribed"
		notifBody = verdict // synopsis is already ≤280 chars from prompt constraint
	} else {
		notifBody = slug
		if verdict != "" {
			notifBody = firstSentence(verdict, 120)
		}
		switch {
		case score >= 70:
			title = fmt.Sprintf("Worth reading — %d/100", score)
		case score >= 40:
			title = fmt.Sprintf("Maybe — %d/100", score)
		default:
			title = fmt.Sprintf("Skip it — %d/100", score)
		}
	}

	payload := map[string]interface{}{
		"message": map[string]interface{}{
			"token": deviceToken,
			"notification": map[string]string{
				"title": title,
				"body":  notifBody,
			},
			"data": map[string]string{
				"slug":            slug,
				"verdict":         verdict,
				"url":             url,
				"score":           fmt.Sprintf("%d", score),
				"profile":         profile,
				"gap_summary":     gapSummary,
				"content_type":    contentType,
				"classify_source": classifySource,
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
