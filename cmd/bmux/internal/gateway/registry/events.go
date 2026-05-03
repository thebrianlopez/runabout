// Package registry implements the SessionRegistry (F4) for bmux.
// It maintains an in-memory map of active tmux sessions and panes,
// driven by the F1 ControlModeBridge event bus (no polling).
package registry

import "context"

// EventType classifies F1 control-mode events.
type EventType string

const (
	EventSessionCreated EventType = "session-created"
	EventSessionClosed  EventType = "session-closed"
	EventWindowAdd      EventType = "window-add"
	EventPaneExited     EventType = "pane-exited"
)

// ControlModeEvent is a topology change notification from F1.
type ControlModeEvent struct {
	Type        EventType
	SessionName string
	PaneID      string
}

// Session describes a tmux session with its panes.
type Session struct {
	Name  string
	ID    string
	Panes []Pane
}

// Pane describes a tmux pane within a session.
type Pane struct {
	ID     string
	Window string
}

// TopologyEventType classifies registry topology events fanned out to subscribers.
type TopologyEventType string

const (
	TopologySessionCreated TopologyEventType = "session-created"
	TopologySessionClosed  TopologyEventType = "session-closed"
	TopologyWindowAdded    TopologyEventType = "window-add"
	TopologyPaneExited     TopologyEventType = "pane-exited"
	TopologyStale          TopologyEventType = "stale"
)

// TopologyEvent is delivered to subscribers on registry changes.
type TopologyEvent struct {
	Type    TopologyEventType
	Session *Session
	PaneID  string
}

// F1Bridge is the subset of F1 ControlModeBridge the registry requires.
type F1Bridge interface {
	// Events returns a channel of topology change events from F1.
	// The channel is closed when F1 disconnects; the registry will
	// re-query ListSessions/ListPanes when a new channel is obtained.
	Events() <-chan ControlModeEvent

	// ListSessions returns all active tmux sessions.
	ListSessions(ctx context.Context) ([]Session, error)

	// ListPanes returns all panes for a given session.
	ListPanes(ctx context.Context, sessionName string) ([]Pane, error)
}
