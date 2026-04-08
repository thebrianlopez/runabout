// Production AWS Secrets Manager factory backing the SecretsManagerAPI
// interface in secrets.go. Kept in its own file so tests in secrets_test.go
// can stay free of AWS SDK imports and run without AWS config discovery.
package secrets

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// awsSMClient adapts *secretsmanager.Client to the SecretsManagerAPI surface.
type awsSMClient struct {
	c *secretsmanager.Client
}

func (a *awsSMClient) GetSecretValue(ctx context.Context, id string) (string, error) {
	out, err := a.c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &id,
	})
	if err != nil {
		return "", err
	}
	if out.SecretString != nil {
		return *out.SecretString, nil
	}
	if len(out.SecretBinary) > 0 {
		return string(out.SecretBinary), nil
	}
	return "", fmt.Errorf("secrets: %s: empty SecretString and SecretBinary", id)
}

// DefaultAWSFactory returns a smFactory that loads default AWS config (env,
// shared config files, IMDS, etc.) and constructs a Secrets Manager client.
// Pass to New() in production. Tests should not use this — supply a fake
// SecretsManagerAPI instead.
func DefaultAWSFactory() func(ctx context.Context) (SecretsManagerAPI, error) {
	return func(ctx context.Context) (SecretsManagerAPI, error) {
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("secrets: load aws config: %w", err)
		}
		return &awsSMClient{c: secretsmanager.NewFromConfig(cfg)}, nil
	}
}
