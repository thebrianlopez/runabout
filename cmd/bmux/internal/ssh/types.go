// Package ssh manages independent SSH connections to configured remote hosts,
// parses tmux control mode event streams, and emits PaneEvents consumed by the
// local bridge and higher-level features.
package ssh

import (
	"context"

	"github.com/blo-grindr/bmux/internal/config"
)

// SessionStatus represents a connection's lifecycle state.
type SessionStatus string

const (
	StatusConnected    SessionStatus = "connected"
	StatusDisconnected SessionStatus = "disconnected"
)

// Session represents one projected remote tmux session.
type Session interface {
	// Host returns the HostConfig.Name for this session.
	Host() string

	// Status returns the current connection state.
	Status() SessionStatus

	// Disconnected is closed when the SSH session terminates.
	// Consumers (e.g. F5 reconnect scheduler) select on this channel.
	Disconnected() <-chan struct{}

	// SendInput forwards raw bytes to the remote active pane.
	// Returns ErrSessionClosed if the session is not connected.
	SendInput(data []byte) error

	// Events returns a channel of PaneEvents for this session.
	// The channel is closed when the session disconnects.
	Events() <-chan PaneEvent

	// Close tears down the SSH connection and frees resources.
	Close() error
}

// PaneEventType classifies a PaneEvent.
type PaneEventType string

const (
	PaneOutput  PaneEventType = "output"
	PaneCreated PaneEventType = "created"
	PaneClosed  PaneEventType = "closed"
	PaneResized PaneEventType = "resized"
)

// PaneEvent is emitted by ControlModeParser for each parsed control mode event.
type PaneEvent struct {
	Type   PaneEventType
	Host   string
	PaneID string
	Data   []byte // for Output events: raw pane bytes (decoded from wire encoding)
	Rows   int    // for Resized events
	Cols   int
}

// SSHManager manages independent SSH connections to all configured hosts.
type SSHManager interface {
	// Connect establishes SSH + control mode for a single host.
	// Blocks until connected or returns a typed *SSHError.
	Connect(ctx context.Context, host config.HostConfig) (Session, error)

	// Disconnect closes the SSH session for the named host.
	Disconnect(name string) error

	// Sessions returns all currently-connected sessions.
	Sessions() []Session

	// Events returns the shared channel of PaneEvents from all hosts.
	// The channel is never closed while the manager is alive.
	Events() <-chan PaneEvent
}
