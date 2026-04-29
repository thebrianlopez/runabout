package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// ── mock PlaidClient ──────────────────────────────────────────────────────────

type mockPlaidClient struct {
	syncTransactions    func(ctx context.Context, itemID string) (*SyncResult, error)
	getAccounts         func(ctx context.Context, itemID string) ([]Account, error)
	createLinkToken     func(ctx context.Context) (string, error)
	exchangePublicToken func(ctx context.Context, publicToken string) (string, string, error)
}

func (m *mockPlaidClient) SyncTransactions(ctx context.Context, itemID string) (*SyncResult, error) {
	if m.syncTransactions != nil {
		return m.syncTransactions(ctx, itemID)
	}
	return &SyncResult{Cursor: "cur_" + itemID, RunID: "run_" + itemID}, nil
}

func (m *mockPlaidClient) GetAccounts(ctx context.Context, itemID string) ([]Account, error) {
	if m.getAccounts != nil {
		return m.getAccounts(ctx, itemID)
	}
	return nil, nil
}

func (m *mockPlaidClient) CreateLinkToken(ctx context.Context) (string, error) {
	return "", nil
}

func (m *mockPlaidClient) ExchangePublicToken(ctx context.Context, pt string) (string, string, error) {
	return "", "", nil
}

// mustScheduler creates a Scheduler wired to the given mock and DB, with tick() callable directly.
func mustScheduler(t *testing.T, client PlaidClient) (*Scheduler, *mockPlaidClient) {
	t.Helper()
	mc, ok := client.(*mockPlaidClient)
	if !ok {
		mc = nil
	}
	db := mustOpenDB(t)
	s := newScheduler(db, client, nil)
	return s, mc
}

// ── CT-1: isDeferred true when rate_limit_reset_at is in future ──────────────

func TestCT1_IsDeferredFuture(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	mustExec(t, db, `UPDATE plaid_sync_state SET rate_limit_reset_at = ? WHERE item_id = 'item_1'`, future)

	s := newScheduler(db, &mockPlaidClient{}, nil)
	if !s.isDeferred("item_1") {
		t.Error("isDeferred should return true when rate_limit_reset_at is in the future")
	}
}

// ── CT-2: isDeferred false when rate_limit_reset_at is in past ───────────────

func TestCT2_IsDeferredPast(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	past := "2000-01-01T00:00:00Z"
	mustExec(t, db, `UPDATE plaid_sync_state SET rate_limit_reset_at = ? WHERE item_id = 'item_1'`, past)

	s := newScheduler(db, &mockPlaidClient{}, nil)
	if s.isDeferred("item_1") {
		t.Error("isDeferred should return false when rate_limit_reset_at is in the past")
	}
}

// ── CT-3: commitCursor called after successful SyncTransactions ───────────────

func TestCT3_CursorCommittedOnSuccess(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	seedAccount(t, db, "acct_1", "item_1")

	client := &mockPlaidClient{
		syncTransactions: func(_ context.Context, itemID string) (*SyncResult, error) {
			return &SyncResult{Cursor: "committed_cursor", RunID: "run_1", Added: 2}, nil
		},
	}
	s := newScheduler(db, client, nil)
	s.tick()

	var cursor string
	db.QueryRow(`SELECT COALESCE(cursor, '') FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&cursor)
	if cursor != "committed_cursor" {
		t.Errorf("cursor: got %q, want %q", cursor, "committed_cursor")
	}
}

// ── CT-4: item skipped in tick() when isDeferred ─────────────────────────────

func TestCT4_DeferredItemSkipped(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	mustExec(t, db, `UPDATE plaid_sync_state SET rate_limit_reset_at = ? WHERE item_id = 'item_1'`, future)

	syncCalled := false
	client := &mockPlaidClient{
		syncTransactions: func(_ context.Context, _ string) (*SyncResult, error) {
			syncCalled = true
			return &SyncResult{}, nil
		},
	}
	s := newScheduler(db, client, nil)
	s.tick()

	if syncCalled {
		t.Error("SyncTransactions should NOT be called for a deferred item")
	}
}

// ── CT-5: handleSyncError uses SyncError.EventClass (not re-classifies) ──────

func TestCT5_HandleSyncErrorUsesEventClass(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	s := newScheduler(db, &mockPlaidClient{}, nil)
	syncErr := &SyncError{EventClass: "vendor_unavailable", PlaidCode: "INSTITUTION_DOWN", Err: errors.New("down")}
	s.handleSyncError(context.Background(), "run_1", "item_1", syncErr)

	// vendor_unavailable should NOT set status=login_required or status=error (first retry)
	var status string
	db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_1'`).Scan(&status)
	if status != "active" {
		t.Errorf("status: got %q, want 'active' after first vendor_unavailable", status)
	}
	// retries should be incremented
	var retries int
	db.QueryRow(`SELECT retries FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&retries)
	if retries != 1 {
		t.Errorf("retries: got %d, want 1", retries)
	}
}

// ── CT-6: first error sets next_sync_at ≈ now+5min, retries=1 ────────────────

func TestCT6_BackoffFirstRetry(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	s := newScheduler(db, &mockPlaidClient{}, nil)
	before := time.Now().UTC()
	s.handleSyncError(context.Background(), "run_1", "item_1",
		&SyncError{EventClass: "cursor_corrupted", Err: errors.New("err")})

	var nextSync string
	db.QueryRow(`SELECT COALESCE(next_sync_at, '') FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&nextSync)
	if nextSync == "" {
		t.Fatal("next_sync_at should be set after first error")
	}

	t2, err := time.Parse(time.RFC3339, nextSync)
	if err != nil {
		t.Fatalf("parse next_sync_at: %v", err)
	}
	lo := before.Add(4 * time.Minute)
	hi := before.Add(6 * time.Minute)
	if t2.Before(lo) || t2.After(hi) {
		t.Errorf("next_sync_at %v not in [now+4m, now+6m]", t2)
	}

	var retries int
	db.QueryRow(`SELECT retries FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&retries)
	if retries != 1 {
		t.Errorf("retries: got %d, want 1", retries)
	}
}

// ── CT-7: 4th error sets plaid_items.status = 'error' ────────────────────────

func TestCT7_FourthRetryStatusError(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	mustExec(t, db, `UPDATE plaid_sync_state SET retries = 3 WHERE item_id = 'item_1'`)

	s := newScheduler(db, &mockPlaidClient{}, nil)
	s.handleSyncError(context.Background(), "run_1", "item_1",
		&SyncError{EventClass: "cursor_corrupted", Err: errors.New("err")})

	var status string
	db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_1'`).Scan(&status)
	if status != "error" {
		t.Errorf("status: got %q, want 'error' after 4th retry", status)
	}
}

// ── CT-8: goroutine panic recovered; other items still sync ──────────────────

func TestCT8_PanicRecoveredOtherItemsContinue(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	seedItem(t, db, "item_2")

	client := &mockPlaidClient{
		syncTransactions: func(_ context.Context, itemID string) (*SyncResult, error) {
			if itemID == "item_1" {
				panic("simulated panic")
			}
			return &SyncResult{Cursor: "cur_item_2", RunID: "run_2"}, nil
		},
	}
	s := newScheduler(db, client, nil)

	// tick() must not panic or hang
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.tick()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tick() hung — panic recovery may be missing")
	}

	// item_2 cursor should be committed despite item_1 panic
	var cursor string
	db.QueryRow(`SELECT COALESCE(cursor, '') FROM plaid_sync_state WHERE item_id = 'item_2'`).Scan(&cursor)
	if cursor != "cur_item_2" {
		t.Errorf("item_2 cursor: got %q, want %q", cursor, "cur_item_2")
	}
}

// ── BT-1: activeItems returns only status='active' items ─────────────────────

func TestBT1_ActiveItemsFilter(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "active_1")
	seedItem(t, db, "active_2")
	seedItem(t, db, "login_req")
	seedItem(t, db, "err_item")
	mustExec(t, db, `UPDATE plaid_items SET status = 'login_required' WHERE item_id = 'login_req'`)
	mustExec(t, db, `UPDATE plaid_items SET status = 'error' WHERE item_id = 'err_item'`)

	s := newScheduler(db, &mockPlaidClient{}, nil)
	items, err := s.activeItems(context.Background())
	if err != nil {
		t.Fatalf("activeItems: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("activeItems count: got %d, want 2 (got %v)", len(items), items)
	}
	for _, id := range items {
		if id != "active_1" && id != "active_2" {
			t.Errorf("unexpected item in activeItems: %q", id)
		}
	}
}

// ── BT-3: writeJournalEntry called with correct counts after success ──────────

func TestBT3_JournalEntryOnSuccess(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	seedAccount(t, db, "acct_1", "item_1")

	client := &mockPlaidClient{
		syncTransactions: func(_ context.Context, _ string) (*SyncResult, error) {
			return &SyncResult{Added: 3, Modified: 1, Removed: 0, Cursor: "c1", RunID: "run_x"}, nil
		},
	}
	s := newScheduler(db, client, nil)
	s.tick()

	var added, modified int
	db.QueryRow(`SELECT tx_added, tx_modified FROM plaid_sync_journal WHERE item_id = 'item_1'`).Scan(&added, &modified)
	if added != 3 {
		t.Errorf("journal tx_added: got %d, want 3", added)
	}
	if modified != 1 {
		t.Errorf("journal tx_modified: got %d, want 1", modified)
	}
}

// ── BT-4: vendor_auth_required → no backoff, no retry increment ──────────────

func TestBT4_AuthRequiredNoBackoff(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	s := newScheduler(db, &mockPlaidClient{}, nil)
	s.handleSyncError(context.Background(), "run_1", "item_1",
		&SyncError{EventClass: "vendor_auth_required", Err: errors.New("login required")})

	var retries int
	var nextSync string
	db.QueryRow(`SELECT retries, COALESCE(next_sync_at, '') FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&retries, &nextSync)
	if retries != 0 {
		t.Errorf("retries: got %d, want 0 (no backoff for auth errors)", retries)
	}
	if nextSync != "" {
		t.Errorf("next_sync_at should not be set for auth error, got %q", nextSync)
	}

	var status string
	db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_1'`).Scan(&status)
	if status != "login_required" {
		t.Errorf("status: got %q, want 'login_required'", status)
	}
}

// ── RG-1: cursor NOT committed when SyncTransactions returns error ────────────

func TestRG1_CursorNotCommittedOnError(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	mustExec(t, db, `UPDATE plaid_sync_state SET cursor = 'stable' WHERE item_id = 'item_1'`)

	client := &mockPlaidClient{
		syncTransactions: func(_ context.Context, _ string) (*SyncResult, error) {
			return nil, &SyncError{EventClass: "cursor_corrupted", Err: errors.New("api error")}
		},
	}
	s := newScheduler(db, client, nil)
	s.tick()

	var cursor string
	db.QueryRow(`SELECT COALESCE(cursor, '') FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&cursor)
	if cursor != "stable" {
		t.Errorf("cursor should remain 'stable' after error, got %q", cursor)
	}
}

// ── RG-2: status stays 'active' for transient errors until 4th retry ─────────

func TestRG2_StatusActiveUntilFourthRetry(t *testing.T) {
	for retry := 0; retry < 3; retry++ {
		t.Run(fmt.Sprintf("retry_%d", retry), func(t *testing.T) {
			db := mustOpenDB(t)
			seedItem(t, db, "item_1")
			if retry > 0 {
				mustExec(t, db, `UPDATE plaid_sync_state SET retries = ? WHERE item_id = 'item_1'`, retry)
			}

			s := newScheduler(db, &mockPlaidClient{}, nil)
			s.handleSyncError(context.Background(), "run_1", "item_1",
				&SyncError{EventClass: "vendor_unavailable", Err: errors.New("down")})

			var status string
			db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_1'`).Scan(&status)
			if status != "active" {
				t.Errorf("retry %d: status got %q, want 'active'", retry, status)
			}
		})
	}
}
