// Package daemon implements the bmux daemon lifecycle: start, stop, status,
// PID file management, and atomic state file writes.
package daemon

import (
	"context"
)

// DaemonManager controls the daemon process lifecycle.
type DaemonManager interface {
	// Start spawns bmux serve, waits for the ready sentinel (up to readyTimeout).
	// Returns daemon_already_running (E20) if the daemon is alive, or
	// daemon_start_timeout (E23) if the sentinel does not appear in time.
	Start(ctx context.Context, configPath string) error

	// Stop sends SIGTERM to the daemon; escalates to SIGKILL after stopTimeout.
	// Returns daemon_not_running (E21) if no live daemon is found.
	Stop(ctx context.Context) error

	// Status reads the daemon's current state from status.json.
	// Returns daemon_not_running (E21) if no PID file or process is dead.
	Status(ctx context.Context) (*DaemonStatus, error)

	// IsRunning checks whether the daemon process is alive.
	// Returns (alive, pid, err).
	IsRunning() (bool, int, error)
}

// DaemonStatus is the structure written to status.json by the running daemon.
type DaemonStatus struct {
	PID   int          `json:"pid"`
	Hosts []HostStatus `json:"hosts"`
}

// HostStatus reports the connection state for a single host.
type HostStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "connected" | "reconnecting" | "disconnected"
}
