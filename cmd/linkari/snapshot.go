package main

import (
	"context"
	"log/slog"
	"time"
)

// SnapshotWorker periodically writes a clean copy of queue.db via VACUUM INTO.
// A single rotating backup is kept at DestPath — old content is overwritten on
// each tick. This gives one clean recovery baseline; if the live DB becomes
// corrupt, the snapshot is the starting point before attempting sqlite3 .recover.
//
// The worker is intentionally simpler than RelayedWatchdog: no hot-reload, no
// alert path. Interval and DestPath are fixed at construction.
type SnapshotWorker struct {
	queue    *Queue
	interval time.Duration
	destPath string
}

// NewSnapshotWorker constructs a worker. Run is a no-op when interval <= 0.
func NewSnapshotWorker(q *Queue, interval time.Duration, destPath string) *SnapshotWorker {
	return &SnapshotWorker{queue: q, interval: interval, destPath: destPath}
}

// Run ticks at the configured interval until ctx is cancelled. Each tick calls
// snapshotOnce. Extracted so tests can drive snapshots synchronously.
func (w *SnapshotWorker) Run(ctx context.Context) {
	if w.interval <= 0 {
		slog.Info("snapshot worker disabled", "event_type", "snapshot_worker_disabled")
		return
	}
	slog.Info(
		"snapshot worker started",
		"event_type", "snapshot_worker_started",
		"interval_secs", int(w.interval.Seconds()),
		"dest", w.destPath,
	)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.snapshotOnce()
		}
	}
}

func (w *SnapshotWorker) snapshotOnce() {
	if err := w.queue.Snapshot(w.destPath); err != nil {
		slog.Error(
			"snapshot failed",
			"event_type", "snapshot_error",
			"dest", w.destPath,
			"error", err,
		)
		return
	}
	slog.Info(
		"snapshot written",
		"event_type", "snapshot_written",
		"dest", w.destPath,
	)
}
