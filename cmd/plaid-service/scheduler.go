package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

const (
	defaultHourlyCron  = "0 * * * *"
	defaultDailyCron   = "0 6 * * *"
	defaultConcurrency = 5
	maxRetries         = 4
)

// Scheduler drives hourly and daily sync ticks across all active items.
type Scheduler struct {
	db      *sql.DB
	client  PlaidClient
	secrets *TokenStore
	cron    *cron.Cron
}

func newScheduler(db *sql.DB, client PlaidClient, secrets *TokenStore) *Scheduler {
	return &Scheduler{db: db, client: client, secrets: secrets}
}

// Start registers cron jobs and begins the scheduler loop.
func (s *Scheduler) Start() error {
	s.cron = cron.New()

	hourlyCron := getenv("PLAID_HOURLY_CRON")
	if hourlyCron == "" {
		hourlyCron = defaultHourlyCron
	}
	dailyCron := getenv("PLAID_DAILY_CRON")
	if dailyCron == "" {
		dailyCron = defaultDailyCron
	}

	if _, err := s.cron.AddFunc(hourlyCron, s.tick); err != nil {
		return fmt.Errorf("scheduler startup failed: %w", err)
	}
	if _, err := s.cron.AddFunc(dailyCron, s.tick); err != nil {
		return fmt.Errorf("scheduler startup failed: %w", err)
	}

	s.cron.Start()
	return nil
}

// Stop waits for in-flight syncs to drain before returning.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}

// tick fans out a sync run across all active items.
func (s *Scheduler) tick() {
	tickRunID := uuid.New().String()
	ctx := context.WithValue(context.Background(), syncRunIDKey{}, tickRunID)

	items, err := s.activeItems(ctx)
	if err != nil {
		emitToolFailure(tickRunID, "", "scheduler_startup_failed", "", err.Error())
		return
	}

	maxConc := defaultConcurrency
	if maxConc > len(items) {
		maxConc = len(items)
	}
	if maxConc == 0 {
		emitServiceHealth(tickRunID, 0, 0, 0, 0, 0, 0)
		return
	}

	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var synced, deferred, loginRequired int

	for _, itemID := range items {
		if s.isDeferred(itemID) {
			mu.Lock()
			deferred++
			mu.Unlock()
			continue
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					emitToolFailure(tickRunID, id, "fan_out_panic", "", fmt.Sprintf("%v", r))
				}
			}()

			res, err := s.client.SyncTransactions(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				s.handleSyncError(ctx, tickRunID, id, err)
				var syncErr *SyncError
				if errors.As(err, &syncErr) && syncErr.EventClass == "vendor_auth_required" {
					loginRequired++
				}
				return
			}
			commitCursor(s.db, id, res.Cursor)
			writeJournalEntry(s.db, id, res.RunID, res.Added, res.Modified, res.Removed, "", res.Cursor, "success")
			emitTransactionBatch(tickRunID, id, res)
			synced++
		}(itemID)
	}

	wg.Wait()
	emitServiceHealth(tickRunID, len(items), synced, deferred, loginRequired, computeOldestUnsyncedHrs(s.db), 0)
}

// computeOldestUnsyncedHrs returns hours since the oldest last_sync_at across active items.
// Returns 0 if no items have synced yet.
func computeOldestUnsyncedHrs(db *sql.DB) float64 {
	var minSync sql.NullString
	db.QueryRow(`
		SELECT MIN(ss.last_sync_at)
		FROM plaid_sync_state ss
		JOIN plaid_items pi ON pi.item_id = ss.item_id
		WHERE pi.status = 'active'
	`).Scan(&minSync)
	if !minSync.Valid || minSync.String == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, minSync.String)
	if err != nil {
		return 0
	}
	if h := time.Since(t).Hours(); h > 0 {
		return h
	}
	return 0
}

type syncRunIDKey struct{}

// activeItems returns all item IDs with status='active'.
func (s *Scheduler) activeItems(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT item_id FROM plaid_items WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// isDeferred returns true when rate_limit_reset_at is in the future.
func (s *Scheduler) isDeferred(itemID string) bool {
	var resetAt sql.NullString
	s.db.QueryRow(
		`SELECT rate_limit_reset_at FROM plaid_sync_state WHERE item_id = ?`, itemID,
	).Scan(&resetAt)

	if !resetAt.Valid || resetAt.String == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, resetAt.String)
	return err == nil && time.Now().Before(t)
}

// handleSyncError applies backoff and error classification on sync failure.
func (s *Scheduler) handleSyncError(ctx context.Context, runID, itemID string, err error) {
	var syncErr *SyncError
	var errClass string
	if errors.As(err, &syncErr) {
		errClass = syncErr.EventClass
	} else {
		errClass = "cursor_corrupted"
	}
	emitToolFailure(runID, itemID, errClass, "", err.Error())

	if errClass == "vendor_auth_required" {
		s.db.ExecContext(ctx,
			`UPDATE plaid_items SET status = 'login_required' WHERE item_id = ?`, itemID)
		return
	}

	var retries int
	s.db.QueryRow(
		`SELECT retries FROM plaid_sync_state WHERE item_id = ?`, itemID).Scan(&retries)

	retries++
	if retries >= maxRetries {
		s.db.ExecContext(ctx,
			`UPDATE plaid_items SET status = 'error' WHERE item_id = ?`, itemID)
		s.db.ExecContext(ctx,
			`UPDATE plaid_sync_state SET retries = ?, last_error_code = ?, last_error_at = ? WHERE item_id = ?`,
			retries, errClass, nowUTC(), itemID)
		return
	}

	backoffs := []time.Duration{5 * time.Minute, 15 * time.Minute, 60 * time.Minute}
	var delay time.Duration
	if retries-1 < len(backoffs) {
		delay = backoffs[retries-1]
	} else {
		delay = 60 * time.Minute
	}
	nextSync := time.Now().Add(delay).Format(time.RFC3339)

	s.db.ExecContext(ctx,
		`UPDATE plaid_sync_state SET retries = ?, last_error_code = ?, last_error_at = ?, next_sync_at = ? WHERE item_id = ?`,
		retries, errClass, nowUTC(), nextSync, itemID)

	if errClass == "vendor_rate_limited" {
		setRateLimitReset(s.db, itemID, nextSync)
		emitRateLimit(runID, itemID, 60, retries)
	}
}
