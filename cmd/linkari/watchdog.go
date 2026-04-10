package main

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// RelayedWatchdog is a background goroutine that scans for queue rows stuck
// in `relayed` status longer than a configured max age.
//
// EPIC-054 M3 (baseline): marks timed-out rows failed and emits a
// `share_scoring_timeout` event per row.
//
// EPIC-055 M1 (rescue): before marking rows failed, attempts to recover
// verdicts from on-disk _score.json files written by the scoring pipeline.
// Rescued rows are promoted to `scored` rather than `failed`, and a
// `score_ingested` event is emitted per rescue with source=watchdog_backfill.
//
// EPIC-055 M3 (alert): tracks share_scoring_timeout event volume over a
// rolling window; emits a `push_outbox` alert + `share_scoring_timeout_alert`
// JSONL event when the count exceeds AlertThreshold within AlertWindow.
//
// Idempotency: the WHERE status='relayed' predicate guarantees a row marked
// failed or scored on a previous tick is never re-swept. IngestScoreIfRelayed
// uses an additional AND status='relayed' guard against concurrent writers.
//
// Hot-reload: SIGHUP swaps the config atomically via SetConfig. The ticker
// period is rebuilt lazily when a changed Interval is observed.
type RelayedWatchdog struct {
	queue  *Queue
	events *EventLogger
	cfg    atomic.Pointer[RelayedWatchdogCfg]

	// alert ring buffer: timestamps of recent share_scoring_timeout events.
	alertMu      sync.Mutex
	alertTimes   []time.Time
	alertFiredAt time.Time // last time an FCM alert was enqueued

	// burstEmitted ensures the score_ingested_backfill_burst event fires at
	// most once per process lifetime (first sweep that rescues ≥1 row).
	burstEmittedMu sync.Mutex
	burstEmitted   bool
}

// NewRelayedWatchdog constructs the watchdog with an initial config. Callers
// should not call Run unless cfg.MaxAge > 0 (the zero value disables the
// watchdog entirely — Run returns immediately).
func NewRelayedWatchdog(q *Queue, events *EventLogger, cfg RelayedWatchdogCfg) *RelayedWatchdog {
	w := &RelayedWatchdog{queue: q, events: events}
	w.cfg.Store(&cfg)
	return w
}

// SetConfig atomically replaces the watchdog config. Called from the SIGHUP
// reload path in main.go.
func (w *RelayedWatchdog) SetConfig(cfg RelayedWatchdogCfg) {
	w.cfg.Store(&cfg)
}

// Config returns the current config snapshot.
func (w *RelayedWatchdog) Config() RelayedWatchdogCfg {
	if p := w.cfg.Load(); p != nil {
		return *p
	}
	return RelayedWatchdogCfg{}
}

// Run ticks at the configured interval until ctx is cancelled. Each tick
// invokes sweepOnce. Interval changes via SetConfig are picked up at the
// next tick boundary — the ticker is rebuilt when the observed interval
// differs from the previous tick's.
func (w *RelayedWatchdog) Run(ctx context.Context) {
	cfg := w.Config()
	if cfg.Interval <= 0 || cfg.MaxAge <= 0 {
		slog.Info("relayed watchdog disabled", "event_type", "relayed_watchdog_disabled")
		return
	}
	slog.Info("relayed watchdog started",
		"event_type", "relayed_watchdog_started",
		"interval_secs", int(cfg.Interval.Seconds()),
		"max_age_secs", int(cfg.MaxAge.Seconds()),
		"url_work_dir", cfg.UrlWorkDir,
		"alert_threshold", cfg.AlertThreshold,
	)
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	currentInterval := cfg.Interval
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			live := w.Config()
			if live.MaxAge <= 0 {
				// Runtime disable via SIGHUP — keep goroutine alive but skip work.
				continue
			}
			if live.Interval != currentInterval && live.Interval > 0 {
				ticker.Reset(live.Interval)
				currentInterval = live.Interval
			}
			w.sweepOnce(time.Now(), live)
		}
	}
}

// sweepOnce performs a single pass: select stuck rows, attempt on-disk rescue,
// mark unrescued rows failed, emit events. Extracted from Run so tests can
// drive it synchronously.
func (w *RelayedWatchdog) sweepOnce(now time.Time, cfg RelayedWatchdogCfg) {
	stuck, err := w.queue.SelectStuckRelayed(now, cfg.MaxAge)
	if err != nil {
		slog.Error("relayed watchdog sweep failed",
			"event_type", "relayed_watchdog_error",
			"error", err,
		)
		return
	}
	if len(stuck) == 0 {
		return
	}

	// M1: attempt on-disk rescue before marking rows as timed out.
	rescued, unrescued := w.rescueFromDisk(stuck, now, cfg)

	// Emit score_ingested_backfill_burst on the first sweep that rescues rows.
	if len(rescued) > 0 {
		w.burstEmittedMu.Lock()
		if !w.burstEmitted {
			w.burstEmitted = true
			w.burstEmittedMu.Unlock()
			slog.Info("watchdog backfill burst",
				"event_type", "score_ingested_backfill_burst",
				"count", len(rescued),
			)
			if w.events != nil {
				_ = w.events.Emit("score_ingested_backfill_burst", map[string]interface{}{
					"count": len(rescued),
				})
			}
		} else {
			w.burstEmittedMu.Unlock()
		}
	}

	// Mark unrescued rows as timed out and emit events.
	var timedOutIDs []int64
	for _, t := range unrescued {
		timedOutIDs = append(timedOutIDs, t.ID)
	}
	if len(timedOutIDs) > 0 {
		if err := w.queue.MarkRelayedTimedOut(timedOutIDs); err != nil {
			slog.Error("relayed watchdog mark failed",
				"event_type", "relayed_watchdog_error",
				"error", err,
			)
		}
	}
	for _, t := range unrescued {
		slog.Warn("relayed row timed out",
			"event_type", "share_scoring_timeout",
			"id", t.ID,
			"url", t.URL,
			"profile", t.Profile,
			"queued_at", t.QueuedAt,
			"age_seconds", t.AgeSecs,
		)
		if w.events != nil {
			if emitErr := w.events.Emit("share_scoring_timeout", map[string]interface{}{
				"id":          t.ID,
				"url":         t.URL,
				"profile":     t.Profile,
				"queued_at":   t.QueuedAt,
				"age_seconds": t.AgeSecs,
			}); emitErr != nil {
				slog.Error("relayed watchdog emit failed",
					"event_type", "relayed_watchdog_emit_error",
					"id", t.ID,
					"error", emitErr,
				)
			}
		}
	}

	// M3: alert on sustained timeout volume.
	if cfg.AlertThreshold > 0 && len(unrescued) > 0 {
		w.alertOnVolume(unrescued, now, cfg)
	}
}

// rescueFromDisk attempts to ingest scores from _score.json files for each
// stuck row. Returns (rescued, unrescued) partitions. Rows whose _score.json
// is found and successfully ingested via IngestScoreIfRelayed are rescued;
// others fall through to the timeout path.
//
// The on-disk index is built once per call and is not cached across ticks.
// When UrlWorkDir is empty, rescue is skipped (Tier-1 rollback knob).
func (w *RelayedWatchdog) rescueFromDisk(stuck []TimedOutRelayed, now time.Time, cfg RelayedWatchdogCfg) (rescued, unrescued []TimedOutRelayed) {
	if cfg.UrlWorkDir == "" {
		return nil, stuck
	}

	index, err := buildScoreIndex(cfg.UrlWorkDir)
	if err != nil {
		slog.Warn("watchdog score index build failed",
			"event_type", "relayed_watchdog_index_error",
			"url_work_dir", cfg.UrlWorkDir,
			"error", err,
		)
		return nil, stuck
	}
	if len(index) == 0 {
		return nil, stuck
	}

	for _, t := range stuck {
		s, ok := index[scoreIndexKey{URL: t.URL, Profile: t.Profile}]
		if !ok {
			unrescued = append(unrescued, t)
			continue
		}

		ingested, ierr := w.queue.IngestScoreIfRelayed(t.ID, s.Score, s.Tags, s.Verdict, s.Slug)
		if ierr != nil {
			slog.Error("watchdog ingest failed",
				"event_type", "relayed_watchdog_ingest_error",
				"id", t.ID,
				"error", ierr,
			)
			unrescued = append(unrescued, t)
			continue
		}
		if !ingested {
			// Row was already scored by a concurrent writer — treat as rescued
			// (no timeout event, no duplicate score_ingested).
			rescued = append(rescued, t)
			continue
		}

		// M2: emit score_ingested event.
		latencyMs := now.Sub(func() time.Time {
			if parsed, perr := time.Parse(time.RFC3339, t.QueuedAt); perr == nil {
				return parsed
			}
			return now
		}()).Milliseconds()

		slog.Info("score ingested from disk",
			"event_type", "score_ingested",
			"id", t.ID,
			"slug", s.Slug,
			"score", s.Score,
			"profile", t.Profile,
			"source", "watchdog_backfill",
			"latency_ms", latencyMs,
			"url", t.URL,
		)
		if w.events != nil {
			_ = w.events.Emit("score_ingested", map[string]interface{}{
				"id":         t.ID,
				"slug":       s.Slug,
				"score":      s.Score,
				"profile":    t.Profile,
				"source":     "watchdog_backfill",
				"latency_ms": latencyMs,
				"url":        t.URL,
			})
		}
		rescued = append(rescued, t)
	}
	return rescued, unrescued
}

// alertOnVolume maintains a rolling ring buffer of share_scoring_timeout
// timestamps. When the count within AlertWindow exceeds AlertThreshold, it
// enqueues one FCM push_outbox alert row (kind='alert', bypassing the EPIC-051
// digest dual-writer invariant which scopes only to kind='digest') and emits a
// share_scoring_timeout_alert event. A cooldown of AlertWindow prevents alert
// spam during sustained outages.
func (w *RelayedWatchdog) alertOnVolume(timedOut []TimedOutRelayed, now time.Time, cfg RelayedWatchdogCfg) {
	w.alertMu.Lock()
	defer w.alertMu.Unlock()

	// Append new timeout timestamps.
	for range timedOut {
		w.alertTimes = append(w.alertTimes, now)
	}

	// Prune entries outside the window.
	cutoff := now.Add(-cfg.AlertWindow)
	i := 0
	for i < len(w.alertTimes) && w.alertTimes[i].Before(cutoff) {
		i++
	}
	w.alertTimes = w.alertTimes[i:]

	count := len(w.alertTimes)
	if count <= cfg.AlertThreshold {
		return
	}

	// Cooldown: suppress if we fired an alert within AlertWindow.
	if !w.alertFiredAt.IsZero() && now.Sub(w.alertFiredAt) < cfg.AlertWindow {
		return
	}
	w.alertFiredAt = now

	windowSecs := int(cfg.AlertWindow.Seconds())
	slog.Warn("share_scoring_timeout volume alert",
		"event_type", "share_scoring_timeout_alert",
		"window_secs", windowSecs,
		"count", count,
		"threshold", cfg.AlertThreshold,
		"epic", "EPIC-055",
	)
	if w.events != nil {
		_ = w.events.Emit("share_scoring_timeout_alert", map[string]interface{}{
			"window_secs": windowSecs,
			"count":       count,
			"threshold":   cfg.AlertThreshold,
			"epic":        "EPIC-055",
		})
	}

	// Enqueue FCM alert directly via EnqueuePushWithProfile (kind='alert').
	// This bypasses EnqueueDigestIfDue intentionally — the EPIC-051 dual-writer
	// invariant scopes exclusively to kind='digest'. Alerts are exempt.
	if w.queue != nil {
		msg := "linkari: scoring pipeline stalled — see EPIC-055 for remediation"
		_, err := w.queue.EnqueuePushWithProfile("alert", "ops", 0, "", msg, "", "")
		if err != nil {
			slog.Error("watchdog alert enqueue failed",
				"event_type", "relayed_watchdog_alert_error",
				"error", err,
			)
		}
	}
}
