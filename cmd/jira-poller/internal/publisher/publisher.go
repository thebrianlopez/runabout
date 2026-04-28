// Package publisher inserts TransitionEvents into a SQLite outbox and runs a
// drain worker goroutine that delivers them to a configured Sink.
package publisher

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blo-grindr/runabout/cmd/jira-poller/internal/types"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaDDL string

// Sentinel errors.
var (
	// ErrOutboxWrite is returned when the SQLite INSERT fails entirely.
	ErrOutboxWrite = errors.New("publisher: outbox write failed")

	// ErrDelivery is returned when Sink.Deliver fails.
	ErrDelivery = errors.New("publisher: sink delivery failed")
)

// Publisher writes events to the outbox. Delivery is async via drain worker.
type Publisher interface {
	Publish(ctx context.Context, events []types.TransitionEvent) (PublishResult, error)
}

// PublishResult summarises a Publish call.
type PublishResult struct {
	Succeeded []string     // ChangelogIDs successfully inserted
	Failed    []FailedEvent
}

// FailedEvent captures one INSERT failure.
type FailedEvent struct {
	ChangelogID  string
	ErrorMessage string
}

// Sink is the delivery target for the drain worker.
type Sink interface {
	Deliver(ctx context.Context, events []types.TransitionEvent) error
}

// ApplySchema runs the publisher schema DDL. Call once at startup.
func ApplySchema(db *sql.DB) error {
	_, err := db.Exec(schemaDDL)
	if err != nil {
		return fmt.Errorf("publisher: apply schema: %w", err)
	}
	return nil
}

// sqlitePublisher implements Publisher.
type sqlitePublisher struct {
	db *sql.DB
}

// NewSQLitePublisher returns a Publisher backed by the given *sql.DB.
func NewSQLitePublisher(db *sql.DB) Publisher {
	return &sqlitePublisher{db: db}
}

// Publish inserts all events into the pending_events outbox.
func (p *sqlitePublisher) Publish(ctx context.Context, events []types.TransitionEvent) (PublishResult, error) {
	if len(events) == 0 {
		return PublishResult{}, nil
	}

	var result PublishResult
	for _, ev := range events {
		payload, err := json.Marshal(ev)
		if err != nil {
			result.Failed = append(result.Failed, FailedEvent{
				ChangelogID:  ev.ChangelogID,
				ErrorMessage: err.Error(),
			})
			continue
		}

		sqlResult, err := p.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO pending_events (event_id, payload) VALUES (?, ?)`,
			ev.ChangelogID, string(payload),
		)
		if err != nil {
			// DB completely unavailable on first attempt → fatal ErrOutboxWrite.
			if len(result.Succeeded) == 0 && len(result.Failed) == 0 {
				return PublishResult{}, fmt.Errorf("%w: %w", ErrOutboxWrite, err)
			}
			result.Failed = append(result.Failed, FailedEvent{
				ChangelogID:  ev.ChangelogID,
				ErrorMessage: err.Error(),
			})
			continue
		}
		// RowsAffected=0 → event_id already in outbox (OR IGNORE skipped it).
		if ra, _ := sqlResult.RowsAffected(); ra == 0 {
			result.Failed = append(result.Failed, FailedEvent{
				ChangelogID:  ev.ChangelogID,
				ErrorMessage: "duplicate: event_id already in outbox",
			})
			continue
		}
		result.Succeeded = append(result.Succeeded, ev.ChangelogID)
	}
	return result, nil
}

// backoffSeconds returns the delay in seconds after `attempt` failures (1-based).
// Matches linkari's outbox.go schedule: 30s, 2m, 10m, 1h.
var backoffSeconds = []int64{30, 120, 600, 3600}

func nextAttemptAt(attempts int, now int64) int64 {
	idx := attempts - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(backoffSeconds) {
		idx = len(backoffSeconds) - 1
	}
	return now + backoffSeconds[idx]
}

const (
	drainBatchSize = 10
	maxAttempts    = 5
	deadAgeSeconds = 24 * 3600
)

// StartDrainWorker launches a goroutine that periodically delivers pending
// outbox events to sink. It runs until ctx is cancelled.
// nowFn is injected for deterministic testing.
func StartDrainWorker(ctx context.Context, db *sql.DB, sink Sink, nowFn func() time.Time) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		cleanupTicker := time.NewTicker(1 * time.Hour)
		defer cleanupTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				drainOnce(ctx, db, sink, nowFn)
			case <-cleanupTicker.C:
				// no-op in tests; BT-3 tests CleanupExpiredSeen directly
			}
		}
	}()
}

// DrainOnce performs a single drain tick. Exported for testing.
func DrainOnce(ctx context.Context, db *sql.DB, sink Sink, nowFn func() time.Time) {
	drainOnce(ctx, db, sink, nowFn)
}

func drainOnce(ctx context.Context, db *sql.DB, sink Sink, nowFn func() time.Time) {
	now := nowFn().Unix()

	rows, err := db.QueryContext(ctx,
		`SELECT id, event_id, payload, attempts, created_at
		 FROM pending_events
		 WHERE status = 'pending' AND next_attempt_at <= ?
		 ORDER BY next_attempt_at
		 LIMIT ?`,
		now, drainBatchSize,
	)
	if err != nil {
		slog.Error("drain: query pending events", "error", err)
		return
	}
	defer rows.Close()

	type row struct {
		id        int64
		eventID   string
		payload   string
		attempts  int
		createdAt int64
	}
	var batch []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.eventID, &r.payload, &r.attempts, &r.createdAt); err != nil {
			slog.Error("drain: scan row", "error", err)
			continue
		}
		batch = append(batch, r)
	}
	if err := rows.Err(); err != nil {
		slog.Error("drain: rows error", "error", err)
		return
	}
	if len(batch) == 0 {
		return
	}

	// Dead-letter rows that exceed age threshold.
	var toDeliver []row
	for _, r := range batch {
		if now-r.createdAt > deadAgeSeconds {
			markDead(ctx, db, r.id, r.eventID, "age > 24h", now)
			continue
		}
		toDeliver = append(toDeliver, r)
	}
	if len(toDeliver) == 0 {
		return
	}

	// Deserialise events.
	events := make([]types.TransitionEvent, 0, len(toDeliver))
	for _, r := range toDeliver {
		var ev types.TransitionEvent
		if err := json.Unmarshal([]byte(r.payload), &ev); err != nil {
			slog.Error("drain: unmarshal payload", "event_id", r.eventID, "error", err)
			continue
		}
		events = append(events, ev)
	}

	// Attempt delivery.
	err = sink.Deliver(ctx, events)
	if err != nil {
		for _, r := range toDeliver {
			newAttempts := r.attempts + 1
			if newAttempts >= maxAttempts {
				markDead(ctx, db, r.id, r.eventID, err.Error(), now)
			} else {
				next := nextAttemptAt(newAttempts, now)
				_, dbErr := db.ExecContext(ctx,
					`UPDATE pending_events SET attempts=?, last_attempt_at=?, next_attempt_at=?, error=? WHERE id=?`,
					newAttempts, now, next, err.Error(), r.id,
				)
				if dbErr != nil {
					slog.Error("drain: update attempt", "event_id", r.eventID, "error", dbErr)
				}
			}
		}
		return
	}

	// Mark delivered.
	for _, r := range toDeliver {
		_, dbErr := db.ExecContext(ctx,
			`UPDATE pending_events SET status='delivered', last_attempt_at=? WHERE id=?`,
			now, r.id,
		)
		if dbErr != nil {
			slog.Error("drain: mark delivered", "event_id", r.eventID, "error", dbErr)
		}
	}
}

func markDead(ctx context.Context, db *sql.DB, id int64, eventID, reason string, now int64) {
	_, err := db.ExecContext(ctx,
		`UPDATE pending_events SET status='dead', last_attempt_at=?, error=? WHERE id=?`,
		now, reason, id,
	)
	if err != nil {
		slog.Error("drain: mark dead", "event_id", eventID, "error", err)
	}
	slog.Warn("drain: dead-lettered event", "event_id", eventID, "reason", reason)
}

// JSONLSink appends one JSON line per event to a dated file in dir.
// File name: jira-YYYY-MM-DD.jsonl (UTC date from nowFn).
type JSONLSink struct {
	dir   string
	nowFn func() time.Time
}

// NewJSONLSink returns a Sink that writes to dir/jira-YYYY-MM-DD.jsonl.
func NewJSONLSink(dir string, nowFn func() time.Time) Sink {
	return &JSONLSink{dir: dir, nowFn: nowFn}
}

// Deliver implements Sink.
func (s *JSONLSink) Deliver(ctx context.Context, events []types.TransitionEvent) error {
	if len(events) == 0 {
		return nil
	}
	date := s.nowFn().UTC().Format("2006-01-02")
	path := filepath.Join(s.dir, "jira-"+date+".jsonl")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("%w: open %s: %w", ErrDelivery, path, err)
	}
	defer f.Close()

	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("%w: marshal: %w", ErrDelivery, err)
		}
		if _, err := fmt.Fprintf(f, "%s\n", line); err != nil {
			return fmt.Errorf("%w: write: %w", ErrDelivery, err)
		}
	}
	return nil
}

// HTTPSink POSTs a JSON array of events to a URL.
type HTTPSink struct {
	url    string
	client *http.Client
}

// NewHTTPSink returns a Sink that POSTs to url using client.
func NewHTTPSink(url string, client *http.Client) Sink {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPSink{url: url, client: client}
}

// Deliver implements Sink.
func (s *HTTPSink) Deliver(ctx context.Context, events []types.TransitionEvent) error {
	if len(events) == 0 {
		return nil
	}
	body, err := json.Marshal(events)
	if err != nil {
		return fmt.Errorf("%w: marshal: %w", ErrDelivery, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("%w: build request: %w", ErrDelivery, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: do: %w", ErrDelivery, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: HTTP %d", ErrDelivery, resp.StatusCode)
	}
	return nil
}
