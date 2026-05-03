package registry

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubBridge is a test double for the F1 bridge interface.
// Each test creates its own instance; no shared state.
type stubBridge struct {
	mu       sync.Mutex
	sessions []Session
	panes    map[string][]Pane // keyed by session name
	eventCh  chan ControlModeEvent
}

func newStub(sessions []Session, panes map[string][]Pane) *stubBridge {
	if panes == nil {
		panes = map[string][]Pane{}
	}
	return &stubBridge{
		sessions: sessions,
		panes:    panes,
		eventCh:  make(chan ControlModeEvent, 16),
	}
}

func (s *stubBridge) Events() <-chan ControlModeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventCh
}

func (s *stubBridge) ListSessions(_ context.Context) ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Session, len(s.sessions))
	copy(out, s.sessions)
	return out, nil
}

func (s *stubBridge) ListPanes(_ context.Context, sessionName string) ([]Pane, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.panes[sessionName], nil
}

func (s *stubBridge) send(ev ControlModeEvent) {
	s.eventCh <- ev
}

// closeEvents simulates F1 disconnect.
func (s *stubBridge) closeEvents() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.eventCh)
}

// helper: start registry and return it + cancel func.
func startRegistry(t *testing.T, b F1Bridge) (SessionRegistry, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	reg := New(b)
	require.NoError(t, reg.Start(ctx))
	t.Cleanup(func() {
		cancel()
		reg.Stop()
	})
	return reg, cancel
}

// ─── Contract Tests ────────────────────────────────────────────────────────────

// CT-1: Snapshot() returns sessions populated by ListSessions/ListPanes on Start.
func TestCT1_SnapshotPopulatedOnStart(t *testing.T) {
	stub := newStub(
		[]Session{{Name: "main", ID: "1"}},
		map[string][]Pane{"main": {{ID: "p1", Window: "w1"}}},
	)
	reg, _ := startRegistry(t, stub)

	sessions := reg.Snapshot()
	require.Len(t, sessions, 1)
	assert.Equal(t, "main", sessions[0].Name)
	assert.Equal(t, "1", sessions[0].ID)
	require.Len(t, sessions[0].Panes, 1)
	assert.Equal(t, "p1", sessions[0].Panes[0].ID)
	assert.Equal(t, "w1", sessions[0].Panes[0].Window)
}

// CT-2: EventSessionCreated adds session to registry within 1s.
func TestCT2_SessionCreatedAddsSession(t *testing.T) {
	stub := newStub(nil, nil)
	reg, _ := startRegistry(t, stub)

	stub.send(ControlModeEvent{Type: EventSessionCreated, SessionName: "newsession"})

	require.Eventually(t, func() bool {
		for _, s := range reg.Snapshot() {
			if s.Name == "newsession" {
				return true
			}
		}
		return false
	}, 1*time.Second, 10*time.Millisecond)
}

// CT-3: EventSessionClosed removes session from registry.
func TestCT3_SessionClosedRemovesSession(t *testing.T) {
	stub := newStub(
		[]Session{{Name: "tobeclosed", ID: "2"}},
		nil,
	)
	reg, _ := startRegistry(t, stub)

	// Verify it starts present.
	require.Eventually(t, func() bool {
		for _, s := range reg.Snapshot() {
			if s.Name == "tobeclosed" {
				return true
			}
		}
		return false
	}, 1*time.Second, 10*time.Millisecond)

	stub.send(ControlModeEvent{Type: EventSessionClosed, SessionName: "tobeclosed"})

	require.Eventually(t, func() bool {
		for _, s := range reg.Snapshot() {
			if s.Name == "tobeclosed" {
				return false
			}
		}
		return true
	}, 1*time.Second, 10*time.Millisecond)
}

// CT-4: EventPaneExited removes pane from its session.
func TestCT4_PaneExitedRemovesPaneFromSession(t *testing.T) {
	stub := newStub(
		[]Session{{Name: "s1", ID: "3"}},
		map[string][]Pane{"s1": {{ID: "p-remove", Window: "w1"}, {ID: "p-keep", Window: "w2"}}},
	)
	reg, _ := startRegistry(t, stub)

	// Wait for population.
	require.Eventually(t, func() bool {
		snap := reg.Snapshot()
		if len(snap) == 0 {
			return false
		}
		return len(snap[0].Panes) == 2
	}, 1*time.Second, 10*time.Millisecond)

	stub.send(ControlModeEvent{Type: EventPaneExited, SessionName: "s1", PaneID: "p-remove"})

	require.Eventually(t, func() bool {
		snap := reg.Snapshot()
		if len(snap) == 0 {
			return false
		}
		for _, p := range snap[0].Panes {
			if p.ID == "p-remove" {
				return false
			}
		}
		return true
	}, 1*time.Second, 10*time.Millisecond)

	// Verify p-keep still present.
	snap := reg.Snapshot()
	require.Len(t, snap, 1)
	require.Len(t, snap[0].Panes, 1)
	assert.Equal(t, "p-keep", snap[0].Panes[0].ID)
}

// CT-5: Subscribe channel receives TopologySessionCreated event.
func TestCT5_SubscribeReceivesTopologyEvent(t *testing.T) {
	stub := newStub(nil, nil)
	reg, _ := startRegistry(t, stub)

	ch := make(chan TopologyEvent, 4)
	reg.Subscribe(ch)

	stub.send(ControlModeEvent{Type: EventSessionCreated, SessionName: "subscribed-session"})

	require.Eventually(t, func() bool {
		select {
		case ev := <-ch:
			return ev.Type == TopologySessionCreated && ev.Session != nil && ev.Session.Name == "subscribed-session"
		default:
			return false
		}
	}, 1*time.Second, 10*time.Millisecond)
}

// CT-6: Unsubscribe stops event delivery.
func TestCT6_UnsubscribeStopsDelivery(t *testing.T) {
	stub := newStub(nil, nil)
	reg, _ := startRegistry(t, stub)

	ch := make(chan TopologyEvent, 4)
	unsub := reg.Subscribe(ch)
	unsub()

	stub.send(ControlModeEvent{Type: EventSessionCreated, SessionName: "after-unsub"})

	// Give some time for event to be processed (it shouldn't reach the channel).
	time.Sleep(100 * time.Millisecond)

	assert.Len(t, ch, 0, "channel should be empty after unsubscribe")
}

// CT-7: Topology event delivered within 1s of F1 event.
func TestCT7_EventDeliveredWithin1Second(t *testing.T) {
	stub := newStub(nil, nil)
	reg, _ := startRegistry(t, stub)

	ch := make(chan TopologyEvent, 4)
	reg.Subscribe(ch)

	start := time.Now()
	stub.send(ControlModeEvent{Type: EventSessionCreated, SessionName: "timing-session"})

	require.Eventually(t, func() bool {
		select {
		case ev := <-ch:
			return ev.Type == TopologySessionCreated
		default:
			return false
		}
	}, 1*time.Second, 5*time.Millisecond)

	assert.Less(t, time.Since(start), 1*time.Second)
}

// CT-8: Concurrent Snapshot() calls + events — no data race (go test -race).
func TestCT8_ConcurrentSnapshotNoDataRace(t *testing.T) {
	stub := newStub(
		[]Session{{Name: "concurrent", ID: "c1"}},
		map[string][]Pane{"concurrent": {{ID: "pc1", Window: "wc1"}}},
	)
	reg, _ := startRegistry(t, stub)

	var wg sync.WaitGroup
	// 10 concurrent Snapshot readers.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = reg.Snapshot()
			}
		}()
	}

	// Inject events concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			stub.send(ControlModeEvent{Type: EventSessionCreated, SessionName: "race-session"})
			stub.send(ControlModeEvent{Type: EventSessionClosed, SessionName: "race-session"})
		}
	}()

	wg.Wait()
}

// ─── Behavioral Tests ──────────────────────────────────────────────────────────

// BT-1: Empty registry when F1 returns empty lists on Start.
func TestBT1_EmptyRegistryOnEmptyStart(t *testing.T) {
	stub := newStub(nil, nil)
	reg, _ := startRegistry(t, stub)

	sessions := reg.Snapshot()
	assert.NotNil(t, sessions, "Snapshot() must never return nil")
	assert.Len(t, sessions, 0)
}

// BT-2: Multiple subscribers all receive the same event.
func TestBT2_MultipleSubscribersReceiveEvent(t *testing.T) {
	stub := newStub(nil, nil)
	reg, _ := startRegistry(t, stub)

	ch1 := make(chan TopologyEvent, 4)
	ch2 := make(chan TopologyEvent, 4)
	ch3 := make(chan TopologyEvent, 4)
	reg.Subscribe(ch1)
	reg.Subscribe(ch2)
	reg.Subscribe(ch3)

	stub.send(ControlModeEvent{Type: EventSessionCreated, SessionName: "broadcast"})

	for _, ch := range []chan TopologyEvent{ch1, ch2, ch3} {
		ch := ch
		require.Eventually(t, func() bool {
			select {
			case ev := <-ch:
				return ev.Type == TopologySessionCreated && ev.Session != nil && ev.Session.Name == "broadcast"
			default:
				return false
			}
		}, 1*time.Second, 10*time.Millisecond, "subscriber did not receive event")
	}
}

// BT-3: Registry survives F1 reconnect (re-queries ListSessions/ListPanes
// when Events() channel closes and reopens).
func TestBT3_RegistrySurvivesReconnect(t *testing.T) {
	stub := &reconnectStub{
		sessions: []Session{{Name: "before", ID: "b1"}},
		panes:    map[string][]Pane{},
		eventCh:  make(chan ControlModeEvent, 8),
	}
	ctx, cancel := context.WithCancel(context.Background())
	reg := New(stub)
	require.NoError(t, reg.Start(ctx))
	t.Cleanup(func() {
		cancel()
		reg.Stop()
	})

	// Verify initial sessions present.
	require.Eventually(t, func() bool {
		for _, s := range reg.Snapshot() {
			if s.Name == "before" {
				return true
			}
		}
		return false
	}, 1*time.Second, 10*time.Millisecond)

	// Simulate F1 disconnect: close the old channel.
	stub.mu.Lock()
	close(stub.eventCh)
	// Replace sessions and reopen channel to simulate reconnect.
	stub.sessions = []Session{{Name: "after", ID: "a1"}}
	stub.eventCh = make(chan ControlModeEvent, 8)
	stub.mu.Unlock()

	// After reconnect, "after" should appear within 2s.
	require.Eventually(t, func() bool {
		for _, s := range reg.Snapshot() {
			if s.Name == "after" {
				return true
			}
		}
		return false
	}, 2*time.Second, 20*time.Millisecond)
}

// reconnectStub replaces its eventCh so the registry can re-subscribe.
type reconnectStub struct {
	mu       sync.Mutex
	sessions []Session
	panes    map[string][]Pane
	eventCh  chan ControlModeEvent
}

func (s *reconnectStub) Events() <-chan ControlModeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventCh
}

func (s *reconnectStub) ListSessions(_ context.Context) ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Session, len(s.sessions))
	copy(out, s.sessions)
	return out, nil
}

func (s *reconnectStub) ListPanes(_ context.Context, name string) ([]Pane, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.panes[name], nil
}

// ─── Regression Guards ────────────────────────────────────────────────────────

// RG-1: EventPaneExited for unknown pane ID — no panic, returns without error.
func TestRG1_UnknownPaneExitNoPanic(t *testing.T) {
	stub := newStub(
		[]Session{{Name: "s1", ID: "1"}},
		nil,
	)
	reg, _ := startRegistry(t, stub)

	// Wait for initial population.
	require.Eventually(t, func() bool {
		return len(reg.Snapshot()) > 0
	}, 1*time.Second, 10*time.Millisecond)

	// Send pane-exited for a pane that doesn't exist — must not panic.
	require.NotPanics(t, func() {
		stub.send(ControlModeEvent{Type: EventPaneExited, SessionName: "s1", PaneID: "nonexistent"})
		time.Sleep(50 * time.Millisecond) // allow processing
	})
}
