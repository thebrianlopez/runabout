package registry

import (
	"context"
	"log/slog"
	"sync"
)

// SessionRegistry maintains an in-memory map of active tmux sessions and panes.
// It is populated on Start via F1 ListSessions/ListPanes, then kept current by
// consuming the F1 Events() channel. Topology changes are fanned out to
// registered subscriber channels.
type SessionRegistry interface {
	// Start populates the registry and begins consuming F1 events.
	Start(ctx context.Context) error

	// Stop shuts down the event goroutine.
	Stop()

	// Snapshot returns a copy of the current session list. Never nil.
	Snapshot() []Session

	// Subscribe registers ch to receive topology events.
	// Returns an unsubscribe func; calling it removes the channel.
	// ch must be buffered by the caller; a full channel causes event drop + WARN log.
	Subscribe(ch chan<- TopologyEvent) (unsubscribe func())
}

// New creates a SessionRegistry backed by the given bridge.
func New(b bridge) SessionRegistry {
	return &sessionRegistry{
		b:           b,
		sessions:    map[string]*Session{},
		subscribers: map[chan<- TopologyEvent]struct{}{},
	}
}

type sessionRegistry struct {
	b bridge

	mu          sync.RWMutex
	sessions    map[string]*Session // keyed by session name

	subMu       sync.Mutex
	subscribers map[chan<- TopologyEvent]struct{}

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// Start populates the registry from F1 and spawns the event-consumption goroutine.
func (r *sessionRegistry) Start(ctx context.Context) error {
	r.stopCh = make(chan struct{})
	r.doneCh = make(chan struct{})

	if err := r.populate(ctx); err != nil {
		return err
	}

	go r.run(ctx)
	return nil
}

// populate calls ListSessions + ListPanes and rebuilds the in-memory map.
func (r *sessionRegistry) populate(ctx context.Context) error {
	sessions, err := r.b.ListSessions(ctx)
	if err != nil {
		return err
	}

	newMap := make(map[string]*Session, len(sessions))
	paneTotal := 0
	for i := range sessions {
		s := sessions[i]
		panes, err := r.b.ListPanes(ctx, s.Name)
		if err != nil {
			return err
		}
		s.Panes = panes
		paneTotal += len(panes)
		newMap[s.Name] = &s
	}

	r.mu.Lock()
	r.sessions = newMap
	r.mu.Unlock()

	slog.Info("registry_started", "session_count", len(sessions), "pane_count", paneTotal)
	return nil
}

// run is the event-consumption goroutine. When Events() closes (F1 disconnect),
// it re-populates and re-subscribes, implementing the reconnect loop (BT-3).
func (r *sessionRegistry) run(ctx context.Context) {
	defer close(r.doneCh)
	for {
		eventCh := r.b.Events()
		for {
			select {
			case <-r.stopCh:
				return
			case <-ctx.Done():
				return
			case ev, ok := <-eventCh:
				if !ok {
					// F1 disconnected — emit stale notice and re-populate.
					slog.Warn("registry_stale", "reason", "F1 channel closed")
					r.fanOut(TopologyEvent{Type: TopologyStale})
					// Re-populate from F1 (blocking until ctx is cancelled or success).
					if err := r.populate(ctx); err != nil {
						slog.Warn("registry_stale", "reason", err.Error())
					}
					goto nextChannel
				}
				r.apply(ev)
			}
		}
	nextChannel:
		// Check if we should stop before re-subscribing.
		select {
		case <-r.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}
	}
}

// apply updates the registry based on a single F1 event and fans out.
func (r *sessionRegistry) apply(ev ControlModeEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch ev.Type {
	case EventSessionCreated:
		if _, exists := r.sessions[ev.SessionName]; !exists {
			s := &Session{Name: ev.SessionName}
			r.sessions[ev.SessionName] = s
			slog.Debug("registry_session_added", "session", ev.SessionName)
			// Fan out without holding the registry lock — copy the pointer.
			go r.fanOut(TopologyEvent{Type: TopologySessionCreated, Session: copySession(s)})
		}

	case EventSessionClosed:
		if _, exists := r.sessions[ev.SessionName]; exists {
			delete(r.sessions, ev.SessionName)
			slog.Debug("registry_session_removed", "session", ev.SessionName)
			go r.fanOut(TopologyEvent{Type: TopologySessionClosed, Session: &Session{Name: ev.SessionName}})
		}

	case EventWindowAdd:
		// Window topology changes are tracked but pane-level detail is the primary unit.
		s, ok := r.sessions[ev.SessionName]
		if ok {
			go r.fanOut(TopologyEvent{Type: TopologyWindowAdded, Session: copySession(s)})
		}

	case EventPaneExited:
		s, ok := r.sessions[ev.SessionName]
		if !ok {
			// Unknown session — no-op (RG-1: no panic).
			return
		}
		removed := false
		panes := s.Panes[:0:len(s.Panes)]
		panes = panes[:0]
		for _, p := range s.Panes {
			if p.ID == ev.PaneID {
				removed = true
				continue
			}
			panes = append(panes, p)
		}
		if !removed {
			// Unknown pane ID — no-op (RG-1: no panic).
			return
		}
		s.Panes = panes
		slog.Debug("registry_pane_exited", "session", ev.SessionName, "pane", ev.PaneID)
		go r.fanOut(TopologyEvent{Type: TopologyPaneExited, Session: copySession(s), PaneID: ev.PaneID})
	}
}

// fanOut delivers ev to all registered subscriber channels.
// Non-blocking: a full channel causes an event drop + WARN log.
func (r *sessionRegistry) fanOut(ev TopologyEvent) {
	r.subMu.Lock()
	subs := make([]chan<- TopologyEvent, 0, len(r.subscribers))
	for ch := range r.subscribers {
		subs = append(subs, ch)
	}
	r.subMu.Unlock()

	dropped := 0
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
			dropped++
		}
	}
	if dropped > 0 {
		slog.Warn("registry_event_dropped", "subscriber_count", dropped, "event_type", string(ev.Type))
	}
}

// Snapshot returns a copy of the current session slice. Never nil.
func (r *sessionRegistry) Snapshot() []Session {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, copySessionVal(s))
	}
	return out
}

// Subscribe registers ch to receive topology events. Returns unsubscribe func.
func (r *sessionRegistry) Subscribe(ch chan<- TopologyEvent) func() {
	r.subMu.Lock()
	r.subscribers[ch] = struct{}{}
	r.subMu.Unlock()

	return func() {
		r.subMu.Lock()
		delete(r.subscribers, ch)
		r.subMu.Unlock()
	}
}

// Stop signals the event goroutine to exit and waits for it to finish.
func (r *sessionRegistry) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
		if r.doneCh != nil {
			<-r.doneCh
		}
	})
}

// copySession returns a pointer to a deep copy of s (safe to publish).
func copySession(s *Session) *Session {
	c := copySessionVal(s)
	return &c
}

// copySessionVal returns a deep copy of *s as a value.
func copySessionVal(s *Session) Session {
	panes := make([]Pane, len(s.Panes))
	copy(panes, s.Panes)
	return Session{
		Name:  s.Name,
		ID:    s.ID,
		Panes: panes,
	}
}
