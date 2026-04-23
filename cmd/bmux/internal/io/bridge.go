// Package io implements the bidirectional I/O bridge between the local
// operator/agent and a remote projected tmux session.
package io

import (
	"context"
	"fmt"

	"github.com/blo-grindr/bmux/internal/bridge"
	"github.com/blo-grindr/bmux/internal/ssh"
)

// IOBridge wires together input forwarding and output streaming for one session.
type IOBridge interface {
	// ForwardInput hex-escapes data and sends it to the remote pane via
	// Session.SendInput. Returns ErrSessionClosed if the session is disconnected.
	ForwardInput(ctx context.Context, data []byte) error

	// StreamOutput reads PaneOutput events from the session and applies them
	// to the local projected pane. Runs until ctx is cancelled or the session
	// disconnects. Returns nil on clean exit.
	StreamOutput(ctx context.Context) error
}

// ioBridge is the concrete IOBridge implementation.
type ioBridge struct {
	session  ssh.Session
	bridge   bridge.LocalTmuxBridge
	hostName string
}

// NewIOBridge constructs an IOBridge for the given session and local bridge.
// hostName is the HostConfig.Name used as the local tmux session name.
func NewIOBridge(session ssh.Session, br bridge.LocalTmuxBridge) IOBridge {
	return &ioBridge{
		session:  session,
		bridge:   br,
		hostName: session.Host(),
	}
}

// ForwardInput hex-escapes data and forwards it to the remote pane.
// Format: each byte → "0x<hex>", joined by spaces.
// e.g. []byte{0x41, 0x0a} → "0x41 0x0a"
func (b *ioBridge) ForwardInput(ctx context.Context, data []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if len(data) == 0 {
		return nil
	}
	encoded := hexEscape(data)
	if err := b.session.SendInput([]byte(encoded)); err != nil {
		return err
	}
	return nil
}

// StreamOutput reads PaneOutput events and writes them to the local pane.
// Exits cleanly (nil error) when the events channel closes.
func (b *ioBridge) StreamOutput(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-b.session.Events():
			if !ok {
				// Session disconnected — clean exit.
				return nil
			}
			if ev.Type != ssh.PaneOutput {
				continue
			}
			if err := b.bridge.ApplyOutput(b.hostName, ev.Data); err != nil {
				// E32: log and continue — a failed ApplyOutput never terminates the stream.
				fmt.Printf("[%s: failed to apply output — local pane gone? %v]\n", b.hostName, err)
				continue
			}
		}
	}
}

// hexEscape converts raw bytes to a space-separated hex string.
// e.g. []byte{0x41, 0x0a} → "0x41 0x0a"
func hexEscape(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	buf := make([]byte, 0, len(data)*5)
	for i, b := range data {
		if i > 0 {
			buf = append(buf, ' ')
		}
		buf = append(buf, fmt.Sprintf("0x%02x", b)...)
	}
	return string(buf)
}
