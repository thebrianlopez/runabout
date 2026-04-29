package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func mustOpenDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "plaid.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func seedItem(t *testing.T, db *sql.DB, itemID string) {
	t.Helper()
	mustExec(t, db,
		`INSERT INTO plaid_items (item_id, institution_id, created_at, status) VALUES (?, 'ins_1', ?, 'active')`,
		itemID, nowUTC())
	mustExec(t, db,
		`INSERT INTO plaid_sync_state (item_id, retries) VALUES (?, 0)`, itemID)
}

func seedAccount(t *testing.T, db *sql.DB, accountID, itemID string) {
	t.Helper()
	mustExec(t, db,
		`INSERT INTO plaid_accounts (account_id, item_id, name, subtype) VALUES (?, ?, 'Checking', 'checking')`,
		accountID, itemID)
}

// ── CT-1: all 5 tables created on first run ───────────────────────────────

func TestCT1_AllTablesCreated(t *testing.T) {
	db := mustOpenDB(t)

	want := []string{
		"plaid_items",
		"plaid_accounts",
		"plaid_sync_state",
		"plaid_sync_journal",
		"plaid_transactions_raw",
	}

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		got = append(got, name)
	}

	wantSet := make(map[string]bool, len(want))
	for _, n := range want {
		wantSet[n] = true
	}
	for _, n := range got {
		delete(wantSet, n)
	}
	if len(wantSet) > 0 {
		t.Errorf("missing tables: %v (got %v)", wantSet, got)
	}
}

// ── CT-2: PRAGMA user_version = 1 after migration ────────────────────────

func TestCT2_UserVersion(t *testing.T) {
	db := mustOpenDB(t)
	var v int
	db.QueryRow("PRAGMA user_version").Scan(&v)
	if v != 2 {
		t.Errorf("user_version: got %d, want 2", v)
	}
}

// ── CT-3: duplicate plaid_txn_id silently succeeds ───────────────────────

func TestCT3_DuplicateTxnIDIdempotent(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	seedAccount(t, db, "acct_1", "item_1")

	payload := `{"transaction_id":"txn_abc"}`

	if err := upsertTransaction(db, "txn_abc", "item_1", "acct_1", payload); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := upsertTransaction(db, "txn_abc", "item_1", "acct_1", payload); err != nil {
		t.Errorf("second upsert (duplicate) should not error: %v", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM plaid_transactions_raw WHERE plaid_txn_id = 'txn_abc'`).Scan(&count)
	if count != 1 {
		t.Errorf("row count after duplicate insert: got %d, want 1", count)
	}
}

// ── CT-4: openDB is idempotent ────────────────────────────────────────────

func TestCT4_OpenDBIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plaid.db")

	db1, err := openDB(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	db1.Close()

	db2, err := openDB(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	db2.Close()
}

// ── CT-5: commitCursor updates cursor and last_sync_at ───────────────────

func TestCT5_CommitCursor(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	cursor := "cursor_xyz"
	if err := commitCursor(db, "item_1", cursor); err != nil {
		t.Fatalf("commitCursor: %v", err)
	}

	var got string
	db.QueryRow(`SELECT cursor FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&got)
	if got != cursor {
		t.Errorf("cursor: got %q, want %q", got, cursor)
	}

	var lastSync string
	db.QueryRow(`SELECT last_sync_at FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&lastSync)
	if lastSync == "" {
		t.Error("last_sync_at should be populated after commitCursor")
	}
}

// ── CT-6: writeJournalEntry appends without overwriting prior entries ─────

func TestCT6_WriteJournalEntryAppends(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	if err := writeJournalEntry(db, "item_1", "run_1", 5, 1, 0, "", "cur_a", "success"); err != nil {
		t.Fatalf("first entry: %v", err)
	}
	if err := writeJournalEntry(db, "item_1", "run_2", 3, 0, 1, "cur_a", "cur_b", "success"); err != nil {
		t.Fatalf("second entry: %v", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM plaid_sync_journal WHERE item_id = 'item_1'`).Scan(&count)
	if count != 2 {
		t.Errorf("journal row count: got %d, want 2", count)
	}

	var id1, id2 int
	db.QueryRow(`SELECT id FROM plaid_sync_journal WHERE sync_run_id = 'run_1'`).Scan(&id1)
	db.QueryRow(`SELECT id FROM plaid_sync_journal WHERE sync_run_id = 'run_2'`).Scan(&id2)
	if id1 == id2 {
		t.Error("two entries should have distinct autoincrement IDs")
	}
}

// ── CT-7: foreign key enforcement active ─────────────────────────────────

func TestCT7_ForeignKeyEnforcement(t *testing.T) {
	db := mustOpenDB(t)

	_, err := db.Exec(`INSERT INTO plaid_accounts (account_id, item_id, name, subtype) VALUES ('acct_x', 'nonexistent_item', 'Test', 'checking')`)
	if err == nil {
		t.Error("expected FK violation error, got nil")
	}
}

// ── CT-8: round-trip insert+retrieve for each table ──────────────────────

func TestCT8_RoundTrip(t *testing.T) {
	db := mustOpenDB(t)

	// plaid_items
	mustExec(t, db, `INSERT INTO plaid_items (item_id, institution_id, created_at, status) VALUES ('i1', 'ins_1', '2026-04-28T00:00:00Z', 'active')`)
	var status string
	db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'i1'`).Scan(&status)
	if status != "active" {
		t.Errorf("plaid_items round-trip: status got %q, want 'active'", status)
	}

	// plaid_accounts
	mustExec(t, db, `INSERT INTO plaid_accounts (account_id, item_id, name, subtype) VALUES ('a1', 'i1', 'Checking', 'checking')`)
	var aName string
	db.QueryRow(`SELECT name FROM plaid_accounts WHERE account_id = 'a1'`).Scan(&aName)
	if aName != "Checking" {
		t.Errorf("plaid_accounts round-trip: name got %q, want 'Checking'", aName)
	}

	// plaid_sync_state
	mustExec(t, db, `INSERT INTO plaid_sync_state (item_id, retries) VALUES ('i1', 0)`)
	var retries int
	db.QueryRow(`SELECT retries FROM plaid_sync_state WHERE item_id = 'i1'`).Scan(&retries)
	if retries != 0 {
		t.Errorf("plaid_sync_state round-trip: retries got %d, want 0", retries)
	}

	// plaid_sync_journal
	if err := writeJournalEntry(db, "i1", "run1", 1, 0, 0, "", "cur1", "success"); err != nil {
		t.Fatalf("writeJournalEntry: %v", err)
	}
	var jStatus string
	db.QueryRow(`SELECT status FROM plaid_sync_journal WHERE sync_run_id = 'run1'`).Scan(&jStatus)
	if jStatus != "success" {
		t.Errorf("plaid_sync_journal round-trip: status got %q, want 'success'", jStatus)
	}

	// plaid_transactions_raw
	if err := upsertTransaction(db, "txn1", "i1", "a1", `{"id":"txn1"}`); err != nil {
		t.Fatalf("upsertTransaction: %v", err)
	}
	var txnID string
	db.QueryRow(`SELECT plaid_txn_id FROM plaid_transactions_raw WHERE plaid_txn_id = 'txn1'`).Scan(&txnID)
	if txnID != "txn1" {
		t.Errorf("plaid_transactions_raw round-trip: txn_id got %q, want 'txn1'", txnID)
	}
}

// ── BT-1: openDB creates parent directory ─────────────────────────────────

func TestBT1_OpenDBCreatesDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "plaid.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatalf("openDB with nested path: %v", err)
	}
	db.Close()
}

// ── BT-2: migrate is idempotent on existing v1 schema ────────────────────

func TestBT2_MigrateIdempotent(t *testing.T) {
	db := mustOpenDB(t)

	if err := migrate(db); err != nil {
		t.Fatalf("second migrate call: %v", err)
	}

	var v int
	db.QueryRow("PRAGMA user_version").Scan(&v)
	if v != 2 {
		t.Errorf("user_version after re-migrate: got %d, want 2", v)
	}
}

// ── BT-3: WAL mode active ─────────────────────────────────────────────────

func TestBT3_WALMode(t *testing.T) {
	db := mustOpenDB(t)
	var mode string
	db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if mode != "wal" {
		t.Errorf("journal_mode: got %q, want 'wal'", mode)
	}
}

// ── BT-4: setRateLimitReset updates rate_limit_reset_at ──────────────────

func TestBT4_SetRateLimitReset(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	resetAt := "2026-04-28T02:00:00Z"
	if err := setRateLimitReset(db, "item_1", resetAt); err != nil {
		t.Fatalf("setRateLimitReset: %v", err)
	}

	var got string
	db.QueryRow(`SELECT rate_limit_reset_at FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&got)
	if got != resetAt {
		t.Errorf("rate_limit_reset_at: got %q, want %q", got, resetAt)
	}
}

// ── RG-1: rollback leaves no partial row in plaid_sync_state ─────────────

func TestRG1_RollbackLeavesNoPartialRow(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	tx.Exec(`UPDATE plaid_sync_state SET cursor = 'partial_cursor' WHERE item_id = 'item_1'`)
	tx.Rollback()

	var cursor sql.NullString
	db.QueryRow(`SELECT cursor FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&cursor)
	if cursor.Valid && cursor.String == "partial_cursor" {
		t.Error("rollback should have prevented partial cursor write")
	}
}
