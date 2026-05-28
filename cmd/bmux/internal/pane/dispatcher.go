// Package pane implements pane lifecycle synchronisation (F4). It provides:
//   - EventDispatcher: fans a single PaneEvent channel into typed sub-channels
//   - LifecycleSyncer: applies PaneCreated/PaneClosed/PaneResized events locally
package pane

import (
	"context"
	"fmt"

	"github.com/blo-grindr/bmux/internal/bridge"
	"github.com/blo-grindr/bmux/internal/ssh"
)

// EventDispatcher fans out a single PaneEvent channel into typed sub-channels.
type EventDispatcher interface {
	// Output returns a channel receiving only PaneOutput events.
	Output() <-chan ssh.PaneEvent

	// Lifecycle returns a channel receiving PaneCreated/PaneClosed/PaneResized.
	Lifecycle() <-chan ssh.PaneEvent

	// Run reads from src and dispatches until src closes or ctx is cancelled.
	// Both Output() and Lifecycle() channels are closed when Run returns.
	Run(ctx context.Context, src <-chan ssh.PaneEvent) error
}

// LifecycleSyncer applies remote pane lifecycle events to the local tmux session.
type LifecycleSyncer interface {
	// Apply handles a single lifecycle event. Idempotent.
	Apply(ctx context.Context, event ssh.PaneEvent) error

	// PropagateResize sends the current terminal dimensions to the remote pane.
	PropagateResize(ctx context.Context, rows, cols int) error
}

// NewEventDispatcher creates an EventDispatcher with buffered sub-channels.
func NewEventDispatcher() EventDispatcher {
	return &dispatcher{
		outCh:  make(chan ssh.PaneEvent, 256),
		lifeCh: make(chan ssh.PaneEvent, 256),
	}
}

type dispatcher struct {
	outCh  chan ssh.PaneEvent
	lifeCh chan ssh.PaneEvent
}

func (d *dispatcher) Output() <-chan ssh.PaneEvent    { return d.outCh }
func (d *dispatcher) Lifecycle() <-chan ssh.PaneEvent { return d.lifeCh }

func (d *dispatcher) Run(ctx context.Context, src <-chan ssh.PaneEvent) error {
	defer close(d.outCh)
	defer close(d.lifeCh)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-src:
			if !ok {
				return nil // src closed cleanly
			}
			switch ev.Type {
			case ssh.PaneOutput:
				select {
				case d.outCh <- ev:
				default:
				}
			case ssh.PaneCreated, ssh.PaneClosed, ssh.PaneResized:
				select {
				case d.lifeCh <- ev:
				default:
				}
			}
		}
	}
}

// NewLifecycleSyncer constructs a LifecycleSyncer for the given host.
func NewLifecycleSyncer(host string, session ssh.Session, br bridge.LocalTmuxBridge) LifecycleSyncer {
	return &syncer{host: host, session: session, bridge: br}
}

type syncer struct {
	host    string
	session ssh.Session
	bridge  bridge.LocalTmuxBridge
}

func (s *syncer) Apply(_ context.Context, event ssh.PaneEvent) error {
	switch event.Type {
	case ssh.PaneCreated:
		if err := s.bridge.EnsurePane(s.host, event.PaneID); err != nil {
			// E50: log and continue — never fatal.
			fmt.Printf("[%s: failed to create local pane %s: %v]\n", s.host, event.PaneID, err)
		}
	case ssh.PaneClosed:
		if err := s.bridge.RemovePane(s.host, event.PaneID); err != nil {
			// E51: log and continue.
			fmt.Printf("[%s: failed to remove local pane %s: %v]\n", s.host, event.PaneID, err)
		}
	case ssh.PaneResized:
		if err := s.bridge.ResizePane(s.host, event.PaneID, event.Rows, event.Cols); err != nil {
			fmt.Printf("[%s: failed to resize local pane %s: %v]\n", s.host, event.PaneID, err)
		}
	}
	return nil
}

// PropagateResize sends `resize-pane -t <host> -x <cols> -y <rows>` to the
// remote tmux via Session.SendInput (written to the control mode stdin).
func (s *syncer) PropagateResize(_ context.Context, rows, cols int) error {
	cmd := fmt.Sprintf("resize-pane -t %s -x %d -y %d\n", s.host, cols, rows)
	return s.session.SendInput([]byte(cmd))
}
