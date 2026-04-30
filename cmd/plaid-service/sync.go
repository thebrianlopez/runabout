package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	plaid "github.com/plaid/plaid-go/v29/plaid"
)

// ── interfaces ────────────────────────────────────────────────────────────────

// PlaidClient wraps the plaid-go SDK for transaction sync and account refresh.
type PlaidClient interface {
	SyncTransactions(ctx context.Context, itemID string) (*SyncResult, error)
	GetAccounts(ctx context.Context, itemID string) ([]Account, error)
	CreateLinkToken(ctx context.Context) (string, error)
	ExchangePublicToken(ctx context.Context, publicToken string) (itemID, accessToken string, err error)
}

// transactionsAPI is the narrow plaid-go interface used by plaidClientImpl.
// Extracted so tests can inject a mock without live Plaid API calls.
// *plaid.PlaidApiService satisfies this automatically.
type transactionsAPI interface {
	syncPage(ctx context.Context, accessToken, cursor string) (plaid.TransactionsSyncResponse, error)
	fetchAccounts(ctx context.Context, accessToken string) (plaid.AccountsGetResponse, error)
	createLinkToken(ctx context.Context) (string, error)
	exchangePublicToken(ctx context.Context, publicToken string) (itemID, accessToken string, err error)
}

// ── types ─────────────────────────────────────────────────────────────────────

// SyncResult is the outcome of one complete cursor-drain cycle for an item.
type SyncResult struct {
	Added    int
	Modified int
	Removed  int
	Cursor   string // cursor after full drain; caller commits via commitCursor
	RunID    string // UUID per sync tick; propagated to all events
}

// Account mirrors Plaid's account metadata (no secrets).
type Account struct {
	AccountID    string
	ItemID       string
	Name         string
	OfficialName string
	Subtype      string
	Mask         string
}

// SyncError wraps a sync error with its mapped event_class.
// Callers use errors.As(err, &syncErr) to extract EventClass for event routing.
type SyncError struct {
	EventClass string // vendor_auth_required | vendor_unavailable | vendor_rate_limited | cursor_corrupted | infra_auth_failed
	PlaidCode  string
	Err        error
}

func (e *SyncError) Error() string {
	return fmt.Sprintf("%s (%s): %v", e.EventClass, e.PlaidCode, e.Err)
}
func (e *SyncError) Unwrap() error { return e.Err }

// ── live implementation ───────────────────────────────────────────────────────

type plaidClientImpl struct {
	api     transactionsAPI
	secrets *TokenStore
	db      *sql.DB
}

// newPlaidClient creates the live client backed by plaid-go.
// httpClient is injected so all Plaid traffic routes through tsnet (F5).
func newPlaidClient(httpClient *http.Client, secrets *TokenStore, db *sql.DB) PlaidClient {
	cfg := plaid.NewConfiguration()
	cfg.AddDefaultHeader("PLAID-CLIENT-ID", envOrDie("PLAID_CLIENT_ID"))
	cfg.AddDefaultHeader("PLAID-SECRET", envOrDie("PLAID_SECRET"))
	cfg.UseEnvironment(plaid.Sandbox)
	cfg.HTTPClient = httpClient
	svc := plaid.NewAPIClient(cfg).PlaidApi
	return &plaidClientImpl{api: &plaidAPIAdapter{svc: svc}, secrets: secrets, db: db}
}

// newPlaidClientFromParts creates a testable client with injected dependencies.
func newPlaidClientFromParts(api transactionsAPI, secrets *TokenStore, db *sql.DB) PlaidClient {
	return &plaidClientImpl{api: api, secrets: secrets, db: db}
}

// plaidAPIAdapter wraps *plaid.PlaidApiService to satisfy transactionsAPI.
type plaidAPIAdapter struct {
	svc *plaid.PlaidApiService
}

func (a *plaidAPIAdapter) syncPage(ctx context.Context, accessToken, cursor string) (plaid.TransactionsSyncResponse, error) {
	req := plaid.NewTransactionsSyncRequest(accessToken)
	count := int32(500)
	req.Count = &count
	if cursor != "" {
		req.SetCursor(cursor)
	}
	resp, _, err := a.svc.TransactionsSyncExecute(
		a.svc.TransactionsSync(ctx).TransactionsSyncRequest(*req),
	)
	return resp, err
}

func (a *plaidAPIAdapter) fetchAccounts(ctx context.Context, accessToken string) (plaid.AccountsGetResponse, error) {
	req := plaid.NewAccountsGetRequest(accessToken)
	resp, _, err := a.svc.AccountsGetExecute(
		a.svc.AccountsGet(ctx).AccountsGetRequest(*req),
	)
	return resp, err
}

func (a *plaidAPIAdapter) createLinkToken(ctx context.Context) (string, error) {
	user := plaid.NewLinkTokenCreateRequestUser("plaid-service-user")
	req := plaid.NewLinkTokenCreateRequest("plaid-service", "en",
		[]plaid.CountryCode{plaid.COUNTRYCODE_US}, *user)
	req.SetProducts([]plaid.Products{plaid.PRODUCTS_TRANSACTIONS})
	resp, _, err := a.svc.LinkTokenCreateExecute(
		a.svc.LinkTokenCreate(ctx).LinkTokenCreateRequest(*req),
	)
	if err != nil {
		return "", err
	}
	return resp.GetLinkToken(), nil
}

func (a *plaidAPIAdapter) exchangePublicToken(ctx context.Context, publicToken string) (string, string, error) {
	req := plaid.NewItemPublicTokenExchangeRequest(publicToken)
	resp, _, err := a.svc.ItemPublicTokenExchangeExecute(
		a.svc.ItemPublicTokenExchange(ctx).ItemPublicTokenExchangeRequest(*req),
	)
	if err != nil {
		return "", "", err
	}
	return resp.GetItemId(), resp.GetAccessToken(), nil
}

// ── SyncTransactions ──────────────────────────────────────────────────────────

const maxSyncPages = 200

// SyncTransactions drains all pages of the TransactionsSync cursor loop.
// Returns SyncResult with the final cursor on success.
// The caller (Scheduler) is responsible for calling commitCursor — never called here.
func (c *plaidClientImpl) SyncTransactions(ctx context.Context, itemID string) (*SyncResult, error) {
	accessToken, err := c.secrets.GetToken(ctx, itemID)
	if err != nil {
		if errors.Is(err, ErrInfraAuth) {
			return nil, &SyncError{EventClass: "infra_auth_failed", Err: err}
		}
		return nil, &SyncError{EventClass: "vendor_auth_required", Err: err}
	}

	var nullCursor sql.NullString
	c.db.QueryRowContext(ctx, `SELECT cursor FROM plaid_sync_state WHERE item_id = ?`, itemID).Scan(&nullCursor)
	cur := ""
	if nullCursor.Valid {
		cur = nullCursor.String
	}

	result := &SyncResult{RunID: uuid.New().String()}

	for page := 0; page < maxSyncPages; page++ {
		resp, err := c.api.syncPage(ctx, accessToken, cur)
		if err != nil {
			return nil, c.mapSyncError(ctx, itemID, err)
		}

		for _, txn := range resp.Added {
			payload, _ := json.Marshal(txn)
			if err := upsertTransaction(c.db, txn.TransactionId, itemID, txn.AccountId, string(payload)); err != nil {
				return nil, &SyncError{EventClass: "cursor_corrupted", Err: err}
			}
			result.Added++
		}
		for _, txn := range resp.Modified {
			payload, _ := json.Marshal(txn)
			if err := upsertTransaction(c.db, txn.TransactionId, itemID, txn.AccountId, string(payload)); err != nil {
				return nil, &SyncError{EventClass: "cursor_corrupted", Err: err}
			}
			result.Modified++
		}
		for _, txn := range resp.Removed {
			if err := markTransactionRemoved(c.db, txn.TransactionId); err != nil {
				return nil, &SyncError{EventClass: "cursor_corrupted", Err: err}
			}
			result.Removed++
		}

		cur = resp.NextCursor
		if !resp.HasMore {
			break
		}

		if page == maxSyncPages-1 {
			return nil, &SyncError{
				EventClass: "cursor_corrupted",
				PlaidCode:  "max_pages_exceeded",
				Err:        fmt.Errorf("exceeded %d sync pages for item %s", maxSyncPages, itemID),
			}
		}
	}

	result.Cursor = cur
	return result, nil
}

// mapSyncError extracts the Plaid error and maps it to a SyncError via classifyPlaidError.
// mapSyncError extracts the Plaid error and maps it to a SyncError via classifyPlaidError.
// F3-owned side effects: persists rate_limit_reset_at on vendor_rate_limited,
// sets login_required status on vendor_auth_required, treats vendor_unknown as cursor_corrupted.
func (c *plaidClientImpl) mapSyncError(ctx context.Context, itemID string, err error) *SyncError {
	plaidErr, extractErr := plaid.ToPlaidError(err)
	if extractErr != nil {
		return &SyncError{EventClass: "cursor_corrupted", Err: err}
	}

	eventClass := classifyPlaidError(plaidErr)

	switch eventClass {
	case "vendor_auth_required":
		c.db.ExecContext(ctx, `UPDATE plaid_items SET status = 'login_required' WHERE item_id = ?`, itemID)
	case "vendor_rate_limited":
		resetAt := time.Now().UTC().Add(60 * time.Second).Format(time.RFC3339)
		setRateLimitReset(c.db, itemID, resetAt)
	case "vendor_unknown":
		eventClass = "cursor_corrupted"
	}

	return &SyncError{EventClass: eventClass, PlaidCode: plaidErr.ErrorCode, Err: err}
}

// ── GetAccounts ───────────────────────────────────────────────────────────────

func (c *plaidClientImpl) GetAccounts(ctx context.Context, itemID string) ([]Account, error) {
	accessToken, err := c.secrets.GetToken(ctx, itemID)
	if err != nil {
		return nil, fmt.Errorf("get token for accounts: %w", err)
	}
	resp, err := c.api.fetchAccounts(ctx, accessToken)
	if err != nil {
		return nil, fmt.Errorf("accounts get: %w", err)
	}
	accounts := make([]Account, 0, len(resp.Accounts))
	for _, a := range resp.Accounts {
		var subtype, mask, officialName string
		if a.Subtype.Get() != nil {
			subtype = string(*a.Subtype.Get())
		}
		if a.Mask.Get() != nil {
			mask = *a.Mask.Get()
		}
		if a.OfficialName.Get() != nil {
			officialName = *a.OfficialName.Get()
		}
		accounts = append(accounts, Account{
			AccountID:    a.AccountId,
			ItemID:       itemID,
			Name:         a.Name,
			OfficialName: officialName,
			Subtype:      subtype,
			Mask:         mask,
		})
	}
	return accounts, nil
}

// ── Link flow (F8) ───────────────────────────────────────────────────────────

func (c *plaidClientImpl) CreateLinkToken(ctx context.Context) (string, error) {
	return c.api.createLinkToken(ctx)
}

func (c *plaidClientImpl) ExchangePublicToken(ctx context.Context, publicToken string) (string, string, error) {
	return c.api.exchangePublicToken(ctx, publicToken)
}

// ── DB helpers ────────────────────────────────────────────────────────────────

// upsertTransaction inserts or ignores a raw transaction (idempotent via UNIQUE constraint).
func upsertTransaction(db *sql.DB, plaidTxnID, itemID, accountID, jsonPayload string) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO plaid_transactions_raw
		  (plaid_txn_id, item_id, account_id, json_payload, ingested_at)
		VALUES (?, ?, ?, ?, ?)`,
		plaidTxnID, itemID, accountID, jsonPayload, nowUTC(),
	)
	return err
}

// commitCursor persists the cursor only after a full page drain completes.
func commitCursor(db *sql.DB, itemID, cursor string) error {
	_, err := db.Exec(`
		UPDATE plaid_sync_state SET cursor = ?, last_sync_at = ? WHERE item_id = ?`,
		cursor, nowUTC(), itemID,
	)
	return err
}

// ── link flow CLI helpers ─────────────────────────────────────────────────────

func runLinkStart(ctx context.Context, db *sql.DB, client PlaidClient) error {
	token, err := client.CreateLinkToken(ctx)
	if err != nil {
		return fmt.Errorf("link token creation failed: %w", err)
	}
	fmt.Printf("Visit https://cdn.plaid.com/link/v2/stable/link.html?token=%s\n", token)
	fmt.Println("You have 15 minutes to complete the link flow.")

	expires := time.Now().Add(15 * time.Minute).Format(time.RFC3339)
	pendingID := "pending_link_" + expires

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO plaid_items (item_id, institution_id, created_at, status) VALUES (?, '', ?, 'pending_link')`,
		pendingID, nowUTC()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO plaid_sync_state (item_id, retries) VALUES (?, 0)`, pendingID); err != nil {
		return err
	}
	return tx.Commit()
}

func runLinkComplete(ctx context.Context, db *sql.DB, client PlaidClient, secrets *TokenStore, publicToken string) error {
	itemID, accessToken, err := client.ExchangePublicToken(ctx, publicToken)
	if err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}

	if err := secrets.StoreToken(ctx, itemID, accessToken); err != nil {
		return fmt.Errorf("store token: %w", err)
	}

	accounts, err := client.GetAccounts(ctx, itemID)
	if err != nil {
		return fmt.Errorf("get accounts: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO plaid_items (item_id, institution_id, created_at, status)
		VALUES (?, '', ?, 'active')`, itemID, nowUTC()); err != nil {
		return err
	}
	for _, a := range accounts {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO plaid_accounts
			  (account_id, item_id, name, official_name, subtype, mask)
			VALUES (?, ?, ?, ?, ?, ?)`,
			a.AccountID, itemID, a.Name, a.OfficialName, a.Subtype, a.Mask); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO plaid_sync_state (item_id, retries) VALUES (?, 0)`, itemID); err != nil {
		return err
	}

	return tx.Commit()
}

func envOrDie(key string) string {
	v := getenv(key)
	if v == "" {
		panic("required env var " + key + " is not set")
	}
	return v
}
