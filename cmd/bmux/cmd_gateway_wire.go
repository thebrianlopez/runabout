package main

import (
	"context"
	"fmt"
	"time"

	"github.com/blo-grindr/bmux/internal/config"
	"github.com/blo-grindr/bmux/internal/gateway/ctlbridge"
	"github.com/blo-grindr/bmux/internal/gateway/keys"
	"github.com/blo-grindr/bmux/internal/gateway/lifecycle"
	"github.com/blo-grindr/bmux/internal/gateway/registry"
	"github.com/blo-grindr/bmux/internal/gateway/ws"
	"github.com/blo-grindr/bmux/internal/mirror"
)

// wsReadyBridge adapts ctlbridge.ControlModeBridge + keys.KeyTranslator to ws.ControlModeBridge.
type wsReadyBridge struct {
	b  ctlbridge.ControlModeBridge
	kt keys.KeyTranslator
}

func (w *wsReadyBridge) Subscribe(paneID string) (<-chan []byte, func(), error) {
	ch := make(chan []byte, 64)
	unsub := w.b.Subscribe(paneID, ch)
	return ch, unsub, nil
}

func (w *wsReadyBridge) SendKeys(ctx context.Context, paneID string, input string, literal bool) error {
	ops, err := w.kt.Translate(input, literal)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if err := w.b.SendKeys(ctx, paneID, op.Keys, op.Literal); err != nil {
			return err
		}
	}
	return nil
}

// passthroughTranslator implements ws.KeyTranslator as identity.
// Actual translation happens in wsReadyBridge.SendKeys via the keys package.
type passthroughTranslator struct{}

func (passthroughTranslator) Translate(k string) string { return k }

// registryForWS adapts registry.SessionRegistry to ws.SessionRegistry.
type registryForWS struct {
	r registry.SessionRegistry
}

func (rws *registryForWS) Sessions(_ context.Context) ([]ws.Session, error) {
	snap := rws.r.Snapshot()
	out := make([]ws.Session, len(snap))
	for i, s := range snap {
		panes := make([]ws.Pane, len(s.Panes))
		for j, p := range s.Panes {
			panes[j] = ws.Pane{ID: p.ID, Window: p.Window}
		}
		out[i] = ws.Session{Name: s.Name, ID: s.ID, Panes: panes}
	}
	return out, nil
}

// bridgeForRegistry adapts ctlbridge.ControlModeBridge to registry.F1Bridge.
// A background goroutine converts ctlbridge topology events to registry events.
type bridgeForRegistry struct {
	b       ctlbridge.ControlModeBridge
	eventCh chan registry.ControlModeEvent
}

func newBridgeForRegistry(b ctlbridge.ControlModeBridge) *bridgeForRegistry {
	bfr := &bridgeForRegistry{
		b:       b,
		eventCh: make(chan registry.ControlModeEvent, 256),
	}
	go bfr.pump()
	return bfr
}

func (bfr *bridgeForRegistry) pump() {
	for ev := range bfr.b.Events() {
		var t registry.EventType
		switch ev.Type {
		case ctlbridge.EventSessionCreated:
			t = registry.EventSessionCreated
		case ctlbridge.EventSessionClosed:
			t = registry.EventSessionClosed
		case ctlbridge.EventWindowAdd:
			t = registry.EventWindowAdd
		case ctlbridge.EventPaneExited:
			t = registry.EventPaneExited
		default:
			continue
		}
		select {
		case bfr.eventCh <- registry.ControlModeEvent{
			Type:        t,
			SessionName: ev.Session,
			PaneID:      ev.PaneID,
		}:
		default:
		}
	}
	close(bfr.eventCh)
}

func (bfr *bridgeForRegistry) Events() <-chan registry.ControlModeEvent { return bfr.eventCh }

func (bfr *bridgeForRegistry) ListSessions(ctx context.Context) ([]registry.Session, error) {
	sessions, err := bfr.b.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]registry.Session, len(sessions))
	for i, s := range sessions {
		out[i] = registry.Session{Name: s.Name, ID: s.ID}
	}
	return out, nil
}

func (bfr *bridgeForRegistry) ListPanes(ctx context.Context, sessionName string) ([]registry.Pane, error) {
	all, err := bfr.b.ListPanes(ctx)
	if err != nil {
		return nil, err
	}
	var out []registry.Pane
	for _, p := range all {
		if p.SessionName == sessionName {
			out = append(out, registry.Pane{ID: p.ID, Window: p.WindowName})
		}
	}
	return out, nil
}

// lifecycleBridge adapts ctlbridge.ControlModeBridge to lifecycle.ControlModeBridge.
// lifecycle.ControlModeBridge.Stop() has no error return.
type lifecycleBridge struct {
	b ctlbridge.ControlModeBridge
}

func (lb *lifecycleBridge) Start(ctx context.Context) error { return lb.b.Start(ctx) }
func (lb *lifecycleBridge) Stop()                           { _ = lb.b.Stop() }

// lifecycleRegistry adapts registry.SessionRegistry to lifecycle.SessionRegistry.
// ActivePanes collects pane IDs from the current registry snapshot.
type lifecycleRegistry struct {
	r registry.SessionRegistry
}

func (lr *lifecycleRegistry) Start(ctx context.Context) error { return lr.r.Start(ctx) }
func (lr *lifecycleRegistry) Stop()                           { lr.r.Stop() }
func (lr *lifecycleRegistry) ActivePanes() []string {
	snap := lr.r.Snapshot()
	var panes []string
	for _, s := range snap {
		for _, p := range s.Panes {
			panes = append(panes, p.ID)
		}
	}
	return panes
}

// startGatewayStack wires all Phase 2 subsystems and starts the lifecycle manager.
// Returns a stop func that performs an orderly shutdown.
func startGatewayStack(ctx context.Context, cfg *config.Config) (stop func(), err error) {
	if err := config.ValidateGateway(cfg.Gateway); err != nil {
		return nil, err
	}

	b := ctlbridge.New(ctlbridge.Config{})

	idleTimeout := time.Duration(cfg.Xterm.IdleTimeoutSec) * time.Second
	mirrorMgr, err := mirror.NewHeadlessMirrorManager(mirror.Options{
		IdleTimeout: idleTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway: mirror: %w", err)
	}

	bfr := newBridgeForRegistry(b)
	reg := registry.New(bfr)

	bindAddr := fmt.Sprintf("%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
	gw, err := ws.New(ws.Config{
		Token:      cfg.Gateway.Auth.Token,
		MaxClients: 5,
		BindAddr:   bindAddr,
		Bridge:     &wsReadyBridge{b: b, kt: keys.NewKeyTranslator()},
		Mirror:     mirrorMgr,
		Registry:   &registryForWS{r: reg},
		Translator: passthroughTranslator{},
	})
	if err != nil {
		_ = mirrorMgr.Close()
		return nil, fmt.Errorf("gateway: ws.New: %w", err)
	}

	mgr := lifecycle.New(cfg.Gateway, lifecycle.Deps{
		Bridge:   &lifecycleBridge{b: b},
		Registry: &lifecycleRegistry{r: reg},
		Mirror:   mirrorMgr,
		Gateway:  gw,
	})

	if err := mgr.Start(ctx); err != nil {
		_ = mirrorMgr.Close()
		return nil, err
	}

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = mgr.Stop(shutdownCtx)
	}, nil
}
