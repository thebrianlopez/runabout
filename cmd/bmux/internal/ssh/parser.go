package ssh

import (
	"bufio"
	"encoding/base64"
	"io"
	"strings"
)

// ControlModeParser reads a tmux control mode event stream from r and emits
// PaneEvents on the returned channel. The channel is closed when r reaches EOF
// or an unrecoverable read error occurs.
//
// Wire format (subset parsed):
//
//	%begin <time> <num> <flags>
//	%output %<pane-id> <base64-data>
//	%end <time> <num> <flags>
//	%session-changed <session-id> <name>
//	%window-add <window-id>
//	%layout-change <window-id> <layout>
//	%pane-mode-changed <pane-id>
//	%exit [reason]
//
// Output data is transmitted as base64-encoded bytes. Unrecognised event
// lines are silently skipped (the goroutine continues running).
func ControlModeParser(host string, r io.Reader) <-chan PaneEvent {
	ch := make(chan PaneEvent, 64)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			if ev, ok := parseLine(host, line); ok {
				ch <- ev
				if ev.Type == PaneClosed && ev.PaneID == "" {
					// %exit received — session is done.
					return
				}
			}
		}
	}()
	return ch
}

// stripSigil removes the leading tmux sigil character (%, @, $) from an ID.
func stripSigil(s string) string {
	if len(s) > 0 && (s[0] == '%' || s[0] == '@' || s[0] == '$') {
		return s[1:]
	}
	return s
}

// parseLine converts a single control mode line into a PaneEvent.
// Returns (event, true) on success, (zero, false) on skip.
func parseLine(host, line string) (PaneEvent, bool) {
	if line == "" || !strings.HasPrefix(line, "%") {
		return PaneEvent{}, false
	}

	parts := strings.SplitN(line, " ", 3)
	keyword := parts[0]

	switch keyword {
	case "%output":
		// %output %<pane-id> <base64-data>
		if len(parts) < 3 {
			return PaneEvent{}, false
		}
		paneID := stripSigil(parts[1])
		data, err := base64.StdEncoding.DecodeString(parts[2])
		if err != nil {
			// Treat non-base64 data as raw bytes (compatibility).
			data = []byte(parts[2])
		}
		return PaneEvent{
			Type:   PaneOutput,
			Host:   host,
			PaneID: paneID,
			Data:   data,
		}, true

	case "%exit":
		// Session is ending.
		return PaneEvent{
			Type: PaneClosed,
			Host: host,
		}, true

	case "%window-add":
		// New window/pane created. Emit PaneCreated.
		paneID := ""
		if len(parts) >= 2 {
			paneID = stripSigil(parts[1])
		}
		return PaneEvent{
			Type:   PaneCreated,
			Host:   host,
			PaneID: paneID,
		}, true

	case "%layout-change":
		// Pane dimensions may have changed. Parse cols×rows from layout string
		// if present; otherwise emit a basic resized event.
		// Layout format: <id> <cols>x<rows>,... (simplified)
		return PaneEvent{
			Type: PaneResized,
			Host: host,
		}, true

	// Informational events — no PaneEvent emitted.
	case "%begin", "%end", "%session-changed", "%pane-mode-changed",
		"%sessions-changed", "%client-session-changed", "%unlinked-window-add":
		return PaneEvent{}, false

	default:
		return PaneEvent{}, false
	}
}
