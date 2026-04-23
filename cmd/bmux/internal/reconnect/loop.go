package reconnect

import (
	"context"
	"fmt"

	"github.com/blo-grindr/bmux/internal/config"
	"github.com/blo-grindr/bmux/internal/ssh"
)

// nonRetryableCodes are SSH error codes that require human intervention.
// Retrying auth failures or host-key mismatches would be pointless.
var nonRetryableCodes = map[string]bool{
	"ssh_auth_failed":       true,
	"ssh_host_key_mismatch": true,
	"control_mode_rejected": true,
}

// isRetryable returns true if err is a transient error worth retrying.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var se *ssh.SSHError
	if ok := errorAs(err, &se); ok {
		return !nonRetryableCodes[se.Code]
	}
	return true // unknown errors: retry (e.g. network timeouts)
}

// errorAs is a local wrapper so loop.go doesn't import "errors".
func errorAs(err error, target interface{}) bool {
	type aserr interface{ As(interface{}) bool }
	if a, ok := err.(aserr); ok {
		return a.As(target)
	}
	// Use errors.As indirectly via a type switch on *ssh.SSHError.
	if se, ok := err.(*ssh.SSHError); ok {
		if t, ok := target.(**ssh.SSHError); ok {
			*t = se
			return true
		}
	}
	return false
}

// runLoop is the internal implementation of RunReconnectLoop.
func runLoop(
	ctx context.Context,
	host config.HostConfig,
	initialSession ssh.Session,
	manager ssh.SSHManager,
	scheduler BackoffScheduler,
	clock Clock,
	onStateChange func(host string, status ssh.SessionStatus),
) error {
	session := initialSession

	for {
		// Wait for the current session to disconnect.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-session.Disconnected():
		}

		// Session dropped — start reconnect loop.
		onStateChange(host.Name, ssh.StatusDisconnected)

		attempt := 0
		for {
			delay := scheduler.Next(attempt)

			// Announce reconnect attempt (caller writes to pane/log).
			onStateChange(host.Name, ssh.SessionStatus(
				fmt.Sprintf("reconnecting:%d:%.0fs", attempt, delay.Seconds()),
			))

			// Wait for backoff delay (or context cancel).
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-clock.After(delay):
			}

			// Attempt to reconnect.
			newSession, err := manager.Connect(ctx, host)
			if err == nil {
				// Reconnected.
				session = newSession
				onStateChange(host.Name, ssh.StatusConnected)
				break // inner loop — go back to watching Disconnected()
			}

			if !isRetryable(err) {
				// Non-retryable: give up and report permanent disconnect.
				onStateChange(host.Name, ssh.StatusDisconnected)
				return nil // loop exits; caller handles the permanent disconnect
			}

			attempt++
		}
	}
}
