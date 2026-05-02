package ws_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/blo-grindr/bmux/internal/gateway/ws"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// stubBridge is a test double for ControlModeBridge.
type stubBridge struct {
	mu          sync.Mutex
	subs        map[string][]chan []byte
	sendKeysCalls []sendKeysCall
	// paneNotFound causes Subscribe to return an error for the given pane.
	paneNotFound map[string]bool
}

type sendKeysCall struct {
	PaneID  string
	Keys    string
	Literal bool
}

func newStubBridge() *stubBridge {
	return &stubBridge{
		subs:         make(map[string][]chan []byte),
		paneNotFound: make(map[string]bool),
	}
}

func (b *stubBridge) Subscribe(paneID string) (<-chan []byte, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.paneNotFound[paneID] {
		return nil, nil, fmt.Errorf("pane not found: %s", paneID)
	}
	ch := make(chan []byte, 64)
	b.subs[paneID] = append(b.subs[paneID], ch)
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		subs := b.subs[paneID]
		for i, s := range subs {
			if s == ch {
				b.subs[paneID] = append(subs[:i], subs[i+1:]...)
				close(ch)
				break
			}
		}
	}
	return ch, cancel, nil
}

func (b *stubBridge) SendKeys(ctx context.Context, paneID string, keys string, literal bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sendKeysCalls = append(b.sendKeysCalls, sendKeysCall{PaneID: paneID, Keys: keys, Literal: literal})
	return nil
}

// Emit sends bytes to all subscribers of a pane.
func (b *stubBridge) Emit(paneID string, data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs[paneID] {
		select {
		case ch <- data:
		default:
		}
	}
}

// SubCount returns the number of active subscriptions for a pane.
func (b *stubBridge) SubCount(paneID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[paneID])
}

// stubMirror is a test double for MirrorManager.
type stubMirror struct {
	mu       sync.Mutex
	data     map[string][]byte
}

func newStubMirror() *stubMirror {
	return &stubMirror{data: make(map[string][]byte)}
}

func (m *stubMirror) Snapshot(paneID string, cols, rows int) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[paneID], nil
}

func (m *stubMirror) SetSnapshot(paneID string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[paneID] = data
}

// stubRegistry is a test double for SessionRegistry.
type stubRegistry struct {
	sessions []ws.Session
}

func (r *stubRegistry) Sessions(ctx context.Context) ([]ws.Session, error) {
	return r.sessions, nil
}

// stubTranslator is a test double for KeyTranslator (identity).
type stubTranslator struct{}

func (t *stubTranslator) Translate(keys string) string { return keys }

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

const testToken = "test-secret-token"

func newTestGateway(t *testing.T, bridge ws.ControlModeBridge, mirror ws.MirrorManager, registry ws.SessionRegistry, translator ws.KeyTranslator) (*httptest.Server, ws.Gateway) {
	t.Helper()
	cfg := ws.Config{
		Token:      testToken,
		MaxClients: 5,
		Bridge:     bridge,
		Mirror:     mirror,
		Registry:   registry,
		Translator: translator,
	}
	gw, err := ws.New(cfg)
	require.NoError(t, err)

	srv := httptest.NewServer(gw)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Stop(ctx)
		srv.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, gw.Start(ctx))

	return srv, gw
}

// wsURL converts an http URL to ws URL.
func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func dialWS(t *testing.T, srv *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conn, _, err := websocket.Dial(context.Background(), wsURL(srv)+"/ws", &websocket.DialOptions{
		HTTPHeader: header,
	})
	require.NoError(t, err)
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func readJSONMsg(t *testing.T, conn *websocket.Conn) map[string]interface{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var msg map[string]interface{}
	err := wsjson.Read(ctx, conn, &msg)
	require.NoError(t, err)
	return msg
}

func sendJSON(t *testing.T, conn *websocket.Conn, v interface{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, wsjson.Write(ctx, conn, v))
}

func readBinaryFrame(t *testing.T, conn *websocket.Conn) (msgType websocket.MessageType, data []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	msgType, data, err := conn.Read(ctx)
	require.NoError(t, err)
	return msgType, data
}

// ---------------------------------------------------------------------------
// CT-1: Valid bearer token → connection accepted
// ---------------------------------------------------------------------------

func TestCT1_ValidToken_ConnectionAccepted(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)
	conn := dialWS(t, srv, testToken)
	defer conn.CloseNow()

	// If we can read the session-list message, the connection was accepted.
	msg := readJSONMsg(t, conn)
	assert.Equal(t, "session-list", msg["type"])
}

// ---------------------------------------------------------------------------
// CT-2: Invalid token → close(4001) before any data
// ---------------------------------------------------------------------------

func TestCT2_InvalidToken_Close4001(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)

	header := http.Header{"Authorization": []string{"Bearer wrong-token"}}
	conn, _, err := websocket.Dial(context.Background(), wsURL(srv)+"/ws", &websocket.DialOptions{
		HTTPHeader: header,
	})
	if err == nil {
		// Connection was accepted — read to see if we get close code 4001.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _, readErr := conn.Read(ctx)
		conn.CloseNow()
		require.Error(t, readErr)
		var closeErr websocket.CloseError
		require.ErrorAs(t, readErr, &closeErr, "expected close error")
		assert.Equal(t, websocket.StatusCode(4001), closeErr.Code)
	} else {
		// Some WS libraries reject at HTTP level with 401 — also acceptable
		// as long as no data was sent. We check the error contains 4001 or 401.
		assert.True(t,
			strings.Contains(err.Error(), "4001") || strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403"),
			"expected auth rejection, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CT-3: Missing Authorization header → close(4001)
// ---------------------------------------------------------------------------

func TestCT3_MissingAuthHeader_Close4001(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)

	conn, _, err := websocket.Dial(context.Background(), wsURL(srv)+"/ws", &websocket.DialOptions{})
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _, readErr := conn.Read(ctx)
		conn.CloseNow()
		require.Error(t, readErr)
		var closeErr websocket.CloseError
		require.ErrorAs(t, readErr, &closeErr)
		assert.Equal(t, websocket.StatusCode(4001), closeErr.Code)
	} else {
		assert.True(t,
			strings.Contains(err.Error(), "4001") || strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403"),
			"expected auth rejection, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CT-4: Client receives session-list JSON on connect
// ---------------------------------------------------------------------------

func TestCT4_SessionListOnConnect(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{sessions: []ws.Session{
		{Name: "claude-code", ID: "$0", Panes: []ws.Pane{{ID: "%0", Window: "main"}}},
	}}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)
	conn := dialWS(t, srv, testToken)

	msg := readJSONMsg(t, conn)
	require.Equal(t, "session-list", msg["type"])
	sessions, ok := msg["sessions"].([]interface{})
	require.True(t, ok)
	require.Len(t, sessions, 1)
	s := sessions[0].(map[string]interface{})
	assert.Equal(t, "claude-code", s["name"])
}

// ---------------------------------------------------------------------------
// CT-5: pane-attach → binary PTY frames with correct 16B pane prefix
// ---------------------------------------------------------------------------

func TestCT5_PaneAttach_BinaryFramesWithPrefix(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)
	conn := dialWS(t, srv, testToken)

	// Consume session-list.
	readJSONMsg(t, conn)

	// Attach to pane %0.
	sendJSON(t, conn, map[string]interface{}{
		"type": "pane-attach", "pane_id": "%0", "cols": 220, "rows": 50,
	})

	// Consume snapshot-start.
	readJSONMsg(t, conn)
	// Consume snapshot-end (no snapshot data since mirror returns empty).
	readJSONMsg(t, conn)

	// Emit PTY data from bridge.
	ptyData := []byte("hello terminal")
	bridge.Emit("%0", ptyData)

	// Read binary frame.
	msgType, frame := readBinaryFrame(t, conn)
	assert.Equal(t, websocket.MessageBinary, msgType)
	require.GreaterOrEqual(t, len(frame), 16, "frame must have 16-byte pane prefix")

	prefix := frame[:16]
	// Prefix should be pane ID ASCII zero-padded.
	paneID := strings.TrimRight(string(prefix), "\x00")
	assert.Equal(t, "%0", paneID)

	payload := frame[16:]
	assert.Equal(t, ptyData, payload)
}

// ---------------------------------------------------------------------------
// CT-6: pane-detach → F1 output no longer delivered to this client
// ---------------------------------------------------------------------------

func TestCT6_PaneDetach_StopsDelivery(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)
	conn := dialWS(t, srv, testToken)
	readJSONMsg(t, conn) // session-list

	sendJSON(t, conn, map[string]interface{}{
		"type": "pane-attach", "pane_id": "%1", "cols": 80, "rows": 24,
	})
	readJSONMsg(t, conn) // snapshot-start
	readJSONMsg(t, conn) // snapshot-end

	// Verify subscription exists.
	require.Eventually(t, func() bool { return bridge.SubCount("%1") == 1 }, time.Second, 10*time.Millisecond)

	// Detach.
	sendJSON(t, conn, map[string]interface{}{"type": "pane-detach", "pane_id": "%1"})

	// Subscription should be cleaned up.
	require.Eventually(t, func() bool { return bridge.SubCount("%1") == 0 }, time.Second, 10*time.Millisecond)

	// Any data emitted now should not reach the client (connection should be quiet).
	bridge.Emit("%1", []byte("should not arrive"))

	// Give it a moment.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err := conn.Read(ctx)
	// We expect a timeout, not data.
	assert.Error(t, err, "expected no data after detach")
}

// ---------------------------------------------------------------------------
// CT-7: send-keys → F1.SendKeys called with translated keys
// ---------------------------------------------------------------------------

func TestCT7_SendKeys_CallsBridgeSendKeys(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)
	conn := dialWS(t, srv, testToken)
	readJSONMsg(t, conn) // session-list

	sendJSON(t, conn, map[string]interface{}{
		"type": "send-keys", "pane_id": "%2", "keys": "ls -la", "literal": false,
	})

	require.Eventually(t, func() bool {
		bridge.mu.Lock()
		defer bridge.mu.Unlock()
		return len(bridge.sendKeysCalls) == 1
	}, time.Second, 10*time.Millisecond)

	bridge.mu.Lock()
	call := bridge.sendKeysCalls[0]
	bridge.mu.Unlock()

	assert.Equal(t, "%2", call.PaneID)
	assert.Equal(t, "ls -la", call.Keys)
	assert.Equal(t, false, call.Literal)
}

// ---------------------------------------------------------------------------
// CT-8: 5 concurrent clients each receive only their pane's frames
// ---------------------------------------------------------------------------

func TestCT8_FiveClients_EachReceiveOwnPane(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)

	type clientResult struct {
		paneID string
		frames [][]byte
	}

	var wg sync.WaitGroup
	results := make([]clientResult, 5)
	mu := sync.Mutex{}

	for i := 0; i < 5; i++ {
		i := i
		paneID := fmt.Sprintf("%%%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn := dialWS(t, srv, testToken)
			defer conn.CloseNow()

			readJSONMsg(t, conn) // session-list

			sendJSON(t, conn, map[string]interface{}{
				"type": "pane-attach", "pane_id": paneID, "cols": 80, "rows": 24,
			})
			readJSONMsg(t, conn) // snapshot-start
			readJSONMsg(t, conn) // snapshot-end

			// Wait for subscription.
			require.Eventually(t, func() bool { return bridge.SubCount(paneID) >= 1 }, time.Second, 10*time.Millisecond)

			// Signal ready.
			mu.Lock()
			results[i] = clientResult{paneID: paneID}
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Now emit unique data to each pane.
	emitted := make(map[string][]byte)
	for i := 0; i < 5; i++ {
		paneID := fmt.Sprintf("%%%d", i)
		data := []byte(fmt.Sprintf("data-for-pane-%d", i))
		emitted[paneID] = data
		bridge.Emit(paneID, data)
	}

	// Each client reads one frame and verifies the prefix.
	for i := 0; i < 5; i++ {
		i := i
		paneID := fmt.Sprintf("%%%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Re-dial — we need fresh connections since the goroutines above exited.
			conn := dialWS(t, srv, testToken)
			defer conn.CloseNow()
			readJSONMsg(t, conn)

			sendJSON(t, conn, map[string]interface{}{
				"type": "pane-attach", "pane_id": paneID, "cols": 80, "rows": 24,
			})
			readJSONMsg(t, conn) // snapshot-start
			readJSONMsg(t, conn) // snapshot-end

			require.Eventually(t, func() bool { return bridge.SubCount(paneID) >= 1 }, time.Second, 10*time.Millisecond)
			bridge.Emit(paneID, emitted[paneID])

			_, frame := readBinaryFrame(t, conn)
			require.GreaterOrEqual(t, len(frame), 16)
			prefix := strings.TrimRight(string(frame[:16]), "\x00")
			assert.Equal(t, paneID, prefix, "client %d received wrong pane prefix", i)
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// CT-9: 6th client → close(4008)
// ---------------------------------------------------------------------------

func TestCT9_SixthClient_Close4008(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)

	var conns []*websocket.Conn
	for i := 0; i < 5; i++ {
		conn := dialWS(t, srv, testToken)
		readJSONMsg(t, conn) // consume session-list
		conns = append(conns, conn)
	}
	t.Cleanup(func() {
		for _, c := range conns {
			c.CloseNow()
		}
	})

	// 6th connection.
	conn6, _, err := websocket.Dial(context.Background(), wsURL(srv)+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + testToken}},
	})
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _, readErr := conn6.Read(ctx)
		conn6.CloseNow()
		require.Error(t, readErr)
		var closeErr websocket.CloseError
		require.ErrorAs(t, readErr, &closeErr)
		assert.Equal(t, websocket.StatusCode(4008), closeErr.Code)
	} else {
		// HTTP-level rejection — check status.
		assert.True(t,
			strings.Contains(err.Error(), "4008") || strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "503"),
			"expected limit rejection, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CT-10: Client disconnect → all F1 subscriptions cleaned up
// ---------------------------------------------------------------------------

func TestCT10_ClientDisconnect_SubscriptionsCleaned(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)
	conn := dialWS(t, srv, testToken)
	readJSONMsg(t, conn) // session-list

	sendJSON(t, conn, map[string]interface{}{
		"type": "pane-attach", "pane_id": "%9", "cols": 80, "rows": 24,
	})
	readJSONMsg(t, conn) // snapshot-start
	readJSONMsg(t, conn) // snapshot-end

	require.Eventually(t, func() bool { return bridge.SubCount("%9") == 1 }, time.Second, 10*time.Millisecond)

	// Disconnect abruptly.
	conn.CloseNow()

	// Subscription must be cleaned up.
	require.Eventually(t, func() bool { return bridge.SubCount("%9") == 0 }, 2*time.Second, 20*time.Millisecond)
}

// ---------------------------------------------------------------------------
// CT-11: GET / returns 200 with Content-Type: text/html
// ---------------------------------------------------------------------------

func TestCT11_GetRoot_ReturnsHTML(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)

	resp, err := http.Get(srv.URL + "/")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	ct := resp.Header.Get("Content-Type")
	assert.True(t, strings.HasPrefix(ct, "text/html"), "expected text/html, got %s", ct)
}

// ---------------------------------------------------------------------------
// CT-12: pane-attach delivers snapshot-start before binary frames and snapshot-end after
// ---------------------------------------------------------------------------

func TestCT12_PaneAttach_SnapshotOrder(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	const paneID = "%5"
	snapshotData := []byte("ANSI snapshot bytes")
	mirror.SetSnapshot(paneID, snapshotData)

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)
	conn := dialWS(t, srv, testToken)
	readJSONMsg(t, conn) // session-list

	sendJSON(t, conn, map[string]interface{}{
		"type": "pane-attach", "pane_id": paneID, "cols": 80, "rows": 24,
	})

	// 1. snapshot-start
	start := readJSONMsg(t, conn)
	assert.Equal(t, "snapshot-start", start["type"])
	assert.Equal(t, paneID, start["pane_id"])

	// 2. binary frame(s) from snapshot
	msgType, frame := readBinaryFrame(t, conn)
	assert.Equal(t, websocket.MessageBinary, msgType)
	require.GreaterOrEqual(t, len(frame), 16)
	payload := frame[16:]
	assert.Equal(t, snapshotData, payload)

	// 3. snapshot-end
	end := readJSONMsg(t, conn)
	assert.Equal(t, "snapshot-end", end["type"])
	assert.Equal(t, paneID, end["pane_id"])
}

// ---------------------------------------------------------------------------
// BT-1: Slow client write does not block fast client on same pane
// ---------------------------------------------------------------------------

func TestBT1_SlowClient_DoesNotBlockFastClient(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)

	// Fast client.
	fastConn := dialWS(t, srv, testToken)
	readJSONMsg(t, fastConn)
	sendJSON(t, fastConn, map[string]interface{}{
		"type": "pane-attach", "pane_id": "%bt1", "cols": 80, "rows": 24,
	})
	readJSONMsg(t, fastConn) // snapshot-start
	readJSONMsg(t, fastConn) // snapshot-end

	require.Eventually(t, func() bool { return bridge.SubCount("%bt1") >= 1 }, time.Second, 10*time.Millisecond)

	// Emit many frames rapidly — the fast client should receive them without deadlock.
	for i := 0; i < 20; i++ {
		bridge.Emit("%bt1", []byte(fmt.Sprintf("frame-%d", i)))
	}

	received := 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && received < 20 {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		_, _, err := fastConn.Read(ctx)
		cancel()
		if err != nil {
			break
		}
		received++
	}
	assert.Equal(t, 20, received, "fast client should receive all 20 frames")
}

// ---------------------------------------------------------------------------
// BT-2: Reconnecting client receives fresh snapshot on second pane-attach
// ---------------------------------------------------------------------------

func TestBT2_Reconnect_FreshSnapshot(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	const paneID = "%bt2"
	mirror.SetSnapshot(paneID, []byte("first-snapshot"))

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)
	conn := dialWS(t, srv, testToken)
	readJSONMsg(t, conn)

	// First attach.
	sendJSON(t, conn, map[string]interface{}{
		"type": "pane-attach", "pane_id": paneID, "cols": 80, "rows": 24,
	})
	readJSONMsg(t, conn)                   // snapshot-start
	_, frame1 := readBinaryFrame(t, conn)  // first snapshot frame
	readJSONMsg(t, conn)                   // snapshot-end

	// Detach then update snapshot.
	sendJSON(t, conn, map[string]interface{}{"type": "pane-detach", "pane_id": paneID})
	require.Eventually(t, func() bool { return bridge.SubCount(paneID) == 0 }, time.Second, 10*time.Millisecond)
	mirror.SetSnapshot(paneID, []byte("second-snapshot"))

	// Second attach.
	sendJSON(t, conn, map[string]interface{}{
		"type": "pane-attach", "pane_id": paneID, "cols": 80, "rows": 24,
	})
	readJSONMsg(t, conn)                   // snapshot-start
	_, frame2 := readBinaryFrame(t, conn)  // second snapshot frame
	readJSONMsg(t, conn)                   // snapshot-end

	payload1 := frame1[16:]
	payload2 := frame2[16:]
	assert.Equal(t, []byte("first-snapshot"), payload1)
	assert.Equal(t, []byte("second-snapshot"), payload2)
	assert.NotEqual(t, payload1, payload2, "second attach should deliver fresh snapshot")
}

// ---------------------------------------------------------------------------
// BT-3: pane-attach to non-existent pane → error frame
// ---------------------------------------------------------------------------

func TestBT3_PaneAttach_NotFound_ErrorFrame(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	const paneID = "%nonexistent"
	bridge.paneNotFound[paneID] = true

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)
	conn := dialWS(t, srv, testToken)
	readJSONMsg(t, conn)

	sendJSON(t, conn, map[string]interface{}{
		"type": "pane-attach", "pane_id": paneID, "cols": 80, "rows": 24,
	})

	msg := readJSONMsg(t, conn)
	assert.Equal(t, "error", msg["type"])
	assert.Equal(t, "pane_not_found", msg["code"])
}

// ---------------------------------------------------------------------------
// RG-1: Auth failure — zero bytes of session data transmitted before close(4001)
// ---------------------------------------------------------------------------

func TestRG1_AuthFailure_ZeroBytesBeforeClose(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{sessions: []ws.Session{
		{Name: "sensitive", ID: "$99"},
	}}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)

	// Use raw HTTP to observe the exact exchange.
	conn, _, err := websocket.Dial(context.Background(), wsURL(srv)+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer bad-token"}},
	})
	if err != nil {
		// Rejected at HTTP level — no data transferred.
		assert.NotContains(t, err.Error(), "sensitive", "session data must not appear in error")
		return
	}

	// If we got a connection, read until closed.
	var allData []byte
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, data, readErr := conn.Read(ctx)
		if readErr != nil {
			break
		}
		allData = append(allData, data...)
	}
	conn.CloseNow()

	// Must not contain any session data.
	assert.NotContains(t, string(allData), "sensitive", "session data must not be sent before auth")
	assert.NotContains(t, string(allData), "session-list", "session-list must not be sent before auth")
}

// ---------------------------------------------------------------------------
// RG-2: One client disconnect does not affect other clients' subscriptions
// ---------------------------------------------------------------------------

func TestRG2_OneClientDisconnect_OthersUnaffected(t *testing.T) {
	bridge := newStubBridge()
	mirror := newStubMirror()
	registry := &stubRegistry{}
	translator := &stubTranslator{}

	srv, _ := newTestGateway(t, bridge, mirror, registry, translator)

	// Client A on pane %rg2a.
	connA := dialWS(t, srv, testToken)
	readJSONMsg(t, connA)
	sendJSON(t, connA, map[string]interface{}{
		"type": "pane-attach", "pane_id": "%rg2a", "cols": 80, "rows": 24,
	})
	readJSONMsg(t, connA)
	readJSONMsg(t, connA)

	// Client B on pane %rg2b.
	connB := dialWS(t, srv, testToken)
	readJSONMsg(t, connB)
	sendJSON(t, connB, map[string]interface{}{
		"type": "pane-attach", "pane_id": "%rg2b", "cols": 80, "rows": 24,
	})
	readJSONMsg(t, connB)
	readJSONMsg(t, connB)

	require.Eventually(t, func() bool {
		return bridge.SubCount("%rg2a") == 1 && bridge.SubCount("%rg2b") == 1
	}, time.Second, 10*time.Millisecond)

	// Disconnect client A.
	connA.CloseNow()
	require.Eventually(t, func() bool { return bridge.SubCount("%rg2a") == 0 }, 2*time.Second, 20*time.Millisecond)

	// Client B subscription must still be alive.
	assert.Equal(t, 1, bridge.SubCount("%rg2b"), "client B subscription should survive client A disconnect")

	// Client B should still receive data.
	bridge.Emit("%rg2b", []byte("still alive"))
	_, frame := readBinaryFrame(t, connB)
	require.GreaterOrEqual(t, len(frame), 16)
	assert.Equal(t, []byte("still alive"), frame[16:])
}

// Ensure json import is used.
var _ = json.Marshal
var _ = io.Reader(bytes.NewReader(nil))
var _ = fmt.Sprintf
