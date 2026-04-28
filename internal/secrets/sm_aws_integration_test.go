//go:build integration

// Integration test for the production AWS SM factory against localstack.
//
// Run with:
//
//	docker run --rm -d -p 4566:4566 localstack/localstack
//	aws --endpoint-url=http://localhost:4566 secretsmanager create-secret \
//	    --name linkari/test-token --secret-string hunter2
//	AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-2 \
//	  LINKARI_LOCALSTACK_SM_ENDPOINT=http://localhost:4566 \
//	  go test -tags=integration ./cmd/linkari/internal/secrets/...
package secrets

import (
	"context"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

func TestAWSFactory_Localstack(t *testing.T) {
	endpoint := os.Getenv("LINKARI_LOCALSTACK_SM_ENDPOINT")
	if endpoint == "" {
		t.Skip("LINKARI_LOCALSTACK_SM_ENDPOINT not set")
	}

	ctx := context.Background()
	factory := func(ctx context.Context) (SecretsManagerAPI, error) {
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, err
		}
		c := secretsmanager.NewFromConfig(cfg, func(o *secretsmanager.Options) {
			o.BaseEndpoint = &endpoint
		})
		return &awsSMClient{c: c}, nil
	}

	r := New(factory)
	val, src, err := r.Resolve(ctx, "secretsmanager://linkari/test-token")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if val != "hunter2" {
		t.Errorf("got value %q, want %q", val, "hunter2")
	}
	if src.Scheme != "secretsmanager" || src.ID != "linkari/test-token" {
		t.Errorf("unexpected source: %+v", src)
	}
}
