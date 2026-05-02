// Package ws implements the WebSocket gateway (F3) for bmux.
// It serves mobile clients over Tailscale, multiplexing pane output streams
// and routing key input back to the tmux session.
package ws

import "context"

// ControlModeBridge is a local interface for F1 (not yet implemented).
// It provides per-pane subscriptions for live PTY output and key input routing.
type ControlModeBridge interface {
	// Subscribe returns a channel that receives raw PTY bytes for paneID.
	// The channel is closed when the subscription is cancelled via the returned cancel func.
	Subscribe(paneID string) (<-chan []byte, func(), error)

	// SendKeys sends translated key sequences to the pane.
	SendKeys(ctx context.Context, paneID string, keys string, literal bool) error
}

// MirrorManager is a local alias for F2 HeadlessMirrorManager (subset used here).
type MirrorManager interface {
	// Snapshot returns the current grid state of paneID as ANSI escape sequences.
	Snapshot(paneID string, cols, rows int) ([]byte, error)
}

// SessionRegistry is a local interface for F4 (not yet implemented).
// It provides the list of active tmux sessions and panes.
type SessionRegistry interface {
	// Sessions returns the current session list.
	Sessions(ctx context.Context) ([]Session, error)
}

// Session describes a tmux session.
type Session struct {
	Name  string `json:"name"`
	ID    string `json:"id"`
	Panes []Pane `json:"panes"`
}

// Pane describes a tmux pane within a session.
type Pane struct {
	ID     string `json:"id"`
	Window string `json:"window"`
}

// KeyTranslator is a local interface for F5 (not yet implemented).
// It translates mobile key sequences to tmux-compatible strings.
type KeyTranslator interface {
	// Translate converts a raw key string to a tmux send-keys compatible sequence.
	Translate(keys string) string
}
