package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

const schemaVersion = 3

// openDB opens (or creates) the SQLite database and runs migrations.
func openDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, err
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema migration failed: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	var current int
	db.QueryRow("PRAGMA user_version").Scan(&current)
	if current >= schemaVersion {
		return nil
	}

	if current < 2 {
		if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS plaid_items (
  item_id        TEXT PRIMARY KEY,
  institution_id TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  status         TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS plaid_accounts (
  account_id    TEXT PRIMARY KEY,
  item_id       TEXT NOT NULL REFERENCES plaid_items(item_id),
  name          TEXT NOT NULL,
  official_name TEXT,
  subtype       TEXT NOT NULL,
  mask          TEXT
);

CREATE TABLE IF NOT EXISTS plaid_sync_state (
  item_id             TEXT PRIMARY KEY REFERENCES plaid_items(item_id),
  cursor              TEXT,
  last_sync_at        TEXT,
  next_sync_at        TEXT,
  retries             INTEGER NOT NULL DEFAULT 0,
  last_error_code     TEXT,
  last_error_at       TEXT,
  rate_limit_reset_at TEXT
);

CREATE TABLE IF NOT EXISTS plaid_sync_journal (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  item_id       TEXT NOT NULL REFERENCES plaid_items(item_id),
  sync_run_id   TEXT NOT NULL,
  started_at    TEXT NOT NULL,
  completed_at  TEXT,
  tx_added      INTEGER NOT NULL DEFAULT 0,
  tx_modified   INTEGER NOT NULL DEFAULT 0,
  tx_removed    INTEGER NOT NULL DEFAULT 0,
  cursor_before TEXT,
  cursor_after  TEXT,
  status        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS plaid_transactions_raw (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  plaid_txn_id TEXT UNIQUE NOT NULL,
  item_id      TEXT NOT NULL REFERENCES plaid_items(item_id),
  account_id   TEXT NOT NULL REFERENCES plaid_accounts(account_id),
  json_payload TEXT NOT NULL,
  ingested_at  TEXT NOT NULL,
  is_removed   INTEGER NOT NULL DEFAULT 0
);

PRAGMA user_version = 2;
`); err != nil {
			return err
		}
		current = 2
	}

	if current < 3 {
		if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS health_metrics (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  sampled_at   TEXT NOT NULL,
  item_id      TEXT NOT NULL,
  last_sync_at TEXT,
  tx_count_24h INTEGER NOT NULL DEFAULT 0,
  errors_24h   INTEGER NOT NULL DEFAULT 0
);

PRAGMA user_version = 3;
`); err != nil {
			return err
		}
	}

	return nil
}

// writeJournalEntry appends a completed sync run record to plaid_sync_journal.
// Takes explicit counts rather than *SyncResult to avoid coupling db.go to the F3 type.
func writeJournalEntry(db *sql.DB, itemID, runID string, added, modified, removed int, cursorBefore, cursorAfter, status string) error {
	_, err := db.Exec(`
		INSERT INTO plaid_sync_journal
		  (item_id, sync_run_id, started_at, completed_at,
		   tx_added, tx_modified, tx_removed,
		   cursor_before, cursor_after, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		itemID, runID, nowUTC(), nowUTC(),
		added, modified, removed,
		cursorBefore, cursorAfter, status,
	)
	return err
}

// markTransactionRemoved soft-deletes a transaction by setting is_removed=1.
// Idempotent — calling twice on the same txn_id is a no-op.
func markTransactionRemoved(db *sql.DB, plaidTxnID string) error {
	_, err := db.Exec(`UPDATE plaid_transactions_raw SET is_removed = 1 WHERE plaid_txn_id = ?`, plaidTxnID)
	return err
}

// setRateLimitReset records the rate-limit reset timestamp for an item.
// F4 must call this instead of issuing raw SQL against plaid_sync_state.
func setRateLimitReset(db *sql.DB, itemID, resetAt string) error {
	_, err := db.Exec(
		`UPDATE plaid_sync_state SET rate_limit_reset_at = ?, last_error_at = ? WHERE item_id = ?`,
		resetAt, nowUTC(), itemID,
	)
	return err
}
