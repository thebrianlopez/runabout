package secrets

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

func TestDefaultAWSFactory_ZeroValueConfig(t *testing.T) {
	origLoad := loadDefaultConfig
	origSM := newSecretsManagerFromConfig
	defer func() {
		loadDefaultConfig = origLoad
		newSecretsManagerFromConfig = origSM
	}()

	var gotOpts []func(*config.LoadOptions) error
	loadDefaultConfig = func(ctx context.Context, opts ...func(*config.LoadOptions) error) (aws.Config, error) {
		gotOpts = append([]func(*config.LoadOptions) error(nil), opts...)
		return aws.Config{}, nil
	}
	newSecretsManagerFromConfig = func(cfg aws.Config, optFns ...func(*secretsmanager.Options)) *secretsmanager.Client { return nil }

	factory := DefaultAWSFactory(AWSConfig{})
	if _, err := factory(context.Background()); err != nil {
		t.Fatalf("factory: %v", err)
	}
	if len(gotOpts) != 0 {
		t.Fatalf("expected zero opts, got %d", len(gotOpts))
	}
}

func TestDefaultAWSFactory_ThreadsProfileAndRegion(t *testing.T) {
	origLoad := loadDefaultConfig
	origSM := newSecretsManagerFromConfig
	defer func() {
		loadDefaultConfig = origLoad
		newSecretsManagerFromConfig = origSM
	}()

	var sawRegion, sawProfile bool
	loadDefaultConfig = func(ctx context.Context, opts ...func(*config.LoadOptions) error) (aws.Config, error) {
		lo := &config.LoadOptions{}
		for _, opt := range opts {
			if err := opt(lo); err != nil {
				t.Fatalf("opt: %v", err)
			}
		}
		sawRegion = lo.Region == "us-west-2"
		sawProfile = lo.SharedConfigProfile == "testprofile"
		return aws.Config{}, nil
	}
	newSecretsManagerFromConfig = func(cfg aws.Config, optFns ...func(*secretsmanager.Options)) *secretsmanager.Client { return nil }

	factory := DefaultAWSFactory(AWSConfig{Region: "us-west-2", Profile: "testprofile"})
	if _, err := factory(context.Background()); err != nil {
		t.Fatalf("factory: %v", err)
	}
	if !sawRegion || !sawProfile {
		t.Fatalf("region/profile not threaded: region=%v profile=%v", sawRegion, sawProfile)
	}
}

func TestDefaultAWSFactory_AssumeRoleWrapsCredentials(t *testing.T) {
	origLoad := loadDefaultConfig
	origSM := newSecretsManagerFromConfig
	origSTS := newSTSFromConfig
	origAssume := newAssumeRoleProvider
	defer func() {
		loadDefaultConfig = origLoad
		newSecretsManagerFromConfig = origSM
		newSTSFromConfig = origSTS
		newAssumeRoleProvider = origAssume
	}()

	loadDefaultConfig = func(ctx context.Context, opts ...func(*config.LoadOptions) error) (aws.Config, error) {
		return aws.Config{}, nil
	}
	newSTSFromConfig = func(cfg aws.Config, optFns ...func(*sts.Options)) *sts.Client { return &sts.Client{} }
	newAssumeRoleProvider = func(c stscreds.AssumeRoleAPIClient, roleARN string, optFns ...func(*stscreds.AssumeRoleOptions)) *stscreds.AssumeRoleProvider {
		if roleARN != "arn:aws:iam::123:role/r" {
			t.Fatalf("roleARN=%q", roleARN)
		}
		return &stscreds.AssumeRoleProvider{}
	}
	newSecretsManagerFromConfig = func(cfg aws.Config, optFns ...func(*secretsmanager.Options)) *secretsmanager.Client {
		if cfg.Credentials == nil {
			t.Fatal("expected credentials to be wrapped")
		}
		if _, ok := cfg.Credentials.(*stscreds.AssumeRoleProvider); !ok {
			t.Fatalf("credentials type = %T, want *stscreds.AssumeRoleProvider", cfg.Credentials)
		}
		return nil
	}

	factory := DefaultAWSFactory(AWSConfig{RoleARN: "arn:aws:iam::123:role/r"})
	if _, err := factory(context.Background()); err != nil {
		t.Fatalf("factory: %v", err)
	}
}
