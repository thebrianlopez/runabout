package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// loadSecrets fetches each named secret from AWS Secrets Manager and populates
// the process environment. Each secret must be stored as a JSON object whose
// keys become environment variables: {"ANTHROPIC_API_KEY": "sk-...", ...}.
// Values are set via os.Setenv so subsequent os.Getenv calls (including the
// ANTHROPIC_API_KEY and BRAINTRUST_API_KEY checks in run()) see them without
// any other changes to the binary.
//
// Accepts secret names, full ARNs, or partial ARNs - whatever GetSecretValue accepts.
// Returns nil immediately when secretIDs is empty (no-op for direct env var usage).
func loadSecrets(ctx context.Context, secretIDs []string) error {
	if len(secretIDs) == 0 {
		return nil
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("AWS config: %w", err)
	}
	client := secretsmanager.NewFromConfig(cfg)
	for _, id := range secretIDs {
		if err := fetchAndApply(ctx, client, id); err != nil {
			return err
		}
	}
	return nil
}

func fetchAndApply(ctx context.Context, client *secretsmanager.Client, id string) error {
	out, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &id,
	})
	if err != nil {
		return fmt.Errorf("secret %q: %w", id, err)
	}
	if out.SecretString == nil {
		return fmt.Errorf("secret %q: binary secrets not supported", id)
	}
	var kv map[string]string
	if err := json.Unmarshal([]byte(*out.SecretString), &kv); err != nil {
		return fmt.Errorf("secret %q: invalid JSON (expected {\"KEY\": \"value\", ...}): %w", id, err)
	}
	for k, v := range kv {
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("secret %q key %q: %w", id, k, err)
		}
	}
	return nil
}
