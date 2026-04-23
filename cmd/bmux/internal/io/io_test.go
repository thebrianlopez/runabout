package io

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blo-grindr/bmux/internal/ssh"
)

// --- Mocks ---

// mockSession implements ssh.Session with injectable behavior.
type mockSession struct {
	hostName     string
	sendInputErr error
	sendInputLog [][]byte
	events       chan ssh.PaneEvent
	disconnected chan struct{}
	status       ssh.SessionStatus
	mu           sync.Mutex
}

func newMockSession(host string) *mockSession {
	return &mockSession{
		hostName:     host,
		events:       make(chan ssh.PaneEvent, 16),
		disconnected: make(chan struct{}),
		status:       ssh.StatusConnected,
	}
}

func (m *mockSession) Host() string { return m.hostName }

func (m *mockSession) Status() ssh.SessionStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *mockSession) Disconnected() <-chan struct{} { return m.disconnected }

func (m *mockSession) SendInput(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sendInputErr != nil {
		return m.sendInputErr
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	m.sendInputLog = append(m.sendInputLog, cp)
	return nil
}

func (m *mockSession) Events() <-chan ssh.PaneEvent { return m.events }

func (m *mockSession) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = ssh.StatusDisconnected
	close(m.disconnected)
	return nil
}

func (m *mockSession) lastInput() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sendInputLog) == 0 {
		return nil
	}
	return m.sendInputLog[len(m.sendInputLog)-1]
}

func (m *mockSession) inputCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sendInputLog)
}

// mockBridge implements bridge.LocalTmuxBridge with recorded calls.
type mockBridge struct {
	applyErr    error
	applyLog    []applyCall
	mu          sync.Mutex
}

type applyCall struct {
	name string
	data []byte
}

func (b *mockBridge) EnsureSession(name string) error                       { return nil }
func (b *mockBridge) RemoveSession(name string) error                       { return nil }
func (b *mockBridge) EnsurePane(host, paneID string) error                  { return nil }
func (b *mockBridge) RemovePane(host, paneID string) error                  { return nil }
func (b *mockBridge) ResizePane(host, paneID string, rows, cols int) error  { return nil }
func (b *mockBridge) SocketPath() string                                    { return "/tmp/test" }

func (b *mockBridge) ApplyOutput(name string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.applyErr != nil {
		return b.applyErr
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	b.applyLog = append(b.applyLog, applyCall{name: name, data: cp})
	return nil
}

func (b *mockBridge) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.applyLog)
}

func (b *mockBridge) callAt(i int) applyCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.applyLog[i]
}

// --- Contract Tests ---

// CT-1: ForwardInput calls Session.SendInput with hex-escaped bytes.
func TestForwardInput_HexEscaped(t *testing.T) {
	sess := newMockSession("dev")
	br := &mockBridge{}
	iob := NewIOBridge(sess, br)

	err := iob.ForwardInput(context.Background(), []byte{0x41, 0x0a})
	require.NoError(t, err)

	assert.Equal(t, []byte("0x41 0x0a"), sess.lastInput())
}

// CT-2: ForwardInput returns ErrSessionClosed on closed session.
func TestForwardInput_SessionClosed(t *testing.T) {
	sess := newMockSession("dev")
	sess.sendInputErr = ssh.ErrSessionClosed
	br := &mockBridge{}
	iob := NewIOBridge(sess, br)

	err := iob.ForwardInput(context.Background(), []byte{0x41})
	require.Error(t, err)
	assert.ErrorIs(t, err, ssh.ErrSessionClosed)
}

// CT-3: StreamOutput calls ApplyOutput for each PaneOutput event.
func TestStreamOutput_AppliesOutputEvents(t *testing.T) {
	sess := newMockSession("dev")
	br := &mockBridge{}
	iob := NewIOBridge(sess, br)

	// Send 3 output events then close.
	sess.events <- ssh.PaneEvent{Type: ssh.PaneOutput, Host: "dev", Data: []byte("hello")}
	sess.events <- ssh.PaneEvent{Type: ssh.PaneOutput, Host: "dev", Data: []byte("world")}
	sess.events <- ssh.PaneEvent{Type: ssh.PaneOutput, Host: "dev", Data: []byte("!")}
	close(sess.events)

	err := iob.StreamOutput(context.Background())
	require.NoError(t, err)

	require.Equal(t, 3, br.callCount())
	assert.Equal(t, []byte("hello"), br.callAt(0).data)
	assert.Equal(t, []byte("world"), br.callAt(1).data)
	assert.Equal(t, []byte("!"), br.callAt(2).data)
}

// CT-4: StreamOutput exits cleanly (nil) when events channel closes.
func TestStreamOutput_ExitsOnChannelClose(t *testing.T) {
	sess := newMockSession("dev")
	br := &mockBridge{}
	iob := NewIOBridge(sess, br)

	close(sess.events)
	err := iob.StreamOutput(context.Background())
	assert.NoError(t, err)
}

// CT-5: StreamOutput skips non-output events and continues.
func TestStreamOutput_SkipsNonOutputEvents(t *testing.T) {
	sess := newMockSession("dev")
	br := &mockBridge{}
	iob := NewIOBridge(sess, br)

	// Mix of event types.
	sess.events <- ssh.PaneEvent{Type: ssh.PaneCreated, Host: "dev"}
	sess.events <- ssh.PaneEvent{Type: ssh.PaneOutput, Host: "dev", Data: []byte("data")}
	sess.events <- ssh.PaneEvent{Type: ssh.PaneClosed, Host: "dev"}
	close(sess.events)

	require.NoError(t, iob.StreamOutput(context.Background()))
	assert.Equal(t, 1, br.callCount(), "only PaneOutput events should trigger ApplyOutput")
}

// CT-6: Binary-safe: NUL byte passes through ForwardInput as "0x00".
func TestForwardInput_NULByte(t *testing.T) {
	sess := newMockSession("dev")
	br := &mockBridge{}
	iob := NewIOBridge(sess, br)

	require.NoError(t, iob.ForwardInput(context.Background(), []byte{0x00}))
	assert.Equal(t, []byte("0x00"), sess.lastInput())
}

// CT-7: Binary-safe: arbitrary bytes pass through StreamOutput unchanged.
func TestStreamOutput_BinarySafe(t *testing.T) {
	sess := newMockSession("dev")
	br := &mockBridge{}
	iob := NewIOBridge(sess, br)

	// Build a 256-byte payload with all byte values.
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}

	sess.events <- ssh.PaneEvent{Type: ssh.PaneOutput, Host: "dev", Data: payload}
	close(sess.events)

	require.NoError(t, iob.StreamOutput(context.Background()))
	require.Equal(t, 1, br.callCount())
	assert.Equal(t, payload, br.callAt(0).data)
}

// CT-8: ForwardInput returns ctx.Err() on cancelled context.
func TestForwardInput_ContextCancelled(t *testing.T) {
	sess := newMockSession("dev")
	br := &mockBridge{}
	iob := NewIOBridge(sess, br)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := iob.ForwardInput(ctx, []byte{0x41})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, sess.inputCount(), "SendInput must not be called on cancelled ctx")
}

// --- Behavioral Tests ---

// BT-3: Concurrent ForwardInput calls are safe.
func TestForwardInput_Concurrent(t *testing.T) {
	sess := newMockSession("dev")
	br := &mockBridge{}
	iob := NewIOBridge(sess, br)

	const n = 10
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			errCh <- iob.ForwardInput(context.Background(), []byte{0x41})
		}()
	}
	for i := 0; i < n; i++ {
		require.NoError(t, <-errCh)
	}
}

// BT-4: StreamOutput handles ApplyOutput failure gracefully (E32).
func TestStreamOutput_ApplyOutputError(t *testing.T) {
	sess := newMockSession("dev")
	br := &mockBridge{applyErr: errors.New("local pane gone")}
	iob := NewIOBridge(sess, br)

	// Even with ApplyOutput failing, StreamOutput should continue processing.
	sess.events <- ssh.PaneEvent{Type: ssh.PaneOutput, Host: "dev", Data: []byte("data1")}
	sess.events <- ssh.PaneEvent{Type: ssh.PaneOutput, Host: "dev", Data: []byte("data2")}
	close(sess.events)

	// Should return nil (clean exit) even though ApplyOutput errors.
	err := iob.StreamOutput(context.Background())
	assert.NoError(t, err)
}

// RG-1: NUL byte in output does not terminate StreamOutput.
func TestStreamOutput_NULByteDoesNotTerminate(t *testing.T) {
	sess := newMockSession("dev")
	br := &mockBridge{}
	iob := NewIOBridge(sess, br)

	sess.events <- ssh.PaneEvent{Type: ssh.PaneOutput, Host: "dev", Data: []byte{0x00, 0x41}}
	sess.events <- ssh.PaneEvent{Type: ssh.PaneOutput, Host: "dev", Data: []byte("after nul")}
	close(sess.events)

	require.NoError(t, iob.StreamOutput(context.Background()))
	require.Equal(t, 2, br.callCount())
	assert.Equal(t, []byte{0x00, 0x41}, br.callAt(0).data)
}

// RG-2: Multi-byte UTF-8 sequence passes through as a single unit.
func TestStreamOutput_UTF8Preserved(t *testing.T) {
	sess := newMockSession("dev")
	br := &mockBridge{}
	iob := NewIOBridge(sess, br)

	// "é" is 0xC3 0xA9 (2-byte UTF-8)
	utf8 := []byte{0xC3, 0xA9}
	sess.events <- ssh.PaneEvent{Type: ssh.PaneOutput, Host: "dev", Data: utf8}
	close(sess.events)

	require.NoError(t, iob.StreamOutput(context.Background()))
	require.Equal(t, 1, br.callCount())
	assert.Equal(t, utf8, br.callAt(0).data)
}

// --- Helper ---

func waitChan(ch <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}
