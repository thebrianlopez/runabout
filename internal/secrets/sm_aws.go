// Production AWS Secrets Manager factory backing the SecretsManagerAPI
// interface in secrets.go. Kept in its own file so tests in secrets_test.go
// can stay free of AWS SDK imports and run without AWS config discovery.
package secrets

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// AWSConfig controls default AWS SDK resolution for Secrets Manager.
type AWSConfig struct {
	Region  string
	Profile string
	RoleARN string
}

var loadDefaultConfig = config.LoadDefaultConfig
var newSecretsManagerFromConfig = secretsmanager.NewFromConfig
var newSTSFromConfig = sts.NewFromConfig
var newAssumeRoleProvider = stscreds.NewAssumeRoleProvider

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
func DefaultAWSFactory(awsCfg AWSConfig) func(ctx context.Context) (SecretsManagerAPI, error) {
	return func(ctx context.Context) (SecretsManagerAPI, error) {
		var opts []func(*config.LoadOptions) error
		if awsCfg.Region != "" {
			opts = append(opts, config.WithRegion(awsCfg.Region))
		}
		if awsCfg.Profile != "" {
			opts = append(opts, config.WithSharedConfigProfile(awsCfg.Profile))
		}
		cfg, err := loadDefaultConfig(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("secrets: load aws config: %w", err)
		}
		if awsCfg.RoleARN != "" {
			cfg.Credentials = newAssumeRoleProvider(newSTSFromConfig(cfg), awsCfg.RoleARN)
		}
		return &awsSMClient{c: newSecretsManagerFromConfig(cfg)}, nil
	}
}
