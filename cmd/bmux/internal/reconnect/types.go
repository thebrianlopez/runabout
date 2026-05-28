// Package reconnect implements exponential-backoff reconnect scheduling for
// SSH sessions that drop unexpectedly. The reconnect loop watches
// Session.Disconnected() and calls SSHManager.Connect() with configurable
// exponential backoff until the session is restored or a non-retryable error
// is encountered.
package reconnect

import (
	"context"
	"time"

	"github.com/blo-grindr/bmux/internal/config"
	"github.com/blo-grindr/bmux/internal/ssh"
)

// Clock abstracts time for testability. The real implementation delegates to
// the standard library; the mock fires channels immediately.
type Clock interface {
	After(d time.Duration) <-chan time.Time
	Now() time.Time
}

// RealClock is the production Clock implementation.
type RealClock struct{}

func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (RealClock) Now() time.Time                         { return time.Now() }

// BackoffScheduler computes the delay before the Nth reconnect attempt.
// Attempt is zero-indexed: attempt=0 is the first retry after disconnect.
type BackoffScheduler interface {
	// Next returns the delay to wait before attempt N.
	// Always returns at least 1s regardless of config (prevents tight loops).
	Next(attempt int) time.Duration

	// Config returns the scheduler's configuration.
	Config() config.ReconnectConfig
}

// SessionStatus mirrors the ssh.SessionStatus type for state change callbacks.
// Callers use the ssh package's constants directly.
type SessionStatus = ssh.SessionStatus

// RunReconnectLoop watches a session and reconnects on disconnect.
// Calls onStateChange whenever the session's status transitions.
// Returns ctx.Err() when ctx is cancelled, or nil on clean shutdown.
func RunReconnectLoop(
	ctx context.Context,
	host config.HostConfig,
	initialSession ssh.Session,
	manager ssh.SSHManager,
	scheduler BackoffScheduler,
	clock Clock,
	onStateChange func(host string, status ssh.SessionStatus),
) error {
	return runLoop(ctx, host, initialSession, manager, scheduler, clock, onStateChange)
}
