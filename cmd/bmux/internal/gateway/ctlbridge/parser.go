package ctlbridge

import (
	"bufio"
	"encoding/base64"
	"io"
	"log/slog"
	"strings"
)

// ControlModeParser reads tmux control-mode output line by line, classifies
// each line, and emits ControlModeEvents and %begin/%end responses.
type ControlModeParser struct {
	r      io.Reader
	sender *CommandSender // may be nil (unit tests)
}

// NewParser creates a parser that reads from r.
// sender may be nil — in that case, %begin/%end blocks are not delivered to any sender.
func NewParser(r io.Reader) *ControlModeParser {
	return &ControlModeParser{r: r}
}

// SetSender wires the parser to a CommandSender so that %begin/%end blocks
// are delivered to in-flight Send calls.
func (p *ControlModeParser) SetSender(s *CommandSender) {
	p.sender = s
}

// stripDCS removes the DCS passthrough prefix and suffix that tmux 3.x wraps
// around control mode lines: ESC P 1000 p ... ESC \.
// If the line doesn't start with ESC P (i.e. the DCS passthrough prefix),
// it is returned unchanged.
func stripDCS(line string) string {
	// tmux wraps in "\x1bP1000p<content>\x1b\\"
	// We strip the leading \x1bP...p prefix and trailing \x1b\ suffix.
	if len(line) > 0 && line[0] == '\x1b' {
		// Find the 'p' that ends the DCS parameter section.
		idx := strings.IndexByte(line, 'p')
		if idx >= 1 && idx < 10 {
			line = line[idx+1:]
		}
	}
	// Strip DCS string terminator \x1b\ at the end.
	if strings.HasSuffix(line, "\x1b\\") {
		line = line[:len(line)-2]
	}
	return line
}

// Run reads from the parser's reader and dispatches events.
// It blocks until the reader is closed or returns an error.
// eventCh receives topology/output events.
// interceptFn, if non-nil, is called for every event (used by bridge for routing).
func (p *ControlModeParser) Run(eventCh chan<- ControlModeEvent, interceptFn func(ControlModeEvent)) {
	scanner := bufio.NewScanner(p.r)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	type collectState struct {
		lines []string
	}
	var collecting *collectState

	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimRight(raw, "\r")
		line = stripDCS(line)

		if collecting != nil {
			if strings.HasPrefix(line, "%end") {
				// End of %begin/%end block — deliver to sender.
				if p.sender != nil {
					p.sender.DeliverResponse(collecting.lines)
				}
				collecting = nil
				continue
			}
			if strings.HasPrefix(line, "%begin") {
				// Nested %begin — unexpected; reset state.
				slog.Warn("control_mode_parse_error", "raw_line", line, "error", "unexpected nested %begin")
				collecting = nil
			} else {
				collecting.lines = append(collecting.lines, line)
				continue
			}
		}

		if !strings.HasPrefix(line, "%") {
			continue
		}

		if strings.HasPrefix(line, "%begin") {
			collecting = &collectState{}
			continue
		}

		ev, ok := parseLine(line)
		if !ok {
			continue
		}

		slog.Debug("control_mode_event",
			"event_type", string(ev.Type),
			"pane_id", ev.PaneID,
			"data_bytes", len(ev.Data))

		if interceptFn != nil {
			interceptFn(ev)
		}
		select {
		case eventCh <- ev:
		default:
			slog.Warn("control_mode_event_dropped",
				"event_type", string(ev.Type),
				"pane_id", ev.PaneID)
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("control_mode_parse_error", "raw_line", "", "error", err.Error())
	}
}

// parseLine classifies a single tmux control-mode line.
func parseLine(line string) (ControlModeEvent, bool) {
	switch {
	case strings.HasPrefix(line, "%output "):
		return parseOutput(line), true

	case strings.HasPrefix(line, "%session-created "):
		name := strings.TrimPrefix(line, "%session-created ")
		return ControlModeEvent{Type: EventSessionCreated, Session: name}, true

	case strings.HasPrefix(line, "%session-closed "):
		name := strings.TrimPrefix(line, "%session-closed ")
		return ControlModeEvent{Type: EventSessionClosed, Session: name}, true

	case line == "%sessions-changed":
		return ControlModeEvent{Type: EventSessionsChanged}, true

	case strings.HasPrefix(line, "%session-changed "):
		parts := strings.SplitN(strings.TrimPrefix(line, "%session-changed "), " ", 2)
		sessionID, sessionName := "", ""
		if len(parts) >= 1 {
			sessionID = parts[0]
		}
		if len(parts) >= 2 {
			sessionName = parts[1]
		}
		return ControlModeEvent{
			Type:    EventSessionChanged,
			PaneID:  sessionID,
			Session: sessionName,
		}, true

	case strings.HasPrefix(line, "%window-add "):
		win := strings.TrimPrefix(line, "%window-add ")
		return ControlModeEvent{Type: EventWindowAdd, Window: win}, true

	case strings.HasPrefix(line, "%pane-exited "):
		paneID := strings.TrimPrefix(line, "%pane-exited ")
		return ControlModeEvent{Type: EventPaneExited, PaneID: paneID}, true

	case strings.HasPrefix(line, "%pane-focus "):
		paneID := strings.TrimPrefix(line, "%pane-focus ")
		return ControlModeEvent{Type: EventPaneFocus, PaneID: paneID}, true

	// tmux 3.x: window-close events — map to EventPaneExited for compatibility.
	case strings.HasPrefix(line, "%unlinked-window-close "):
		win := strings.TrimPrefix(line, "%unlinked-window-close ")
		return ControlModeEvent{Type: EventPaneExited, Window: win}, true

	case strings.HasPrefix(line, "%window-close "):
		win := strings.TrimPrefix(line, "%window-close ")
		return ControlModeEvent{Type: EventPaneExited, Window: win}, true
	}

	return ControlModeEvent{}, false
}

// parseOutput parses a "%output %<pane-id> <data>" line.
func parseOutput(line string) ControlModeEvent {
	rest := strings.TrimPrefix(line, "%output ")
	spaceIdx := strings.IndexByte(rest, ' ')

	var paneID, dataStr string
	if spaceIdx < 0 {
		paneID = rest
		dataStr = ""
	} else {
		paneID = rest[:spaceIdx]
		dataStr = rest[spaceIdx+1:]
	}

	var data []byte
	if dataStr == "" {
		data = []byte{}
	} else {
		decoded, err := base64.StdEncoding.DecodeString(dataStr)
		if err == nil {
			data = decoded
		} else {
			data = []byte(dataStr)
		}
	}

	return ControlModeEvent{
		Type:   EventOutput,
		PaneID: paneID,
		Data:   data,
	}
}
