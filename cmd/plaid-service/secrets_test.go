package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

// ── mock ─────────────────────────────────────────────────────────────────────

// mockSecretsClient is a hand-rolled implementation of secretsClient.
// Each test constructs its own instance — no shared state.
type mockSecretsClient struct {
	getSecretValue func(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
	putSecretValue func(context.Context, *secretsmanager.PutSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
	createSecret   func(context.Context, *secretsmanager.CreateSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
	deleteSecret   func(context.Context, *secretsmanager.DeleteSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error)
}

func (m *mockSecretsClient) GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	return m.getSecretValue(ctx, in, opts...)
}

func (m *mockSecretsClient) PutSecretValue(ctx context.Context, in *secretsmanager.PutSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	return m.putSecretValue(ctx, in, opts...)
}

func (m *mockSecretsClient) CreateSecret(ctx context.Context, in *secretsmanager.CreateSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	return m.createSecret(ctx, in, opts...)
}

func (m *mockSecretsClient) DeleteSecret(ctx context.Context, in *secretsmanager.DeleteSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error) {
	return m.deleteSecret(ctx, in, opts...)
}

// ── CT-1: StoreToken → GetToken round-trip ───────────────────────────────────

func TestCT1_StoreGetRoundTrip(t *testing.T) {
	var stored string
	mock := &mockSecretsClient{
		putSecretValue: func(_ context.Context, in *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
			stored = *in.SecretString
			return &secretsmanager.PutSecretValueOutput{}, nil
		},
		getSecretValue: func(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			return &secretsmanager.GetSecretValueOutput{SecretString: &stored}, nil
		},
	}

	store := newTokenStoreFromClient(mock)
	ctx := context.Background()

	const token = "access-token-abc123"
	if err := store.StoreToken(ctx, "item_1", token); err != nil {
		t.Fatalf("StoreToken: %v", err)
	}

	got, err := store.GetToken(ctx, "item_1")
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if got != token {
		t.Errorf("round-trip: got %q, want %q", got, token)
	}
}

// ── CT-2: StoreToken idempotent ───────────────────────────────────────────────

func TestCT2_StoreTokenIdempotent(t *testing.T) {
	calls := 0
	mock := &mockSecretsClient{
		putSecretValue: func(_ context.Context, _ *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
			calls++
			return &secretsmanager.PutSecretValueOutput{}, nil
		},
	}

	store := newTokenStoreFromClient(mock)
	ctx := context.Background()

	if err := store.StoreToken(ctx, "item_1", "token_v1"); err != nil {
		t.Fatalf("first StoreToken: %v", err)
	}
	if err := store.StoreToken(ctx, "item_1", "token_v2"); err != nil {
		t.Errorf("second StoreToken should not error: %v", err)
	}
	if calls != 2 {
		t.Errorf("PutSecretValue call count: got %d, want 2", calls)
	}
}

// ── CT-3: GetToken returns secret_not_found for unknown item ──────────────────

func TestCT3_GetTokenSecretNotFound(t *testing.T) {
	mock := &mockSecretsClient{
		getSecretValue: func(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			return nil, &types.ResourceNotFoundException{Message: strPtr("secret not found")}
		},
	}

	store := newTokenStoreFromClient(mock)
	_, err := store.GetToken(context.Background(), "item_unknown")
	if err == nil {
		t.Fatal("expected error for unknown item, got nil")
	}
	if !strings.Contains(err.Error(), "secret not found") {
		t.Errorf("error should contain 'secret not found': %v", err)
	}
}

// ── CT-4: DeleteToken uses ForceDeleteWithoutRecovery: true ───────────────────

func TestCT4_DeleteTokenForceFlag(t *testing.T) {
	var captured *secretsmanager.DeleteSecretInput
	mock := &mockSecretsClient{
		deleteSecret: func(_ context.Context, in *secretsmanager.DeleteSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error) {
			captured = in
			return &secretsmanager.DeleteSecretOutput{}, nil
		},
	}

	store := newTokenStoreFromClient(mock)
	if err := store.DeleteToken(context.Background(), "item_1"); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if captured == nil {
		t.Fatal("DeleteSecret was not called")
	}
	if captured.ForceDeleteWithoutRecovery == nil || !*captured.ForceDeleteWithoutRecovery {
		t.Error("ForceDeleteWithoutRecovery should be true")
	}
}

// ── CT-5: secretName returns canonical path ───────────────────────────────────

func TestCT5_SecretNameCanonical(t *testing.T) {
	store := newTokenStoreFromClient(nil)
	cases := []struct{ id, want string }{
		{"abc123", "plaid/items/abc123/access_token"},
		{"item-x", "plaid/items/item-x/access_token"},
	}
	for _, c := range cases {
		if got := store.secretName(c.id); got != c.want {
			t.Errorf("secretName(%q): got %q, want %q", c.id, got, c.want)
		}
	}
}

// ── CT-6: newTokenStore returns aws_auth_failed on bad credentials ────────────
//
// newTokenStore calls config.LoadDefaultConfig directly and is not currently
// testable without config injection. This test is deferred to integration
// coverage (aws_auth_failed surfaces in the scheduler startup path).

func TestCT6_AWSAuthFailed(t *testing.T) {
	t.Skip("requires config loader injection — deferred to integration tests")
}

// ── CT-7: StoreToken non-404 error does NOT fall back to CreateSecret ─────────

func TestCT7_StoreTokenNoFallbackOnPermissionError(t *testing.T) {
	createCalled := false
	mock := &mockSecretsClient{
		putSecretValue: func(_ context.Context, _ *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
			return nil, errors.New("AccessDeniedException: access denied")
		},
		createSecret: func(_ context.Context, _ *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
			createCalled = true
			return &secretsmanager.CreateSecretOutput{}, nil
		},
	}

	store := newTokenStoreFromClient(mock)
	err := store.StoreToken(context.Background(), "item_1", "token")
	if err == nil {
		t.Fatal("expected error on permission denied, got nil")
	}
	if createCalled {
		t.Error("CreateSecret must NOT be called on non-ResourceNotFoundException error")
	}
}

// ── BT-1: AWS_PROFILE env var ─────────────────────────────────────────────────
//
// AWS_PROFILE is read by config.LoadDefaultConfig from the environment —
// no injection point in the current impl. Covered by manual local dev testing.

func TestBT1_AWSProfileEnvVar(t *testing.T) {
	t.Skip("config.LoadDefaultConfig reads AWS_PROFILE from env — no injection point")
}

// ── BT-2: JSON payload includes access_token, item_id, created_at ────────────

func TestBT2_StoreTokenJSONPayload(t *testing.T) {
	var capturedPayload string
	mock := &mockSecretsClient{
		putSecretValue: func(_ context.Context, in *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
			capturedPayload = *in.SecretString
			return &secretsmanager.PutSecretValueOutput{}, nil
		},
	}

	store := newTokenStoreFromClient(mock)
	if err := store.StoreToken(context.Background(), "item_42", "tok_abc"); err != nil {
		t.Fatalf("StoreToken: %v", err)
	}

	var ts tokenSecret
	if err := json.Unmarshal([]byte(capturedPayload), &ts); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if ts.AccessToken != "tok_abc" {
		t.Errorf("access_token: got %q, want %q", ts.AccessToken, "tok_abc")
	}
	if ts.ItemID != "item_42" {
		t.Errorf("item_id: got %q, want %q", ts.ItemID, "item_42")
	}
	if ts.CreatedAt == "" {
		t.Error("created_at should be populated")
	}
}

// ── BT-3: StoreToken falls back to CreateSecret on ResourceNotFoundException ──

func TestBT3_StoreTokenFallbackToCreate(t *testing.T) {
	var createPayload string
	mock := &mockSecretsClient{
		putSecretValue: func(_ context.Context, _ *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
			return nil, &types.ResourceNotFoundException{Message: strPtr("does not exist")}
		},
		createSecret: func(_ context.Context, in *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
			createPayload = *in.SecretString
			return &secretsmanager.CreateSecretOutput{}, nil
		},
	}

	store := newTokenStoreFromClient(mock)
	if err := store.StoreToken(context.Background(), "item_new", "first_token"); err != nil {
		t.Fatalf("StoreToken: %v", err)
	}
	if createPayload == "" {
		t.Fatal("CreateSecret was not called")
	}

	var ts tokenSecret
	if err := json.Unmarshal([]byte(createPayload), &ts); err != nil {
		t.Fatalf("unmarshal create payload: %v", err)
	}
	if ts.AccessToken != "first_token" {
		t.Errorf("access_token in create payload: got %q, want %q", ts.AccessToken, "first_token")
	}
}

// ── RG-1: sentinel token must not appear in any slog output ──────────────────

// testLogHandler captures slog records so RG-1 can scan all log output.
type testLogHandler struct {
	records []slog.Record
}

func (h *testLogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *testLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *testLogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *testLogHandler) WithGroup(_ string) slog.Handler      { return h }

func TestRG1_TokenNotInLogs(t *testing.T) {
	const sentinel = "SENTINEL_TOKEN_XYZ_12345_DO_NOT_LOG"

	handler := &testLogHandler{}
	orig := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(orig)

	secretJSON := `{"access_token":"` + sentinel + `","item_id":"item_1","created_at":"2026-04-28T00:00:00Z"}`
	mock := &mockSecretsClient{
		putSecretValue: func(_ context.Context, _ *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
			return &secretsmanager.PutSecretValueOutput{}, nil
		},
		getSecretValue: func(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
			return &secretsmanager.GetSecretValueOutput{SecretString: &secretJSON}, nil
		},
		deleteSecret: func(_ context.Context, _ *secretsmanager.DeleteSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error) {
			return &secretsmanager.DeleteSecretOutput{}, nil
		},
	}

	store := newTokenStoreFromClient(mock)
	ctx := context.Background()
	_ = store.StoreToken(ctx, "item_1", sentinel)
	_, _ = store.GetToken(ctx, "item_1")
	_ = store.DeleteToken(ctx, "item_1")

	for _, rec := range handler.records {
		if strings.Contains(rec.Message, sentinel) {
			t.Errorf("sentinel token found in log message: %q", rec.Message)
		}
		rec.Attrs(func(a slog.Attr) bool {
			if strings.Contains(a.Value.String(), sentinel) {
				t.Errorf("sentinel token found in log attr %q: %v", a.Key, a.Value)
			}
			return true
		})
	}
}
