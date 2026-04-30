package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

// ErrInfraAuth signals that the AWS credential chain failed mid-sync.
// Distinct from secret_not_found (secret absent from ASM).
// Callers must not escalate item status to login_required on this error.
var ErrInfraAuth = errors.New("infra auth failed")

// tokenSecret is the JSON shape stored in ASM.
type tokenSecret struct {
	AccessToken string `json:"access_token"`
	ItemID      string `json:"item_id"`
	CreatedAt   string `json:"created_at"`
}

// secretsClient is the narrow interface over ASM operations used by TokenStore.
// *secretsmanager.Client satisfies this automatically; tests supply a mock.
type secretsClient interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
	PutSecretValue(ctx context.Context, in *secretsmanager.PutSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
	CreateSecret(ctx context.Context, in *secretsmanager.CreateSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
	DeleteSecret(ctx context.Context, in *secretsmanager.DeleteSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error)
}

// TokenStore manages Plaid access tokens in AWS Secrets Manager.
// All secrets live under plaid/items/<item_id>/access_token.
type TokenStore struct {
	client secretsClient
}

func newTokenStore(ctx context.Context) (*TokenStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		return nil, fmt.Errorf("aws auth failed: %w", err)
	}
	return newTokenStoreFromClient(secretsmanager.NewFromConfig(cfg)), nil
}

// newTokenStoreFromClient constructs a TokenStore from any secretsClient.
// Used in tests to inject a mock without touching the AWS credential chain.
func newTokenStoreFromClient(c secretsClient) *TokenStore {
	return &TokenStore{client: c}
}

func (s *TokenStore) secretName(itemID string) string {
	return "plaid/items/" + itemID + "/access_token"
}

// StoreToken creates or updates the access token for an item.
// Never logs the token — only a SHA256[:8] fingerprint is safe to log.
func (s *TokenStore) StoreToken(ctx context.Context, itemID, accessToken string) error {
	payload, _ := json.Marshal(tokenSecret{
		AccessToken: accessToken,
		ItemID:      itemID,
		CreatedAt:   nowUTC(),
	})
	name := s.secretName(itemID)

	// Try update first; fall back to CreateSecret ONLY on ResourceNotFoundException
	// (secret doesn't exist yet). Any other error — e.g. AccessDeniedException — is
	// a real failure and must not silently attempt CreateSecret.
	_, err := s.client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     &name,
		SecretString: strPtr(string(payload)),
	})
	if err == nil {
		return nil
	}

	var notFound *types.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		return fmt.Errorf("secret write failed: %w", err)
	}

	_, err = s.client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         &name,
		SecretString: strPtr(string(payload)),
	})
	if err != nil {
		return fmt.Errorf("secret write failed: %w", err)
	}
	return nil
}

// GetToken retrieves the access token for an item.
// Returns secret_not_found (ResourceNotFoundException) if the secret is absent.
// Returns ErrInfraAuth for all other ASM errors (expired credentials, permission denied, etc.).
func (s *TokenStore) GetToken(ctx context.Context, itemID string) (string, error) {
	name := s.secretName(itemID)
	out, err := s.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &name,
	})
	if err != nil {
		var notFound *types.ResourceNotFoundException
		if errors.As(err, &notFound) {
			return "", fmt.Errorf("secret not found for item %s: %w", itemID, err)
		}
		return "", fmt.Errorf("%w: get secret for item %s: %v", ErrInfraAuth, itemID, err)
	}

	var ts tokenSecret
	if err := json.Unmarshal([]byte(*out.SecretString), &ts); err != nil {
		return "", fmt.Errorf("secret parse error: %w", err)
	}
	return ts.AccessToken, nil
}

// DeleteToken removes the access token for an item immediately (no recovery window).
func (s *TokenStore) DeleteToken(ctx context.Context, itemID string) error {
	name := s.secretName(itemID)
	forceDelete := true
	_, err := s.client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   &name,
		ForceDeleteWithoutRecovery: &forceDelete,
	})
	return err
}

func strPtr(s string) *string { return &s }
