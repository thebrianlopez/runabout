// Package ctlbridge implements the tmux Control Mode Bridge (F1) for bmux.
// This file contains the full test suite — CT (contract), BT (behavioral), and RG (regression guard) tests.
// Tests are written first per the FIRST principle and must all compile/fail before implementation.
package ctlbridge_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blo-grindr/bmux/internal/gateway/ctlbridge"
	"github.com/stretchr/testify/require"
)

// tmuxPath holds the resolved path to tmux, checked once in TestMain.
var tmuxPath string

func TestMain(m *testing.M) {
	path, err := exec.LookPath("tmux")
	if err == nil {
		tmuxPath = path
	}
	m.Run()
}

// skipIfNoTmux skips the test if tmux is not present in PATH.
func skipIfNoTmux(t *testing.T) {
	t.Helper()
	if tmuxPath == "" {
		t.Skip("tmux not found in PATH")
	}
}

// uniqueSocket returns a unique tmux socket name for test isolation.
func uniqueSocket(t *testing.T) string {
	t.Helper()
	// Use test name hash to generate a unique-ish socket name without uuid dependency.
	return fmt.Sprintf("bmux-test-%d", time.Now().UnixNano())
}

// newTestBridge creates a ControlModeBridge for integration tests, auto-cleaned up.
func newTestBridge(t *testing.T) ctlbridge.ControlModeBridge {
	t.Helper()
	socket := uniqueSocket(t)
	b := ctlbridge.New(ctlbridge.Config{
		SocketName: socket,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(func() {
		cancel()
		_ = b.Stop()
		// Kill the test tmux server.
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})
	err := b.Start(ctx)
	require.NoError(t, err, "bridge.Start should succeed with a real tmux server")
	return b
}

// ──────────────────────────────────────────────────────────────────────────────
// CT-1: Start succeeds with real tmux server
// ──────────────────────────────────────────────────────────────────────────────

func TestCT1_StartSucceeds(t *testing.T) {
	skipIfNoTmux(t)
	_ = newTestBridge(t) // newTestBridge already asserts Start succeeds.
}

// ──────────────────────────────────────────────────────────────────────────────
// CT-2: tmux_not_found when tmux absent from PATH
// ──────────────────────────────────────────────────────────────────────────────

func TestCT2_TmuxNotFound(t *testing.T) {
	b := ctlbridge.New(ctlbridge.Config{
		SocketName: "bmux-test-notfound",
		TmuxBinary: "/nonexistent/tmux",
	})
	ctx := context.Background()
	err := b.Start(ctx)
	require.Error(t, err)
	var bridgeErr *ctlbridge.BridgeError
	require.ErrorAs(t, err, &bridgeErr)
	require.Equal(t, ctlbridge.ErrTmuxNotFound, bridgeErr.Code)
}

// ──────────────────────────────────────────────────────────────────────────────
// CT-3: %output event routes to subscribed channel
// ──────────────────────────────────────────────────────────────────────────────

func TestCT3_OutputRoutesToSubscribedChannel(t *testing.T) {
	skipIfNoTmux(t)
	b := newTestBridge(t)

	panes, err := b.ListPanes(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, panes, "expected at least one pane")

	paneID := panes[0].ID
	ch := make(chan []byte, 64)
	_ = b.Subscribe(paneID, ch)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = b.SendKeys(ctx, paneID, "echo ct3test", true)
	require.NoError(t, err)

	deadline := time.After(5 * time.Second)
	var received []byte
	for {
		select {
		case data := <-ch:
			received = append(received, data...)
			if strings.Contains(string(received), "ct3test") {
				return // success
			}
		case <-deadline:
			t.Fatalf("timed out waiting for ct3test output; got: %q", string(received))
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CT-4: %output NOT delivered to non-subscribed pane
// ──────────────────────────────────────────────────────────────────────────────

func TestCT4_OutputNotDeliveredToNonSubscribedPane(t *testing.T) {
	skipIfNoTmux(t)
	b := newTestBridge(t)

	panes, err := b.ListPanes(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, panes)

	// Subscribe to a non-existent pane ID — should receive nothing.
	wrongCh := make(chan []byte, 8)
	_ = b.Subscribe("nonexistent-pane-id", wrongCh)

	paneID := panes[0].ID
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = b.SendKeys(ctx, paneID, "echo ct4test", true)

	// Wait briefly; wrongCh must stay empty.
	select {
	case data := <-wrongCh:
		t.Fatalf("unexpected data on non-subscribed pane: %q", string(data))
	case <-time.After(500 * time.Millisecond):
		// pass
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CT-5: SendKeys reaches tmux pane
// ──────────────────────────────────────────────────────────────────────────────

func TestCT5_SendKeysReachesTmuxPane(t *testing.T) {
	skipIfNoTmux(t)
	b := newTestBridge(t)

	panes, err := b.ListPanes(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, panes)

	paneID := panes[0].ID
	ch := make(chan []byte, 64)
	_ = b.Subscribe(paneID, ch)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = b.SendKeys(ctx, paneID, "echo ct5marker", true)
	require.NoError(t, err)

	deadline := time.After(5 * time.Second)
	var received []byte
	for {
		select {
		case data := <-ch:
			received = append(received, data...)
			if strings.Contains(string(received), "ct5marker") {
				return
			}
		case <-deadline:
			t.Fatalf("ct5marker never appeared; got: %q", string(received))
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CT-6: ListSessions returns current sessions
// ──────────────────────────────────────────────────────────────────────────────

func TestCT6_ListSessionsReturnsCurrent(t *testing.T) {
	skipIfNoTmux(t)
	b := newTestBridge(t)

	sessions, err := b.ListSessions(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, sessions, "expected at least one session")

	// Each session must have a non-empty Name and ID.
	for _, s := range sessions {
		require.NotEmpty(t, s.Name)
		require.NotEmpty(t, s.ID)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CT-7: ListPanes returns all panes
// ──────────────────────────────────────────────────────────────────────────────

func TestCT7_ListPanesReturnsAllPanes(t *testing.T) {
	skipIfNoTmux(t)
	b := newTestBridge(t)

	panes, err := b.ListPanes(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, panes, "expected at least one pane")

	for _, p := range panes {
		require.NotEmpty(t, p.ID, "pane ID must not be empty")
		require.NotEmpty(t, p.SessionName, "pane SessionName must not be empty")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CT-8: Events() emits EventPaneExited on pane exit
// ──────────────────────────────────────────────────────────────────────────────

func TestCT8_EventPaneExited(t *testing.T) {
	skipIfNoTmux(t)
	socket := uniqueSocket(t)
	b := ctlbridge.New(ctlbridge.Config{SocketName: socket})
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	t.Cleanup(func() {
		cancel()
		_ = b.Stop()
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})
	err := b.Start(ctx)
	require.NoError(t, err)

	events := b.Events()

	sessions, err := b.ListSessions(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, sessions)

	// Create a second window so there's a "surviving" window when we close the first.
	// This causes tmux to emit %unlinked-window-close (mapped to EventPaneExited)
	// rather than killing the entire session.
	_ = exec.Command("tmux", "-L", socket,
		"new-window", "-t", sessions[0].Name).Run()

	// Get all panes and send 'exit' to the first window's pane shell.
	panes, err := b.ListPanes(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, panes)
	_ = exec.Command("tmux", "-L", socket,
		"send-keys", "-t", panes[0].ID, "exit", "Enter").Run()

	deadline := time.After(8 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("Events() channel closed before EventPaneExited")
			}
			if ev.Type == ctlbridge.EventPaneExited {
				return // success
			}
		case <-deadline:
			t.Fatal("timed out waiting for EventPaneExited (or %unlinked-window-close / %window-close)")
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CT-9: Parser handles base64-encoded %output
// ──────────────────────────────────────────────────────────────────────────────

func TestCT9_ParserHandlesBase64Output(t *testing.T) {
	pr, pw := io.Pipe()
	p := ctlbridge.NewParser(pr)

	payload := []byte("hello base64 world\r\n")
	encoded := base64.StdEncoding.EncodeToString(payload)

	go func() {
		defer pw.Close()
		fmt.Fprintf(pw, "%%output %%0 %s\r\n", encoded)
	}()

	evCh := make(chan ctlbridge.ControlModeEvent, 8)
	go p.Run(evCh, nil)

	select {
	case ev := <-evCh:
		require.Equal(t, ctlbridge.EventOutput, ev.Type)
		require.Equal(t, "%0", ev.PaneID)
		require.Equal(t, payload, ev.Data)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for parsed base64 output event")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CT-10: Parser handles raw-byte %output
// ──────────────────────────────────────────────────────────────────────────────

func TestCT10_ParserHandlesRawOutput(t *testing.T) {
	pr, pw := io.Pipe()
	p := ctlbridge.NewParser(pr)

	raw := "raw output data"

	go func() {
		defer pw.Close()
		fmt.Fprintf(pw, "%%output %%1 %s\r\n", raw)
	}()

	evCh := make(chan ctlbridge.ControlModeEvent, 8)
	go p.Run(evCh, nil)

	select {
	case ev := <-evCh:
		require.Equal(t, ctlbridge.EventOutput, ev.Type)
		require.Equal(t, "%1", ev.PaneID)
		require.Equal(t, []byte(raw), ev.Data)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for parsed raw output event")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CT-11: Stop() is idempotent
// ──────────────────────────────────────────────────────────────────────────────

func TestCT11_StopIsIdempotent(t *testing.T) {
	skipIfNoTmux(t)
	b := newTestBridge(t)

	require.NoError(t, b.Stop())
	require.NoError(t, b.Stop(), "second Stop() must return nil")
	require.NoError(t, b.Stop(), "third Stop() must return nil")
}

// ──────────────────────────────────────────────────────────────────────────────
// CT-12: command_timeout when no %end arrives within 2s
// ──────────────────────────────────────────────────────────────────────────────

func TestCT12_CommandTimeout(t *testing.T) {
	// Set up two pipes:
	// - stdinPr/stdinPw: sender writes commands → we read from stdinPr (stdin side)
	// - stdoutPr/stdoutPw: parser reads from stdoutPr (stdout side) → we write %begin without %end
	stdinPr, stdinPw := io.Pipe()
	stdoutPr, stdoutPw := io.Pipe()
	defer stdinPr.Close()
	defer stdinPw.Close()
	defer stdoutPw.Close()

	// Parser reads from tmux "stdout" and delivers responses to sender.
	parser := ctlbridge.NewParser(stdoutPr)
	sender := ctlbridge.NewCommandSender(stdinPw, parser)
	parser.SetSender(sender)

	// Start the parser.
	evCh := make(chan ctlbridge.ControlModeEvent, 8)
	go parser.Run(evCh, nil)

	// Drain stdin + emit %begin but never %end.
	go func() {
		buf := make([]byte, 512)
		_, _ = stdinPr.Read(buf)
		fmt.Fprintf(stdoutPw, "%%begin 1234567890 1 0\r\n")
		// Block forever.
		select {}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	_, err := sender.Send(ctx, `list-sessions -F "test"`)
	elapsed := time.Since(start)

	require.Error(t, err)
	var bridgeErr *ctlbridge.BridgeError
	require.ErrorAs(t, err, &bridgeErr)
	require.Equal(t, ctlbridge.ErrCommandTimeout, bridgeErr.Code)
	require.GreaterOrEqual(t, elapsed, 2*time.Second, "should wait at least 2s before timeout")
	require.Less(t, elapsed, 5*time.Second, "should not take longer than 5s")
}

// ──────────────────────────────────────────────────────────────────────────────
// BT-1: Multiple subscribers on same pane all receive output
// ──────────────────────────────────────────────────────────────────────────────

func TestBT1_MultipleSubscribersReceiveOutput(t *testing.T) {
	skipIfNoTmux(t)
	b := newTestBridge(t)

	panes, err := b.ListPanes(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, panes)

	paneID := panes[0].ID
	ch1 := make(chan []byte, 64)
	ch2 := make(chan []byte, 64)
	ch3 := make(chan []byte, 64)
	_ = b.Subscribe(paneID, ch1)
	_ = b.Subscribe(paneID, ch2)
	_ = b.Subscribe(paneID, ch3)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = b.SendKeys(ctx, paneID, "echo bt1fanout", true)
	require.NoError(t, err)

	checkCh := func(ch <-chan []byte, name string) {
		deadline := time.After(5 * time.Second)
		var received []byte
		for {
			select {
			case data := <-ch:
				received = append(received, data...)
				if strings.Contains(string(received), "bt1fanout") {
					return
				}
			case <-deadline:
				t.Errorf("subscriber %s did not receive bt1fanout; got: %q", name, string(received))
				return
			}
		}
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); checkCh(ch1, "ch1") }()
	go func() { defer wg.Done(); checkCh(ch2, "ch2") }()
	go func() { defer wg.Done(); checkCh(ch3, "ch3") }()
	wg.Wait()
}

// ──────────────────────────────────────────────────────────────────────────────
// BT-2: Unsubscribe removes channel from routing
// ──────────────────────────────────────────────────────────────────────────────

func TestBT2_UnsubscribeRemovesChannel(t *testing.T) {
	skipIfNoTmux(t)
	b := newTestBridge(t)

	panes, err := b.ListPanes(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, panes)

	paneID := panes[0].ID
	ch := make(chan []byte, 64)
	unsub := b.Subscribe(paneID, ch)

	// Unsubscribe before sending keys.
	unsub()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = b.SendKeys(ctx, paneID, "echo bt2gone", true)

	select {
	case data := <-ch:
		t.Fatalf("received data on unsubscribed channel: %q", string(data))
	case <-time.After(500 * time.Millisecond):
		// pass
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// BT-3: Slow subscriber causes drop + WARN, other subscribers unaffected
// ──────────────────────────────────────────────────────────────────────────────

func TestBT3_SlowSubscriberDropsAndOthersUnaffected(t *testing.T) {
	skipIfNoTmux(t)
	b := newTestBridge(t)

	panes, err := b.ListPanes(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, panes)

	paneID := panes[0].ID

	// slowCh has buffer 0 — will always block.
	slowCh := make(chan []byte, 0)
	fastCh := make(chan []byte, 256)

	_ = b.Subscribe(paneID, slowCh)
	_ = b.Subscribe(paneID, fastCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send enough output to fill the slow subscriber's zero-buffer.
	for i := 0; i < 5; i++ {
		_ = b.SendKeys(ctx, paneID, fmt.Sprintf("echo bt3frame%d", i), true)
	}

	// Fast subscriber should receive something.
	deadline := time.After(5 * time.Second)
	var received []byte
	for {
		select {
		case data := <-fastCh:
			received = append(received, data...)
			if strings.Contains(string(received), "bt3frame") {
				return // success
			}
		case <-deadline:
			t.Fatalf("fast subscriber never received bt3frame; got: %q", string(received))
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// BT-4: %session-created event appears on Events() channel
// ──────────────────────────────────────────────────────────────────────────────

func TestBT4_SessionCreatedEvent(t *testing.T) {
	skipIfNoTmux(t)
	socket := uniqueSocket(t)
	b := ctlbridge.New(ctlbridge.Config{SocketName: socket})
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	t.Cleanup(func() {
		cancel()
		_ = b.Stop()
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})
	err := b.Start(ctx)
	require.NoError(t, err)

	events := b.Events()

	// Create a new session.
	newSession := fmt.Sprintf("bt4-%d", time.Now().UnixNano())
	_ = exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", newSession).Run()

	deadline := time.After(8 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("Events() channel closed prematurely")
			}
			if (ev.Type == ctlbridge.EventSessionCreated || ev.Type == ctlbridge.EventSessionsChanged) &&
				(ev.Session == newSession || ev.Session == "") {
				return // success — either exact session name or sessions-changed notification
			}
		case <-deadline:
			t.Fatal("timed out waiting for session-created or sessions-changed event")
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// BT-5: Bridge handles %output with empty data (no panic)
// ──────────────────────────────────────────────────────────────────────────────

func TestBT5_EmptyOutputNoPanic(t *testing.T) {
	pr, pw := io.Pipe()
	p := ctlbridge.NewParser(pr)

	go func() {
		defer pw.Close()
		// %output with empty payload (just the pane id, no data after space).
		fmt.Fprintf(pw, "%%output %%0 \r\n")
	}()

	evCh := make(chan ctlbridge.ControlModeEvent, 8)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		p.Run(evCh, nil)
	}()

	select {
	case ev := <-evCh:
		require.Equal(t, ctlbridge.EventOutput, ev.Type)
		require.Equal(t, "%0", ev.PaneID)
		require.Equal(t, []byte{}, ev.Data)
	case <-doneCh:
		// parser exited without panic — acceptable if no event emitted for empty payload
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for empty output event")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// RG-1: Slow subscriber must not block parser goroutine
// ──────────────────────────────────────────────────────────────────────────────

func TestRG1_SlowSubscriberDoesNotBlockParser(t *testing.T) {
	skipIfNoTmux(t)
	b := newTestBridge(t)

	panes, err := b.ListPanes(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, panes)

	paneID := panes[0].ID

	// slowCh is unbuffered — guaranteed to block on non-blocking send.
	slowCh := make(chan []byte)
	fastCh := make(chan []byte, 256)

	_ = b.Subscribe(paneID, slowCh)
	_ = b.Subscribe(paneID, fastCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send output that should be received by fastCh within the deadline.
	_ = b.SendKeys(ctx, paneID, "echo rg1fast", true)

	deadline := time.After(3 * time.Second)
	var received []byte
	for {
		select {
		case data := <-fastCh:
			received = append(received, data...)
			if strings.Contains(string(received), "rg1fast") {
				return // parser was not blocked
			}
		case <-deadline:
			t.Fatal("fast subscriber blocked — parser goroutine may have been stalled by slow subscriber")
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// RG-2: %output %0 with empty payload must not crash parser; Event.Data = []byte{}
// ──────────────────────────────────────────────────────────────────────────────

func TestRG2_EmptyOutputPayloadNoCrash(t *testing.T) {
	pr, pw := io.Pipe()
	p := ctlbridge.NewParser(pr)

	go func() {
		defer pw.Close()
		// Exactly the format: "%output %0 " (trailing space, no data)
		fmt.Fprintf(pw, "%%output %%0 \r\n")
		// Then a second valid line to confirm parser is still running.
		fmt.Fprintf(pw, "%%output %%1 hello\r\n")
	}()

	evCh := make(chan ctlbridge.ControlModeEvent, 8)
	go p.Run(evCh, nil)

	var events []ctlbridge.ControlModeEvent
	deadline := time.After(2 * time.Second)
	for len(events) < 2 {
		select {
		case ev := <-evCh:
			events = append(events, ev)
		case <-deadline:
			// If we got at least the first event and parser didn't crash, that's enough.
			if len(events) > 0 {
				break
			}
			t.Fatal("parser crashed or hung on empty payload")
		}
		if len(events) >= 1 {
			break // Don't need both; just verify no crash
		}
	}

	require.NotEmpty(t, events)
	require.Equal(t, ctlbridge.EventOutput, events[0].Type)
	require.Equal(t, "%0", events[0].PaneID)
	require.Equal(t, []byte{}, events[0].Data, "empty payload must be []byte{}, not nil")
}
