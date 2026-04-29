package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	plaid "github.com/plaid/plaid-go/v29/plaid"
)

// linkSecrets creates a mock TokenStore that accepts StoreToken and returns the
// stored token on GetToken, avoiding a real ASM round-trip in link flow tests.
func linkSecrets(itemID, accessToken string) *TokenStore {
	payload := `{"access_token":"` + accessToken + `","item_id":"` + itemID + `","created_at":"2026-04-28T00:00:00Z"}`
	return newTokenStoreFromClient(&mockSecretsClient{
		putSecretValue: func(_ context.Context, _ *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
			return &secretsmanager.PutSecretValueOutput{}, nil
		},
		getSecretValue: func(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			return &secretsmanager.GetSecretValueOutput{SecretString: &payload}, nil
		},
	})
}

// ── CT-1: link-start prints Link URL containing the token ────────────────────

func TestLinkCT1_PrintsLinkURL(t *testing.T) {
	db := mustOpenDB(t)
	api := &mockTransactionsAPI{
		createLinkTokenFn: func(_ context.Context) (string, error) {
			return "link-sandbox-token-abc", nil
		},
	}
	client := newPlaidClientFromParts(api, mustTokenStore(t, "tok"), db)

	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w

	_ = runLinkStart(context.Background(), db, client)

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	out := buf.String()

	if !strings.Contains(out, "cdn.plaid.com/link") {
		t.Errorf("stdout missing Link URL: %q", out)
	}
	if !strings.Contains(out, "link-sandbox-token-abc") {
		t.Errorf("stdout missing token: %q", out)
	}
}

// ── CT-2: link-start prints 15-minute warning ────────────────────────────────

func TestLinkCT2_Prints15MinWarning(t *testing.T) {
	db := mustOpenDB(t)
	api := &mockTransactionsAPI{}
	client := newPlaidClientFromParts(api, mustTokenStore(t, "tok"), db)

	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w

	_ = runLinkStart(context.Background(), db, client)

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	if !strings.Contains(buf.String(), "15 minutes") {
		t.Errorf("stdout missing 15-minute warning: %q", buf.String())
	}
}

// ── CT-3: link-start writes pending_link row to plaid_sync_state ─────────────

func TestLinkCT3_WritesPendingLinkRow(t *testing.T) {
	db := mustOpenDB(t)
	api := &mockTransactionsAPI{}
	client := newPlaidClientFromParts(api, mustTokenStore(t, "tok"), db)

	// Suppress stdout
	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	_ = runLinkStart(context.Background(), db, client)
	os.Stdout = old

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM plaid_sync_state WHERE item_id LIKE 'pending_link_%'`).Scan(&count)
	if count != 1 {
		t.Errorf("pending_link row count: got %d, want 1", count)
	}
}

// ── CT-4: link-complete stores token in ASM ──────────────────────────────────

func TestLinkCT4_StoresTokenInASM(t *testing.T) {
	db := mustOpenDB(t)

	const (
		wantItem  = "item_abc"
		wantToken = "access-tok-xyz"
	)
	getPayload := `{"access_token":"` + wantToken + `","item_id":"` + wantItem + `","created_at":"2026-04-28T00:00:00Z"}`

	var storedName, storedPayload string
	secrets := newTokenStoreFromClient(&mockSecretsClient{
		putSecretValue: func(_ context.Context, in *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
			storedName = *in.SecretId
			storedPayload = *in.SecretString
			return &secretsmanager.PutSecretValueOutput{}, nil
		},
		getSecretValue: func(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			return &secretsmanager.GetSecretValueOutput{SecretString: &getPayload}, nil
		},
	})

	api := &mockTransactionsAPI{
		exchangePublicTokenFn: func(_ context.Context, _ string) (string, string, error) {
			return wantItem, wantToken, nil
		},
	}
	client := newPlaidClientFromParts(api, secrets, db)

	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	_ = runLinkComplete(context.Background(), db, client, secrets, "public-tok")
	os.Stdout = old

	if storedName != "plaid/items/"+wantItem+"/access_token" {
		t.Errorf("secret name: got %q, want plaid/items/%s/access_token", storedName, wantItem)
	}
	if !strings.Contains(storedPayload, wantToken) {
		t.Errorf("secret payload missing access token: %q", storedPayload)
	}
}

// ── CT-5: link-complete inserts item into plaid_items with status=active ─────

func TestLinkCT5_InsertsItemActive(t *testing.T) {
	db := mustOpenDB(t)
	secrets := linkSecrets("item_abc", "access-tok")
	api := &mockTransactionsAPI{
		exchangePublicTokenFn: func(_ context.Context, _ string) (string, string, error) {
			return "item_abc", "access-tok", nil
		},
		fetchAccountsFn: func(_ context.Context, _ string) (plaid.AccountsGetResponse, error) {
			return plaid.AccountsGetResponse{}, nil
		},
	}
	client := newPlaidClientFromParts(api, secrets, db)

	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	_ = runLinkComplete(context.Background(), db, client, secrets, "public-tok")
	os.Stdout = old

	var status string
	db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_abc'`).Scan(&status)
	if status != "active" {
		t.Errorf("plaid_items.status: got %q, want active", status)
	}
}

// ── CT-6: link-complete populates plaid_accounts ─────────────────────────────

func TestLinkCT6_PopulatesAccounts(t *testing.T) {
	db := mustOpenDB(t)
	secrets := linkSecrets("item_abc", "access-tok")

	subtype := plaid.AccountSubtype("checking")
	acctType := plaid.ACCOUNTTYPE_DEPOSITORY
	api := &mockTransactionsAPI{
		exchangePublicTokenFn: func(_ context.Context, _ string) (string, string, error) {
			return "item_abc", "access-tok", nil
		},
		fetchAccountsFn: func(_ context.Context, _ string) (plaid.AccountsGetResponse, error) {
			return plaid.AccountsGetResponse{
				Accounts: []plaid.AccountBase{
					{AccountId: "acct_1", Name: "Checking", Type: acctType, Subtype: *plaid.NewNullableAccountSubtype(&subtype)},
				},
			}, nil
		},
	}
	client := newPlaidClientFromParts(api, secrets, db)

	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	_ = runLinkComplete(context.Background(), db, client, secrets, "public-tok")
	os.Stdout = old

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM plaid_accounts WHERE item_id = 'item_abc'`).Scan(&count)
	if count != 1 {
		t.Errorf("plaid_accounts count: got %d, want 1", count)
	}
}

// ── CT-7: link-complete never prints access token to stdout ──────────────────

func TestLinkCT7_AccessTokenAbsentFromStdout(t *testing.T) {
	db := mustOpenDB(t)
	const accessToken = "access-secret-tok-should-not-appear"
	secrets := linkSecrets("item_abc", accessToken)
	api := &mockTransactionsAPI{
		exchangePublicTokenFn: func(_ context.Context, _ string) (string, string, error) {
			return "item_abc", accessToken, nil
		},
	}
	client := newPlaidClientFromParts(api, secrets, db)

	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w

	_ = runLinkComplete(context.Background(), db, client, secrets, "public-tok")

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	if strings.Contains(buf.String(), accessToken) {
		t.Error("access token must not appear in stdout")
	}
}

// ── BT-1: link-complete returns error on already-exchanged public_token ───────

func TestLinkBT1_AlreadyExchangedReturnsError(t *testing.T) {
	db := mustOpenDB(t)
	secrets := linkSecrets("item_abc", "access-tok")
	api := &mockTransactionsAPI{
		exchangePublicTokenFn: func(_ context.Context, _ string) (string, string, error) {
			return "", "", errors.New("public token already used")
		},
	}
	client := newPlaidClientFromParts(api, secrets, db)

	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	err := runLinkComplete(context.Background(), db, client, secrets, "already-used-tok")
	os.Stdout = old

	if err == nil {
		t.Error("expected error for already-exchanged token, got nil")
	}
}

// ── BT-2: link-complete rolls back item insert if accounts call fails ─────────

func TestLinkBT2_AccountsFailureRollsBack(t *testing.T) {
	db := mustOpenDB(t)
	secrets := linkSecrets("item_abc", "access-tok")
	api := &mockTransactionsAPI{
		exchangePublicTokenFn: func(_ context.Context, _ string) (string, string, error) {
			return "item_abc", "access-tok", nil
		},
		fetchAccountsFn: func(_ context.Context, _ string) (plaid.AccountsGetResponse, error) {
			return plaid.AccountsGetResponse{}, errors.New("accounts api down")
		},
	}
	client := newPlaidClientFromParts(api, secrets, db)

	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	_ = runLinkComplete(context.Background(), db, client, secrets, "public-tok")
	os.Stdout = old

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM plaid_items WHERE item_id = 'item_abc'`).Scan(&count)
	if count != 0 {
		t.Error("plaid_items row should be rolled back when accounts call fails")
	}
}

// ── BT-3: link-complete with 0 accounts succeeds ─────────────────────────────

func TestLinkBT3_ZeroAccountsSucceeds(t *testing.T) {
	db := mustOpenDB(t)
	secrets := linkSecrets("item_abc", "access-tok")
	api := &mockTransactionsAPI{
		exchangePublicTokenFn: func(_ context.Context, _ string) (string, string, error) {
			return "item_abc", "access-tok", nil
		},
		fetchAccountsFn: func(_ context.Context, _ string) (plaid.AccountsGetResponse, error) {
			return plaid.AccountsGetResponse{}, nil
		},
	}
	client := newPlaidClientFromParts(api, secrets, db)

	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	err := runLinkComplete(context.Background(), db, client, secrets, "public-tok")
	os.Stdout = old

	if err != nil {
		t.Errorf("0-account link-complete should succeed, got: %v", err)
	}
	var status string
	db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_abc'`).Scan(&status)
	if status != "active" {
		t.Errorf("item status: got %q, want active", status)
	}
}

// ── RG-1: access token absent from event bus writes ──────────────────────────

func TestLinkRG1_AccessTokenAbsentFromEvents(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	db := mustOpenDB(t)
	const accessToken = "access-rg1-secret-should-not-appear"
	secrets := linkSecrets("item_abc", accessToken)
	api := &mockTransactionsAPI{
		exchangePublicTokenFn: func(_ context.Context, _ string) (string, string, error) {
			return "item_abc", accessToken, nil
		},
	}
	client := newPlaidClientFromParts(api, secrets, db)

	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	_ = runLinkComplete(context.Background(), db, client, secrets, "public-tok")
	os.Stdout = old

	events := readEvents(t, dir)
	for _, e := range events {
		raw, _ := os.ReadFile(dir)
		_ = raw
		// Check serialized event doesn't contain the token
		if v, ok := e["metadata"]; ok {
			if m, ok := v.(map[string]any); ok {
				for _, val := range m {
					if s, ok := val.(string); ok && strings.Contains(s, accessToken) {
						t.Error("access token found in event metadata")
					}
				}
			}
		}
	}
}
