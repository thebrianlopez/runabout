package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

const (
	paneIDPrefixLen  = 16
	writeTimeout     = 5 * time.Second
	writeChanBuf     = 128
)

// clientMsg is the inbound JSON message from a mobile client.
type clientMsg struct {
	Type   string `json:"type"`
	PaneID string `json:"pane_id"`
	Keys   string `json:"keys"`
	Literal bool   `json:"literal"`
	Cols   int    `json:"cols"`
	Rows   int    `json:"rows"`
}

// serverMsg is an outbound JSON control message to a client.
type serverMsg struct {
	Type     string    `json:"type"`
	Sessions []Session `json:"sessions,omitempty"`
	PaneID   string    `json:"pane_id,omitempty"`
	Code     string    `json:"code,omitempty"`
	Message  string    `json:"message,omitempty"`
}

// buildPaneFrame creates a binary frame with a 16-byte ASCII zero-padded pane ID prefix.
func buildPaneFrame(paneID string, data []byte) []byte {
	frame := make([]byte, paneIDPrefixLen+len(data))
	copy(frame[:paneIDPrefixLen], paneID)
	copy(frame[paneIDPrefixLen:], data)
	return frame
}

// handleConnection manages a single authenticated WebSocket client.
func (g *gateway) handleConnection(ctx context.Context, clientID string, conn *websocket.Conn) {
	defer func() {
		g.registry.Remove(clientID)
		slog.Info("client_disconnected", "client_id", clientID, "clients", g.registry.Count())
	}()

	// Dedicated write channel — write goroutine is the ONLY writer.
	writeCh := make(chan []byte, writeChanBuf)
	writeCtx, writeCancel := context.WithCancel(ctx)
	defer writeCancel()

	// Start write goroutine.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		for {
			select {
			case <-writeCtx.Done():
				return
			case frame, ok := <-writeCh:
				if !ok {
					return
				}
				wCtx, cancel := context.WithTimeout(writeCtx, writeTimeout)
				var err error
				if frame[0] == '{' {
					err = conn.Write(wCtx, websocket.MessageText, frame)
				} else {
					err = conn.Write(wCtx, websocket.MessageBinary, frame)
				}
				cancel()
				if err != nil {
					slog.Debug("client_write_error", "client_id", clientID, "error", err)
					return
				}
			}
		}
	}()

	// Send session list immediately after connect.
	sessions, err := g.registry2.Sessions(ctx)
	if err != nil {
		sessions = nil
	}
	sessionListJSON, _ := json.Marshal(serverMsg{Type: "session-list", Sessions: sessions})
	select {
	case writeCh <- sessionListJSON:
	default:
	}

	// Read loop.
	for {
		var msg clientMsg
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			break
		}
		switch msg.Type {
		case "pane-attach":
			g.handlePaneAttach(ctx, clientID, msg, writeCh)
		case "pane-detach":
			g.registry.RemoveSub(clientID, msg.PaneID)
			slog.Info("pane_unsubscribed", "client_id", clientID, "pane_id", msg.PaneID)
		case "send-keys":
			go func(m clientMsg) {
				translated := g.translator.Translate(m.Keys)
				if err := g.bridge.SendKeys(ctx, m.PaneID, translated, m.Literal); err != nil {
					slog.Warn("send_keys_error", "client_id", clientID, "pane_id", m.PaneID, "error", err)
				} else {
					slog.Info("send_keys", "client_id", clientID, "pane_id", m.PaneID)
				}
			}(msg)
		case "resize":
			go func(m clientMsg) {
				// F2 Resize not in our MirrorManager interface subset; no-op.
				_ = m
			}(msg)
		}
	}

	// Signal writer to stop and wait.
	writeCancel()
	<-writeDone
}

// handlePaneAttach subscribes to F1, sends snapshot, streams live frames.
func (g *gateway) handlePaneAttach(ctx context.Context, clientID string, msg clientMsg, writeCh chan<- []byte) {
	paneID := msg.PaneID

	ch, cancel, err := g.bridge.Subscribe(paneID)
	if err != nil {
		errMsg, _ := json.Marshal(serverMsg{
			Type:    "error",
			PaneID:  paneID,
			Code:    "pane_not_found",
			Message: fmt.Sprintf("pane %s not found", paneID),
		})
		select {
		case writeCh <- errMsg:
		default:
		}
		return
	}

	g.registry.AddSub(clientID, paneID, cancel)
	slog.Info("pane_subscribed", "client_id", clientID, "pane_id", paneID)

	// Send snapshot-start.
	startMsg, _ := json.Marshal(serverMsg{Type: "snapshot-start", PaneID: paneID})
	select {
	case writeCh <- startMsg:
	case <-ctx.Done():
		return
	}

	// Deliver snapshot.
	snap, err := g.mirror.Snapshot(paneID, msg.Cols, msg.Rows)
	if err == nil && len(snap) > 0 {
		frame := buildPaneFrame(paneID, snap)
		// Binary frames start with pane prefix byte which is '%' (0x25) not '{'.
		select {
		case writeCh <- frame:
		case <-ctx.Done():
			return
		}
	}

	// Send snapshot-end.
	endMsg, _ := json.Marshal(serverMsg{Type: "snapshot-end", PaneID: paneID})
	select {
	case writeCh <- endMsg:
	case <-ctx.Done():
		return
	}

	// Stream live frames in a goroutine (non-blocking for the read loop).
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case data, ok := <-ch:
				if !ok {
					return
				}
				frame := buildPaneFrame(paneID, data)
				select {
				case writeCh <- frame:
				case <-ctx.Done():
					return
				default:
					// Drop frame for slow consumer.
					slog.Debug("client_write_timeout", "client_id", clientID, "pane_id", paneID)
				}
			}
		}
	}()
}
