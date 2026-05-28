package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"time"
)

// Event is a structured JSONL event following emit_jsonl conventions.
type Event struct {
	EventType string                 `json:"event_type"`
	Timestamp string                 `json:"timestamp"`
	Metadata  map[string]interface{} `json:"metadata"`
}

// EventLogger appends JSONL events to a file using a persistent file handle.
// The mutex serializes all writes; the handle is opened once in NewEventLogger
// and closed via Close(). This eliminates per-event open/close syscall overhead
// and fd exhaustion risk under burst traffic.
//
// SIGHUP note: if the events log path changes on config reload, call Close()
// in the SIGHUP handler and create a new EventLogger pointing at the new path.
type EventLogger struct {
	mu   sync.Mutex
	path string
	f    *os.File // persistent handle; nil if open failed at construction
}

// NewEventLogger creates a logger that appends to the given file path.
// The parent directory is created if it doesn't exist.
func NewEventLogger(path string) (*EventLogger, error) {
	// Derive parent directory by scanning backwards for the last '/'.
	dir := "."
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			dir = path[:i]
			break
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating event log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}
	return &EventLogger{path: path, f: f}, nil
}

// Emit appends a JSONL event to the log file.
func (l *EventLogger) Emit(eventType string, metadata map[string]interface{}) error {
	slog.Debug("event_bus_emit", "event_type", eventType, "metadata", metadata)

	ev := Event{
		EventType: eventType,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Metadata:  metadata,
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.f == nil {
		return fmt.Errorf("event log not open")
	}
	_, err = l.f.Write(data)
	return err
}

// Close flushes and closes the underlying file handle. Should be called on
// server shutdown or before creating a new EventLogger for the same path
// (e.g. after a SIGHUP that changes the log path).
func (l *EventLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// domainFromURL extracts the hostname from a URL string, returning "" on error.
func domainFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
