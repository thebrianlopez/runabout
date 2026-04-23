package pane

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blo-grindr/bmux/internal/ssh"
)

// --- Mocks ---

type mockBridge struct {
	mu         sync.Mutex
	ensureCalls []panePair
	removeCalls []panePair
	resizeCalls []resizeCall
}

type panePair struct{ host, paneID string }
type resizeCall struct {
	host, paneID    string
	rows, cols      int
}

func (b *mockBridge) EnsureSession(name string) error                      { return nil }
func (b *mockBridge) RemoveSession(name string) error                      { return nil }
func (b *mockBridge) ApplyOutput(name string, data []byte) error           { return nil }
func (b *mockBridge) SocketPath() string                                   { return "" }

func (b *mockBridge) EnsurePane(host, paneID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureCalls = append(b.ensureCalls, panePair{host, paneID})
	return nil
}

func (b *mockBridge) RemovePane(host, paneID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removeCalls = append(b.removeCalls, panePair{host, paneID})
	return nil
}

func (b *mockBridge) ResizePane(host, paneID string, rows, cols int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resizeCalls = append(b.resizeCalls, resizeCall{host, paneID, rows, cols})
	return nil
}

type mockSession struct {
	mu           sync.Mutex
	inputLog     [][]byte
	disconnected chan struct{}
}

func newMockSession() *mockSession {
	return &mockSession{disconnected: make(chan struct{})}
}

func (m *mockSession) Host() string                           { return "dev" }
func (m *mockSession) Status() ssh.SessionStatus              { return ssh.StatusConnected }
func (m *mockSession) Disconnected() <-chan struct{}           { return m.disconnected }
func (m *mockSession) Events() <-chan ssh.PaneEvent            { return nil }
func (m *mockSession) Close() error                           { return nil }
func (m *mockSession) SendInput(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.inputLog = append(m.inputLog, cp)
	return nil
}

func (m *mockSession) lastInput() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.inputLog) == 0 {
		return ""
	}
	return string(m.inputLog[len(m.inputLog)-1])
}

// --- Contract Tests ---

// CT-1: EventDispatcher routes PaneOutput to Output() only.
func TestDispatcher_RoutesPaneOutput(t *testing.T) {
	d := NewEventDispatcher()
	src := make(chan ssh.PaneEvent, 2)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, src) }()

	src <- ssh.PaneEvent{Type: ssh.PaneOutput, Host: "dev", Data: []byte("hello")}
	close(src)

	<-done // wait for Run to return

	// Output channel should have 1 event.
	out := drain(d.Output())
	assert.Len(t, out, 1)
	assert.Equal(t, ssh.PaneOutput, out[0].Type)

	// Lifecycle channel should be empty.
	assert.Empty(t, drain(d.Lifecycle()))
	cancel()
}

// CT-2: EventDispatcher routes PaneCreated to Lifecycle() only.
func TestDispatcher_RoutesPaneCreated(t *testing.T) {
	d := NewEventDispatcher()
	src := make(chan ssh.PaneEvent, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go d.Run(ctx, src) //nolint:errcheck

	src <- ssh.PaneEvent{Type: ssh.PaneCreated, Host: "dev", PaneID: "1"}
	close(src)

	time.Sleep(50 * time.Millisecond)
	life := drain(d.Lifecycle())
	assert.Len(t, life, 1)
	assert.Equal(t, ssh.PaneCreated, life[0].Type)
}

// CT-3: EventDispatcher routes PaneClosed to Lifecycle().
func TestDispatcher_RoutesPaneClosed(t *testing.T) {
	d := NewEventDispatcher()
	src := make(chan ssh.PaneEvent, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go d.Run(ctx, src) //nolint:errcheck

	src <- ssh.PaneEvent{Type: ssh.PaneClosed, Host: "dev", PaneID: "2"}
	close(src)

	time.Sleep(50 * time.Millisecond)
	life := drain(d.Lifecycle())
	require.Len(t, life, 1)
	assert.Equal(t, ssh.PaneClosed, life[0].Type)
}

// CT-4: LifecycleSyncer.Apply calls EnsurePane on PaneCreated.
func TestSyncer_Apply_PaneCreated(t *testing.T) {
	br := &mockBridge{}
	sess := newMockSession()
	s := NewLifecycleSyncer("dev", sess, br)

	require.NoError(t, s.Apply(context.Background(), ssh.PaneEvent{
		Type: ssh.PaneCreated, Host: "dev", PaneID: "p1",
	}))

	br.mu.Lock()
	calls := br.ensureCalls
	br.mu.Unlock()
	require.Len(t, calls, 1)
	assert.Equal(t, "dev", calls[0].host)
	assert.Equal(t, "p1", calls[0].paneID)
}

// CT-5: LifecycleSyncer.Apply calls RemovePane on PaneClosed.
func TestSyncer_Apply_PaneClosed(t *testing.T) {
	br := &mockBridge{}
	sess := newMockSession()
	s := NewLifecycleSyncer("dev", sess, br)

	require.NoError(t, s.Apply(context.Background(), ssh.PaneEvent{
		Type: ssh.PaneClosed, Host: "dev", PaneID: "p1",
	}))

	br.mu.Lock()
	calls := br.removeCalls
	br.mu.Unlock()
	require.Len(t, calls, 1)
	assert.Equal(t, "p1", calls[0].paneID)
}

// CT-6: LifecycleSyncer.Apply calls ResizePane on PaneResized.
func TestSyncer_Apply_PaneResized(t *testing.T) {
	br := &mockBridge{}
	sess := newMockSession()
	s := NewLifecycleSyncer("dev", sess, br)

	require.NoError(t, s.Apply(context.Background(), ssh.PaneEvent{
		Type: ssh.PaneResized, Host: "dev", PaneID: "p1", Rows: 40, Cols: 120,
	}))

	br.mu.Lock()
	calls := br.resizeCalls
	br.mu.Unlock()
	require.Len(t, calls, 1)
	assert.Equal(t, 40, calls[0].rows)
	assert.Equal(t, 120, calls[0].cols)
}

// CT-7: PropagateResize sends correct tmux resize command.
func TestSyncer_PropagateResize(t *testing.T) {
	br := &mockBridge{}
	sess := newMockSession()
	s := NewLifecycleSyncer("dev", sess, br)

	require.NoError(t, s.PropagateResize(context.Background(), 40, 120))

	input := sess.lastInput()
	assert.Contains(t, input, "resize-pane")
	assert.Contains(t, input, "-t dev")
	assert.Contains(t, input, "-x 120")
	assert.Contains(t, input, "-y 40")
}

// CT-8: Apply is idempotent — duplicate PaneCreated is safe.
func TestSyncer_Apply_Idempotent(t *testing.T) {
	br := &mockBridge{}
	sess := newMockSession()
	s := NewLifecycleSyncer("dev", sess, br)

	ev := ssh.PaneEvent{Type: ssh.PaneCreated, Host: "dev", PaneID: "p1"}
	require.NoError(t, s.Apply(context.Background(), ev))
	require.NoError(t, s.Apply(context.Background(), ev)) // second call: no error

	br.mu.Lock()
	assert.Len(t, br.ensureCalls, 2, "EnsurePane called twice (both safe)")
	br.mu.Unlock()
}

// CT-9: EventDispatcher exits cleanly (closes sub-channels) when src closes.
func TestDispatcher_ExitsOnSrcClose(t *testing.T) {
	d := NewEventDispatcher()
	src := make(chan ssh.PaneEvent)
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, src) }()

	close(src)

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after src closed")
	}

	// Both channels must be closed.
	assertChanClosed(t, d.Output(), "Output channel must close")
	assertChanClosed(t, d.Lifecycle(), "Lifecycle channel must close")
}

// --- Regression Guards ---

// RG-1: Duplicate PaneCreated for same ID does not cause extra local panes.
// (EnsurePane is already idempotent in the bridge — test that Apply calls it correctly.)
func TestRG1_DuplicatePaneCreated(t *testing.T) {
	br := &mockBridge{}
	sess := newMockSession()
	s := NewLifecycleSyncer("dev", sess, br)

	ev := ssh.PaneEvent{Type: ssh.PaneCreated, Host: "dev", PaneID: "p1"}
	require.NoError(t, s.Apply(context.Background(), ev))
	require.NoError(t, s.Apply(context.Background(), ev))

	// No error returned — idempotency guaranteed by bridge.EnsurePane.
	br.mu.Lock()
	assert.Len(t, br.ensureCalls, 2)
	br.mu.Unlock()
}

// RG-2: PaneClosed for unknown pane must not error.
func TestRG2_PaneClosedUnknown(t *testing.T) {
	br := &mockBridge{}
	sess := newMockSession()
	s := NewLifecycleSyncer("dev", sess, br)

	// PaneClosed for a pane that was never created.
	err := s.Apply(context.Background(), ssh.PaneEvent{
		Type: ssh.PaneClosed, Host: "dev", PaneID: "unknown",
	})
	assert.NoError(t, err)
}

// --- Helpers ---

func drain[T any](ch <-chan T) []T {
	var out []T
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, v)
		default:
			return out
		}
	}
}

func assertChanClosed[T any](t *testing.T, ch <-chan T, msg string) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("%s: channel not closed (received value)", msg)
		}
	default:
		t.Fatalf("%s: channel not closed (no value available)", msg)
	}
}
