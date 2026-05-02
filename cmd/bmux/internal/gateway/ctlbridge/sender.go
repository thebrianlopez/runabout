package ctlbridge

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// commandTimeout is the per-command deadline for receiving a %end response.
const commandTimeout = 2 * time.Second

// CommandSender writes commands to tmux stdin and correlates responses via a
// FIFO queue of pending channels.
// tmux processes commands sequentially, so each %begin/%end block corresponds
// to the next command in the queue. The parser calls DeliverResponse when
// a %begin/%end block completes.
type CommandSender struct {
	w io.Writer

	mu        sync.Mutex
	queue     []chan []string // FIFO of pending response channels
	cancelled bool
	cancelChs []chan struct{}
}

// NewCommandSender creates a CommandSender that writes to w.
// Wire it to a parser via parser.SetSender(s).
func NewCommandSender(w io.Writer, _ *ControlModeParser) *CommandSender {
	return &CommandSender{w: w}
}

// Send writes cmd to tmux stdin and waits for the correlated %end response.
// Returns the collected response lines, or an error on timeout/context cancel.
func (s *CommandSender) Send(ctx context.Context, cmd string) ([]string, error) {
	respCh := make(chan []string, 1)
	cancelCh := make(chan struct{})

	s.mu.Lock()
	if s.cancelled {
		s.mu.Unlock()
		return nil, context.Canceled
	}
	s.queue = append(s.queue, respCh)
	s.cancelChs = append(s.cancelChs, cancelCh)
	s.mu.Unlock()

	cleanup := func() {
		s.mu.Lock()
		// Remove respCh from queue (it may already be delivered, just remove cancelCh).
		newChs := s.cancelChs[:0:len(s.cancelChs)]
		newChs = newChs[:0]
		for _, c := range s.cancelChs {
			if c != cancelCh {
				newChs = append(newChs, c)
			}
		}
		s.cancelChs = newChs
		s.mu.Unlock()
	}
	defer cleanup()

	// Write command to stdin.
	if _, err := fmt.Fprintf(s.w, "%s\n", cmd); err != nil {
		// Remove our entry from the queue on write failure.
		s.mu.Lock()
		newQ := s.queue[:0:len(s.queue)]
		newQ = newQ[:0]
		for _, c := range s.queue {
			if c != respCh {
				newQ = append(newQ, c)
			}
		}
		s.queue = newQ
		s.mu.Unlock()
		return nil, fmt.Errorf("send command: %w", err)
	}

	timer := time.NewTimer(commandTimeout)
	defer timer.Stop()

	select {
	case lines := <-respCh:
		return lines, nil

	case <-timer.C:
		slog.Warn("command_timeout",
			"command_type", extractCommandType(cmd),
			"timeout_ms", commandTimeout.Milliseconds())
		return nil, bridgeErr(ErrCommandTimeout,
			fmt.Sprintf("no %%end received within %v", commandTimeout),
			nil)

	case <-cancelCh:
		return nil, context.Canceled

	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// DeliverResponse is called by the parser when a %begin/%end block completes.
// It pops the next pending channel from the FIFO and sends lines to it.
func (s *CommandSender) DeliverResponse(lines []string) {
	s.mu.Lock()
	if len(s.queue) == 0 {
		s.mu.Unlock()
		return
	}
	ch := s.queue[0]
	s.queue = s.queue[1:]
	s.mu.Unlock()

	select {
	case ch <- lines:
	default:
	}
}

// CancelAll cancels all in-flight Send calls.
func (s *CommandSender) CancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelled {
		return
	}
	s.cancelled = true
	for _, ch := range s.cancelChs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// extractCommandType returns the first word of a command string for logging.
func extractCommandType(cmd string) string {
	for i, c := range cmd {
		if c == ' ' || c == '\t' {
			return cmd[:i]
		}
	}
	return cmd
}
