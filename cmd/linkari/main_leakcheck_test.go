//go:build leakcheck

// EPIC-250 M4: goroutine-leak detection, opt-in only.
//
// Not enabled in CI by default: a first pass surfaced pre-existing leaks in
// suites unrelated to the firehose transcript bug this epic fixes (see the
// dispatch summary for the count observed). Flipping this on unconditionally
// would convert one known intermittent failure into many unrelated ones.
// Run manually with:
//
//	go test -tags leakcheck ./cmd/linkari/...
//
// to audit goroutine leaks incrementally, then delete this ignore list entry
// by entry as each is fixed.
package main

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(
		m,
		// net/http's transport idle-connection reaper and DNS resolution
		// goroutines are long-lived by design and not test-owned leaks.
		goleak.IgnoreTopFunction("net/http.(*Transport).dialConn"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
	)
}
