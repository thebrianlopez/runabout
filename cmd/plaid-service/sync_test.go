package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	plaid "github.com/plaid/plaid-go/v29/plaid"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// ── mock ─────────────────────────────────────────────────────────────────────

type mockTransactionsAPI struct {
	syncPageFn            func(ctx context.Context, accessToken, cursor string) (plaid.TransactionsSyncResponse, error)
	fetchAccountsFn       func(ctx context.Context, accessToken string) (plaid.AccountsGetResponse, error)
	createLinkTokenFn     func(ctx context.Context) (string, error)
	exchangePublicTokenFn func(ctx context.Context, publicToken string) (string, string, error)
}

func (m *mockTransactionsAPI) syncPage(ctx context.Context, accessToken, cursor string) (plaid.TransactionsSyncResponse, error) {
	return m.syncPageFn(ctx, accessToken, cursor)
}

func (m *mockTransactionsAPI) fetchAccounts(ctx context.Context, accessToken string) (plaid.AccountsGetResponse, error) {
	if m.fetchAccountsFn != nil {
		return m.fetchAccountsFn(ctx, accessToken)
	}
	return plaid.AccountsGetResponse{}, nil
}

func (m *mockTransactionsAPI) createLinkToken(ctx context.Context) (string, error) {
	if m.createLinkTokenFn != nil {
		return m.createLinkTokenFn(ctx)
	}
	return "link-sandbox-test-token", nil
}

func (m *mockTransactionsAPI) exchangePublicToken(ctx context.Context, publicToken string) (string, string, error) {
	if m.exchangePublicTokenFn != nil {
		return m.exchangePublicTokenFn(ctx, publicToken)
	}
	return "item_exchanged", "access-sandbox-test-token", nil
}

// mustInfraAuthTokenStore returns a TokenStore whose GetToken returns ErrInfraAuth.
// Simulates mid-sync AWS credential expiry (not a missing secret).
func mustInfraAuthTokenStore(t *testing.T) *TokenStore {
	t.Helper()
	return newTokenStoreFromClient(&mockSecretsClient{
		getSecretValue: func(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			return nil, errors.New("ExpiredTokenException: credentials have expired")
		},
	})
}

// mustTokenStore builds a TokenStore that always returns the given access token.
func mustTokenStore(t *testing.T, token string) *TokenStore {
	t.Helper()
	payload := `{"access_token":"` + token + `","item_id":"item_1","created_at":"2026-04-28T00:00:00Z"}`
	return newTokenStoreFromClient(&mockSecretsClient{
		getSecretValue: func(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			return &secretsmanager.GetSecretValueOutput{SecretString: &payload}, nil
		},
	})
}

// plaidAPIErr creates a plaid.GenericOpenAPIError with the given error_code in its body.
func plaidAPIErr(code string) error {
	body, _ := json.Marshal(map[string]string{
		"error_type":      "ITEM_ERROR",
		"error_code":      code,
		"error_message":   code,
		"display_message": "",
	})
	return plaid.MakeGenericOpenAPIError(body, code, nil)
}

// syncTxn returns a minimal plaid.Transaction for use in test responses.
func syncTxn(txnID, accountID string) plaid.Transaction {
	t := plaid.Transaction{}
	t.TransactionId = txnID
	t.AccountId = accountID
	return t
}

// ── CT-1: cursor committed only after full page drain ────────────────────────

func TestCT1_CursorAfterFullDrain(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	seedAccount(t, db, "acct_1", "item_1")

	page := 0
	mock := &mockTransactionsAPI{
		syncPageFn: func(_ context.Context, _, _ string) (plaid.TransactionsSyncResponse, error) {
			page++
			switch page {
			case 1:
				return plaid.TransactionsSyncResponse{
					Added:      []plaid.Transaction{syncTxn("txn_1", "acct_1"), syncTxn("txn_2", "acct_1")},
					NextCursor: "cur_1",
					HasMore:    true,
				}, nil
			default:
				return plaid.TransactionsSyncResponse{
					Added:      []plaid.Transaction{syncTxn("txn_3", "acct_1")},
					NextCursor: "cur_final",
					HasMore:    false,
				}, nil
			}
		},
	}

	client := newPlaidClientFromParts(mock, mustTokenStore(t, "tok"), db)
	result, err := client.SyncTransactions(context.Background(), "item_1")
	if err != nil {
		t.Fatalf("SyncTransactions: %v", err)
	}

	if result.Cursor != "cur_final" {
		t.Errorf("cursor: got %q, want %q", result.Cursor, "cur_final")
	}
	if result.Added != 3 {
		t.Errorf("added: got %d, want 3", result.Added)
	}

	// Cursor in DB must NOT be updated by SyncTransactions itself — caller commits it.
	var dbCursor string
	db.QueryRow(`SELECT COALESCE(cursor, '') FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&dbCursor)
	if dbCursor == "cur_final" {
		t.Error("SyncTransactions must not commit cursor — that is the caller's responsibility")
	}
}

// ── CT-2: error mid-pagination leaves cursor unchanged ───────────────────────

func TestCT2_ErrorMidPaginationCursorUnchanged(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	seedAccount(t, db, "acct_1", "item_1")

	// Seed an existing cursor in DB.
	mustExec(t, db, `UPDATE plaid_sync_state SET cursor = 'initial_cur' WHERE item_id = 'item_1'`)

	page := 0
	mock := &mockTransactionsAPI{
		syncPageFn: func(_ context.Context, _, _ string) (plaid.TransactionsSyncResponse, error) {
			page++
			if page == 1 {
				return plaid.TransactionsSyncResponse{
					Added:      []plaid.Transaction{syncTxn("txn_1", "acct_1")},
					NextCursor: "cur_1",
					HasMore:    true,
				}, nil
			}
			return plaid.TransactionsSyncResponse{}, errors.New("network failure")
		},
	}

	client := newPlaidClientFromParts(mock, mustTokenStore(t, "tok"), db)
	_, err := client.SyncTransactions(context.Background(), "item_1")
	if err == nil {
		t.Fatal("expected error from SyncTransactions, got nil")
	}

	var dbCursor string
	db.QueryRow(`SELECT COALESCE(cursor, '') FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&dbCursor)
	if dbCursor != "initial_cur" {
		t.Errorf("cursor should remain 'initial_cur' after error, got %q", dbCursor)
	}
}

// ── CT-3: duplicate plaid_txn_id in sync silently succeeds ───────────────────

func TestCT3_DuplicateTxnInSyncSucceeds(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	seedAccount(t, db, "acct_1", "item_1")

	calls := 0
	mock := &mockTransactionsAPI{
		syncPageFn: func(_ context.Context, _, _ string) (plaid.TransactionsSyncResponse, error) {
			calls++
			if calls == 1 {
				return plaid.TransactionsSyncResponse{
					Added:      []plaid.Transaction{syncTxn("txn_dup", "acct_1")},
					NextCursor: "cur_1",
					HasMore:    false,
				}, nil
			}
			// Second sync returns same txn (already in DB).
			return plaid.TransactionsSyncResponse{
				Modified:   []plaid.Transaction{syncTxn("txn_dup", "acct_1")},
				NextCursor: "cur_2",
				HasMore:    false,
			}, nil
		},
	}

	client := newPlaidClientFromParts(mock, mustTokenStore(t, "tok"), db)
	ctx := context.Background()

	if _, err := client.SyncTransactions(ctx, "item_1"); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	// Seed cursor so second call advances.
	mustExec(t, db, `UPDATE plaid_sync_state SET cursor = 'cur_1' WHERE item_id = 'item_1'`)
	if _, err := client.SyncTransactions(ctx, "item_1"); err != nil {
		t.Errorf("second sync (duplicate txn) should not error: %v", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM plaid_transactions_raw WHERE plaid_txn_id = 'txn_dup'`).Scan(&count)
	if count != 1 {
		t.Errorf("row count after duplicate: got %d, want 1", count)
	}
}

// ── CT-4: SyncResult.RunID is a non-empty string ─────────────────────────────

func TestCT4_RunIDNonEmpty(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	mock := &mockTransactionsAPI{
		syncPageFn: func(_ context.Context, _, _ string) (plaid.TransactionsSyncResponse, error) {
			return plaid.TransactionsSyncResponse{HasMore: false, NextCursor: "cur"}, nil
		},
	}

	client := newPlaidClientFromParts(mock, mustTokenStore(t, "tok"), db)
	result, err := client.SyncTransactions(context.Background(), "item_1")
	if err != nil {
		t.Fatalf("SyncTransactions: %v", err)
	}
	if result.RunID == "" {
		t.Error("RunID should be non-empty")
	}
	// Distinct RunIDs per call.
	result2, _ := client.SyncTransactions(context.Background(), "item_1")
	if result.RunID == result2.RunID {
		t.Error("consecutive calls should produce distinct RunIDs")
	}
}

// ── CT-5: ITEM_LOGIN_REQUIRED → vendor_auth_required + item status=login_required

func TestCT5_ItemLoginRequired(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	mock := &mockTransactionsAPI{
		syncPageFn: func(_ context.Context, _, _ string) (plaid.TransactionsSyncResponse, error) {
			return plaid.TransactionsSyncResponse{}, plaidAPIErr("ITEM_LOGIN_REQUIRED")
		},
	}

	client := newPlaidClientFromParts(mock, mustTokenStore(t, "tok"), db)
	_, err := client.SyncTransactions(context.Background(), "item_1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var syncErr *SyncError
	if !errors.As(err, &syncErr) {
		t.Fatalf("error should be *SyncError, got %T: %v", err, err)
	}
	if syncErr.EventClass != "vendor_auth_required" {
		t.Errorf("EventClass: got %q, want %q", syncErr.EventClass, "vendor_auth_required")
	}

	var status string
	db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_1'`).Scan(&status)
	if status != "login_required" {
		t.Errorf("plaid_items.status: got %q, want %q", status, "login_required")
	}
}

// ── CT-6: RATE_LIMIT_EXCEEDED → vendor_rate_limited + rate_limit_reset_at set ─

func TestCT6_RateLimitExceeded(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	mock := &mockTransactionsAPI{
		syncPageFn: func(_ context.Context, _, _ string) (plaid.TransactionsSyncResponse, error) {
			return plaid.TransactionsSyncResponse{}, plaidAPIErr("RATE_LIMIT_EXCEEDED")
		},
	}

	client := newPlaidClientFromParts(mock, mustTokenStore(t, "tok"), db)
	_, err := client.SyncTransactions(context.Background(), "item_1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var syncErr *SyncError
	if !errors.As(err, &syncErr) {
		t.Fatalf("error should be *SyncError, got %T", err)
	}
	if syncErr.EventClass != "vendor_rate_limited" {
		t.Errorf("EventClass: got %q, want %q", syncErr.EventClass, "vendor_rate_limited")
	}

	var resetAt string
	db.QueryRow(`SELECT COALESCE(rate_limit_reset_at, '') FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&resetAt)
	if resetAt == "" {
		t.Error("rate_limit_reset_at should be populated after RATE_LIMIT_EXCEEDED")
	}
}

// ── CT-7: INSTITUTION_DOWN → vendor_unavailable + NO item status change ───────

func TestCT7_InstitutionDown(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	mock := &mockTransactionsAPI{
		syncPageFn: func(_ context.Context, _, _ string) (plaid.TransactionsSyncResponse, error) {
			return plaid.TransactionsSyncResponse{}, plaidAPIErr("INSTITUTION_DOWN")
		},
	}

	client := newPlaidClientFromParts(mock, mustTokenStore(t, "tok"), db)
	_, err := client.SyncTransactions(context.Background(), "item_1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var syncErr *SyncError
	if !errors.As(err, &syncErr) {
		t.Fatalf("error should be *SyncError, got %T", err)
	}
	if syncErr.EventClass != "vendor_unavailable" {
		t.Errorf("EventClass: got %q, want %q", syncErr.EventClass, "vendor_unavailable")
	}

	var status string
	db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_1'`).Scan(&status)
	if status != "active" {
		t.Errorf("plaid_items.status should remain 'active' on vendor_unavailable, got %q", status)
	}
}

// ── CT-8: unknown error → cursor_corrupted + cursor unchanged ─────────────────

func TestCT8_UnknownErrorCursorCorrupted(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	mustExec(t, db, `UPDATE plaid_sync_state SET cursor = 'stable_cur' WHERE item_id = 'item_1'`)

	mock := &mockTransactionsAPI{
		syncPageFn: func(_ context.Context, _, _ string) (plaid.TransactionsSyncResponse, error) {
			return plaid.TransactionsSyncResponse{}, plaidAPIErr("UNKNOWN_PLAID_CODE_XYZ")
		},
	}

	client := newPlaidClientFromParts(mock, mustTokenStore(t, "tok"), db)
	_, err := client.SyncTransactions(context.Background(), "item_1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var syncErr *SyncError
	if !errors.As(err, &syncErr) {
		t.Fatalf("error should be *SyncError, got %T", err)
	}
	if syncErr.EventClass != "cursor_corrupted" {
		t.Errorf("EventClass: got %q, want %q", syncErr.EventClass, "cursor_corrupted")
	}

	var dbCursor string
	db.QueryRow(`SELECT COALESCE(cursor, '') FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&dbCursor)
	if dbCursor != "stable_cur" {
		t.Errorf("cursor should remain 'stable_cur', got %q", dbCursor)
	}
}

// ── RG-3: item stays active when GetToken returns ErrInfraAuth ───────────────
// Source: POMO asm-plaid-auth-misclassification

func TestRG3_InfraAuthItemStaysActive(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	client := newPlaidClientFromParts(&mockTransactionsAPI{
		syncPageFn: func(_ context.Context, _, _ string) (plaid.TransactionsSyncResponse, error) {
			return plaid.TransactionsSyncResponse{}, nil
		},
	}, mustInfraAuthTokenStore(t), db)

	client.SyncTransactions(context.Background(), "item_1") //nolint:errcheck

	var status string
	db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_1'`).Scan(&status)
	if status != "active" {
		t.Errorf("plaid_items.status should remain 'active' on infra_auth_failed, got %q", status)
	}
}

// ── BT-2: multi-page accumulates Added/Modified/Removed counts ────────────────

func TestBT2_MultiPageCounts(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	seedAccount(t, db, "acct_1", "item_1")
	// Pre-insert txn_r1 so markTransactionRemoved has a row to update.
	mustExec(t, db,
		`INSERT INTO plaid_transactions_raw (plaid_txn_id, item_id, account_id, json_payload, ingested_at) VALUES ('txn_r1', 'item_1', 'acct_1', '{}', ?)`,
		nowUTC())

	page := 0
	mock := &mockTransactionsAPI{
		syncPageFn: func(_ context.Context, _, _ string) (plaid.TransactionsSyncResponse, error) {
			page++
			switch page {
			case 1:
				removed := plaid.RemovedTransaction{}
				removed.TransactionId = "txn_r1"
				return plaid.TransactionsSyncResponse{
					Added:      []plaid.Transaction{syncTxn("txn_a1", "acct_1"), syncTxn("txn_a2", "acct_1"), syncTxn("txn_a3", "acct_1")},
					Removed:    []plaid.RemovedTransaction{removed},
					NextCursor: "cur_1",
					HasMore:    true,
				}, nil
			default:
				return plaid.TransactionsSyncResponse{
					Added:      []plaid.Transaction{syncTxn("txn_a4", "acct_1"), syncTxn("txn_a5", "acct_1")},
					NextCursor: "cur_2",
					HasMore:    false,
				}, nil
			}
		},
	}

	client := newPlaidClientFromParts(mock, mustTokenStore(t, "tok"), db)
	result, err := client.SyncTransactions(context.Background(), "item_1")
	if err != nil {
		t.Fatalf("SyncTransactions: %v", err)
	}
	if result.Added != 5 {
		t.Errorf("Added: got %d, want 5", result.Added)
	}
	if result.Removed != 1 {
		t.Errorf("Removed: got %d, want 1", result.Removed)
	}
}

// ── BT-4: markTransactionRemoved idempotent ───────────────────────────────────

func TestBT4_MarkRemovedIdempotent(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	seedAccount(t, db, "acct_1", "item_1")
	mustExec(t, db,
		`INSERT INTO plaid_transactions_raw (plaid_txn_id, item_id, account_id, json_payload, ingested_at) VALUES ('txn_x', 'item_1', 'acct_1', '{}', ?)`,
		nowUTC())

	if err := markTransactionRemoved(db, "txn_x"); err != nil {
		t.Fatalf("first markTransactionRemoved: %v", err)
	}
	if err := markTransactionRemoved(db, "txn_x"); err != nil {
		t.Errorf("second markTransactionRemoved should be idempotent: %v", err)
	}

	var isRemoved int
	db.QueryRow(`SELECT is_removed FROM plaid_transactions_raw WHERE plaid_txn_id = 'txn_x'`).Scan(&isRemoved)
	if isRemoved != 1 {
		t.Errorf("is_removed: got %d, want 1", isRemoved)
	}
}

// ── RG-1: cursor never committed on error at any pagination position ──────────

func TestRG1_CursorNeverCommittedOnError(t *testing.T) {
	for _, errorPage := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("error_on_page_%d", errorPage), func(t *testing.T) {
			db := mustOpenDB(t)
			seedItem(t, db, "item_1")
			seedAccount(t, db, "acct_1", "item_1")
			mustExec(t, db, `UPDATE plaid_sync_state SET cursor = 'pre_test_cur' WHERE item_id = 'item_1'`)

			page := 0
			mock := &mockTransactionsAPI{
				syncPageFn: func(_ context.Context, _, _ string) (plaid.TransactionsSyncResponse, error) {
					page++
					if page == errorPage {
						return plaid.TransactionsSyncResponse{}, errors.New("injected error")
					}
					return plaid.TransactionsSyncResponse{
						Added:      []plaid.Transaction{syncTxn(fmt.Sprintf("txn_p%d", page), "acct_1")},
						NextCursor: fmt.Sprintf("cur_%d", page),
						HasMore:    true,
					}, nil
				},
			}

			client := newPlaidClientFromParts(mock, mustTokenStore(t, "tok"), db)
			_, err := client.SyncTransactions(context.Background(), "item_1")
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			var dbCursor string
			db.QueryRow(`SELECT COALESCE(cursor, '') FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&dbCursor)
			if dbCursor != "pre_test_cur" {
				t.Errorf("cursor should remain 'pre_test_cur' after page-%d error, got %q", errorPage, dbCursor)
			}
		})
	}
}

// ── CT-9: infra_auth_failed on ErrInfraAuth from GetToken ────────────────────

func TestCT9_InfraAuthFailedEventClass(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	client := newPlaidClientFromParts(&mockTransactionsAPI{
		syncPageFn: func(_ context.Context, _, _ string) (plaid.TransactionsSyncResponse, error) {
			return plaid.TransactionsSyncResponse{}, nil
		},
	}, mustInfraAuthTokenStore(t), db)

	_, err := client.SyncTransactions(context.Background(), "item_1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var syncErr *SyncError
	if !errors.As(err, &syncErr) {
		t.Fatalf("error should be *SyncError, got %T: %v", err, err)
	}
	if syncErr.EventClass != "infra_auth_failed" {
		t.Errorf("EventClass: got %q, want %q", syncErr.EventClass, "infra_auth_failed")
	}

	var status string
	db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_1'`).Scan(&status)
	if status != "active" {
		t.Errorf("plaid_items.status: got %q, want 'active'", status)
	}
}

// ── RG-2: transient errors do NOT mutate plaid_items.status ──────────────────

func TestRG2_TransientErrorsNoStatusChange(t *testing.T) {
	for _, code := range []string{"INSTITUTION_DOWN", "UNKNOWN_CODE_ZZZ"} {
		t.Run(code, func(t *testing.T) {
			db := mustOpenDB(t)
			seedItem(t, db, "item_1")

			mock := &mockTransactionsAPI{
				syncPageFn: func(_ context.Context, _, _ string) (plaid.TransactionsSyncResponse, error) {
					return plaid.TransactionsSyncResponse{}, plaidAPIErr(code)
				},
			}

			client := newPlaidClientFromParts(mock, mustTokenStore(t, "tok"), db)
			client.SyncTransactions(context.Background(), "item_1") //nolint:errcheck

			var status string
			db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_1'`).Scan(&status)
			if status != "active" {
				t.Errorf("[%s] status should remain 'active', got %q", code, status)
			}
		})
	}
}
