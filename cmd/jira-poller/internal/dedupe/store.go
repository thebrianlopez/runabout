// Package dedupe provides an idempotency store backed by SQLite.
// It records processed event IDs using INSERT OR IGNORE so overlapping
// poll windows never deliver duplicate events.
package dedupe

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // register "sqlite" driver
)

//go:embed schema.sql
var schemaDDL string

// ErrDedupeWrite is returned when the SQLite INSERT fails for a non-duplicate reason.
var ErrDedupeWrite = errors.New("dedupe: store write failed")

// DedupeStore records processed event IDs atomically.
type DedupeStore interface {
	// Mark attempts to record eventID.
	//   isNew=true, nil   — first time seen; caller should publish.
	//   isNew=false, nil  — already seen; caller should skip.
	//   false, err        — store error; caller should skip and log.
	Mark(ctx context.Context, eventID string, ttl time.Duration) (isNew bool, err error)
}

// ApplySchema runs the schema DDL. Call once at startup.
func ApplySchema(db *sql.DB) error {
	_, err := db.Exec(schemaDDL)
	if err != nil {
		return fmt.Errorf("dedupe: apply schema: %w", err)
	}
	return nil
}

// OpenDB opens (or creates) a SQLite database with WAL mode and a busy timeout.
// path may be ":memory:" for tests.
func OpenDB(path string) (*sql.DB, error) {
	dsn := path + "?_journal=WAL&_timeout=5000"
	if path == ":memory:" {
		// In-memory DBs don't support WAL.
		dsn = path
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("dedupe: open db: %w", err)
	}
	return db, nil
}

// sqliteStore implements DedupeStore using SQLite.
type sqliteStore struct {
	db    *sql.DB
	nowFn func() time.Time
}

// NewSQLiteStore returns a DedupeStore backed by the given *sql.DB.
// nowFn is injected for deterministic time in tests; pass time.Now in production.
func NewSQLiteStore(db *sql.DB, nowFn func() time.Time) DedupeStore {
	return &sqliteStore{db: db, nowFn: nowFn}
}

// Mark implements DedupeStore.
func (s *sqliteStore) Mark(ctx context.Context, eventID string, ttl time.Duration) (bool, error) {
	now := s.nowFn().Unix()
	expiresAt := now + int64(ttl.Seconds())

	result, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO seen_events (event_id, seen_at, expires_at) VALUES (?, ?, ?)`,
		eventID, now, expiresAt,
	)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrDedupeWrite, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%w: rows affected: %w", ErrDedupeWrite, err)
	}

	return rows > 0, nil
}

// CleanupExpired deletes rows whose TTL has elapsed. Intended for use in a
// periodic cleanup goroutine. nowFn is the clock source.
func CleanupExpired(ctx context.Context, db *sql.DB, nowFn func() time.Time) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM seen_events WHERE expires_at < ?`,
		nowFn().Unix(),
	)
	if err != nil {
		return fmt.Errorf("dedupe: cleanup expired: %w", err)
	}
	return nil
}
