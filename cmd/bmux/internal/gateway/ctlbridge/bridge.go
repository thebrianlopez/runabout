// Package ctlbridge implements the tmux Control Mode Bridge (F1) for bmux.
// It spawns a tmux -CC subprocess over a real PTY, parses its control-mode
// output, routes %output events to per-pane subscribers, and forwards
// commands (send-keys, list-sessions, etc.) to tmux stdin.
package ctlbridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
)

// ─── Public types ─────────────────────────────────────────────────────────────

// ControlModeBridge is the primary interface for the F1 layer.
type ControlModeBridge interface {
	// Start spawns the tmux -CC subprocess and blocks until the bridge is ready.
	Start(ctx context.Context) error

	// Stop sends detach-client to tmux and waits for the subprocess to exit
	// (max 2s). Idempotent — safe to call multiple times.
	Stop() error

	// Subscribe registers ch to receive raw PTY bytes for paneID.
	// Returns an unsubscribe func; calling it removes ch from routing.
	// ch must be buffered by the caller; a full channel causes frame drop + WARN.
	Subscribe(paneID string, ch chan<- []byte) (unsubscribe func())

	// SendKeys sends key sequences to the given pane.
	SendKeys(ctx context.Context, paneID string, keys string, literal bool) error

	// ResizePane resizes a pane to the given dimensions.
	ResizePane(ctx context.Context, paneID string, cols, rows int) error

	// ListSessions returns all active tmux sessions.
	ListSessions(ctx context.Context) ([]Session, error)

	// ListPanes returns all panes across all sessions.
	ListPanes(ctx context.Context) ([]Pane, error)

	// Events returns a channel of control-mode topology events.
	// The channel is closed when the bridge reaches Stopped state.
	Events() <-chan ControlModeEvent
}

// Session describes a tmux session.
type Session struct {
	Name string
	ID   string
}

// Pane describes a tmux pane.
type Pane struct {
	ID          string
	SessionName string
	WindowName  string
	Active      bool
}

// ControlModeEvent is a topology or output notification from tmux control mode.
type ControlModeEvent struct {
	Type    ControlModeEventType
	PaneID  string
	Data    []byte
	Session string
	Window  string
}

// ControlModeEventType classifies control-mode events.
type ControlModeEventType string

const (
	EventOutput          ControlModeEventType = "output"
	EventSessionCreated  ControlModeEventType = "session-created"
	EventSessionClosed   ControlModeEventType = "session-closed"
	EventWindowAdd       ControlModeEventType = "window-add"
	EventPaneExited      ControlModeEventType = "pane-exited"
	EventPaneFocus       ControlModeEventType = "pane-focus"
	EventSessionsChanged ControlModeEventType = "sessions-changed"
	EventSessionChanged  ControlModeEventType = "session-changed"
)

// ─── Error types ──────────────────────────────────────────────────────────────

// ErrorCode classifies bridge errors.
type ErrorCode string

const (
	ErrTmuxNotFound          ErrorCode = "tmux_not_found"
	ErrControlModeAttachFail ErrorCode = "control_mode_attach_failed"
	ErrControlModeDisconnect ErrorCode = "control_mode_disconnected"
	ErrCommandTimeout        ErrorCode = "command_timeout"
)

// BridgeError wraps an error with a typed error code.
type BridgeError struct {
	Code ErrorCode
	Msg  string
	Err  error
}

func (e *BridgeError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Msg, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Msg)
}

func (e *BridgeError) Unwrap() error { return e.Err }

func bridgeErr(code ErrorCode, msg string, err error) *BridgeError {
	return &BridgeError{Code: code, Msg: msg, Err: err}
}

// ─── Config ───────────────────────────────────────────────────────────────────

// Config holds the bridge configuration.
type Config struct {
	SocketName string
	TmuxBinary string
}

// ─── bridge implementation ───────────────────────────────────────────────────

// New creates a new ControlModeBridge with the given config.
func New(cfg Config) ControlModeBridge {
	return &bridge{
		cfg:      cfg,
		eventsCh: make(chan ControlModeEvent, 256),
		stopCh:   make(chan struct{}),
	}
}

type bridge struct {
	cfg Config

	ptmx *os.File
	cmd  *exec.Cmd

	parser *ControlModeParser
	router *SubscriptionRouter
	sender *CommandSender

	eventsCh chan ControlModeEvent
	stopCh   chan struct{}

	mu         sync.Mutex
	started    bool
	stopped    bool
	stopOnce   sync.Once
	closedOnce sync.Once
}

// Start spawns the tmux -CC subprocess.
func (b *bridge) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return errors.New("bridge already started")
	}

	// Resolve tmux binary.
	tmuxBin := b.cfg.TmuxBinary
	if tmuxBin == "" {
		path, err := exec.LookPath("tmux")
		if err != nil {
			b.mu.Unlock()
			return bridgeErr(ErrTmuxNotFound, "tmux binary not found in PATH", err)
		}
		tmuxBin = path
	} else {
		if _, err := os.Stat(tmuxBin); err != nil {
			b.mu.Unlock()
			return bridgeErr(ErrTmuxNotFound, "tmux binary not found at configured path", err)
		}
	}

	slog.Info("control_mode_attach", "socket", b.cfg.SocketName, "binary", tmuxBin)
	startTime := time.Now()

	socketArgs := []string{}
	if b.cfg.SocketName != "" {
		socketArgs = []string{"-L", b.cfg.SocketName}
	}

	// Ensure a session exists. Run "new-session -d -s bmux" first (ignore errors
	// if session already exists).
	ensureArgs := append(append([]string{}, socketArgs...), "new-session", "-d", "-s", "bmux")
	_ = exec.Command(tmuxBin, ensureArgs...).Run()

	// Attach in control mode.
	attachArgs := append(append([]string{}, socketArgs...), "-CC", "attach-session")
	cmd := exec.Command(tmuxBin, attachArgs...)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		b.mu.Unlock()
		return bridgeErr(ErrControlModeAttachFail, "failed to spawn tmux -CC subprocess", err)
	}

	b.ptmx = ptmx
	b.cmd = cmd
	b.router = NewSubscriptionRouter()
	b.parser = NewParser(ptmx)
	b.sender = NewCommandSender(ptmx, b.parser)
	b.parser.SetSender(b.sender)
	b.started = true
	b.mu.Unlock()

	// readyCh is closed when the bridge is ready (first control-mode event).
	readyCh := make(chan struct{})
	var readyOnce sync.Once
	markReady := func() { readyOnce.Do(func() { close(readyCh) }) }

	// Launch parser goroutine.
	go func() {
		b.parser.Run(b.eventsCh, func(ev ControlModeEvent) {
			markReady()
			if ev.Type == EventOutput {
				b.router.Deliver(ev.PaneID, ev.Data)
			}
		})
		// Parser done → bridge disconnected.
		b.mu.Lock()
		alreadyStopped := b.stopped
		b.mu.Unlock()

		if !alreadyStopped {
			slog.Warn("control_mode_disconnected", "reason", "parser EOF")
		}
		b.closedOnce.Do(func() { close(b.eventsCh) })
	}()

	// Also start a goroutine that marks ready if the process stays alive
	// for a short window even without emitting events (some setups are quiet
	// until the first command).
	go func() {
		select {
		case <-time.After(300 * time.Millisecond):
			// Process still running after 300ms → consider it ready.
			if cmd.ProcessState == nil {
				markReady()
			}
		case <-readyCh:
		case <-b.stopCh:
		}
	}()

	// Wait for ready, context cancellation, or process death.
	select {
	case <-readyCh:
		slog.Info("control_mode_attached", "duration_ms", time.Since(startTime).Milliseconds())
		return nil
	case <-time.After(10 * time.Second):
		// If the process is still alive after 10s we assume it's connected.
		if cmd.ProcessState == nil {
			slog.Info("control_mode_attached", "duration_ms", time.Since(startTime).Milliseconds())
			return nil
		}
		return bridgeErr(ErrControlModeAttachFail, "tmux process exited before first event", nil)
	case <-ctx.Done():
		_ = b.Stop()
		return ctx.Err()
	}
}

// Stop sends detach-client and waits for subprocess exit (max 2s). Idempotent.
func (b *bridge) Stop() error {
	b.stopOnce.Do(func() {
		b.mu.Lock()
		b.stopped = true
		ptmx := b.ptmx
		cmd := b.cmd
		sender := b.sender
		b.mu.Unlock()

		close(b.stopCh)

		if sender != nil {
			sender.CancelAll()
		}

		if ptmx != nil && cmd != nil && cmd.Process != nil {
			_, _ = fmt.Fprintf(ptmx, "detach-client\n")

			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = cmd.Wait()
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
			_ = ptmx.Close()
		}

		b.closedOnce.Do(func() { close(b.eventsCh) })
	})
	return nil
}

// Subscribe registers ch to receive output for paneID.
func (b *bridge) Subscribe(paneID string, ch chan<- []byte) (unsubscribe func()) {
	return b.router.Subscribe(paneID, ch)
}

// SendKeys sends key sequences to a pane.
func (b *bridge) SendKeys(ctx context.Context, paneID string, keys string, literal bool) error {
	var cmd string
	if literal {
		cmd = fmt.Sprintf("send-keys -t %s -l %q", paneID, keys)
	} else {
		cmd = fmt.Sprintf("send-keys -t %s %q", paneID, keys)
	}
	slog.Debug("command_sent", "command_type", "send-keys", "pane_id", paneID)
	_, err := b.sender.Send(ctx, cmd)
	return err
}

// ResizePane resizes a pane.
func (b *bridge) ResizePane(ctx context.Context, paneID string, cols, rows int) error {
	cmd := fmt.Sprintf("resize-pane -t %s -x %d -y %d", paneID, cols, rows)
	slog.Debug("command_sent", "command_type", "resize-pane", "pane_id", paneID)
	_, err := b.sender.Send(ctx, cmd)
	return err
}

// ListSessions returns all active tmux sessions.
func (b *bridge) ListSessions(ctx context.Context) ([]Session, error) {
	slog.Debug("command_sent", "command_type", "list-sessions", "pane_id", "")
	lines, err := b.sender.Send(ctx, `list-sessions -F "#{session_name} #{session_id}"`)
	if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, line := range lines {
		line = trimCR(line)
		if line == "" {
			continue
		}
		var s Session
		if n, _ := fmt.Sscanf(line, "%s %s", &s.Name, &s.ID); n >= 1 {
			sessions = append(sessions, s)
		}
	}
	return sessions, nil
}

// ListPanes returns all panes across all sessions.
func (b *bridge) ListPanes(ctx context.Context) ([]Pane, error) {
	slog.Debug("command_sent", "command_type", "list-panes", "pane_id", "")
	lines, err := b.sender.Send(ctx, `list-panes -a -F "#{pane_id} #{session_name} #{window_name} #{pane_active}"`)
	if err != nil {
		return nil, err
	}
	var panes []Pane
	for _, line := range lines {
		line = trimCR(line)
		if line == "" {
			continue
		}
		var p Pane
		var activeStr string
		n, _ := fmt.Sscanf(line, "%s %s %s %s", &p.ID, &p.SessionName, &p.WindowName, &activeStr)
		if n >= 1 {
			p.Active = activeStr == "1"
			panes = append(panes, p)
		}
	}
	return panes, nil
}

// Events returns the channel of control-mode events.
func (b *bridge) Events() <-chan ControlModeEvent {
	return b.eventsCh
}

// trimCR removes trailing \r from a string.
func trimCR(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\r' {
		return s[:len(s)-1]
	}
	return s
}
