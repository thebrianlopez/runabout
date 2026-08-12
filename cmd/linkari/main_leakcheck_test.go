//go:build leakcheck

// EPIC-250 M4: goroutine-leak detection. Kept behind the `leakcheck` build tag
// so ordinary `go test` runs stay fast; CI runs it as a dedicated step.
//
//	go test -tags leakcheck ./cmd/linkari/...
//
// The ignore list below is deliberately restricted to goroutines that are owned
// and managed by third-party frameworks (Go's net/http transport, the AWS SDK's
// credential/retry machinery). All application-owned leaks that an earlier pass
// surfaced  -  StartReplay's ticker loop, SourceRegistry.Start, unclosed test
// queues, and the RG-2 multipart pipe writer  -  have been fixed at the source
// rather than suppressed here. Do NOT add application (cmd/linkari.*) functions
// to this list; fix the leak instead.
package main

import (
	"testing"

	"go.uber.org/goleak"
)

func runTests(m *testing.M) int {
	goleakVerifyTestMain(
		m,
		// net/http's transport idle-connection reaper and DNS resolution
		// goroutines are long-lived by design and not test-owned leaks.
		goleak.IgnoreTopFunction("net/http.(*Transport).dialConn"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),

		// AWS SDK credential-resolution goroutines. Scoring paths that read
		// config values sourced from `${secretsmanager:...}` cause the AWS SDK
		// to resolve credentials and call Secrets Manager. In tests (and any
		// environment without reachable AWS credentials) the SDK spins its own
		// retry/backoff goroutines that sit in `sleepWithContext` and its
		// singleflight-deduplicated `CredentialsCache.Retrieve` waiter. These
		// are internal to aws-sdk-go-v2, are cancelled by their own contexts on
		// process exit, and are not spawned or owned by our code, so we cannot
		// join them from cmd/linkari. Scoped to the two exact top-of-stack
		// functions rather than a broad package prefix so a genuine leak that
		// merely passes through the SDK is still caught.
		goleak.IgnoreTopFunction("github.com/aws/aws-sdk-go-v2/internal/sdk.sleepWithContext"),
		goleak.IgnoreTopFunction("github.com/aws/aws-sdk-go-v2/aws.(*CredentialsCache).Retrieve"),
	)
	return 0
}

func goleakVerifyTestMain(m *testing.M, opts ...goleak.Option) {
	goleak.VerifyTestMain(m, opts...)
}
