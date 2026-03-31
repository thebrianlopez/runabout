package main

import (
	"encoding/json"
	"fmt"
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

// EventLogger appends JSONL events to a file.
type EventLogger struct {
	mu   sync.Mutex
	path string
}

// NewEventLogger creates a logger that appends to the given file path.
// The parent directory is created if it doesn't exist.
func NewEventLogger(path string) (*EventLogger, error) {
	dir := path[:max(0, len(path)-len("/linkari_events.jsonl"))]
	if dir == "" {
		dir = "."
	}
	// Use filepath.Dir logic inline to avoid import for one call.
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			dir = path[:i]
			break
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating event log directory: %w", err)
	}
	return &EventLogger{path: path}, nil
}

// Emit appends a JSONL event to the log file.
func (l *EventLogger) Emit(eventType string, metadata map[string]interface{}) error {
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

	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer f.Close()

	_, err = f.Write(data)
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
