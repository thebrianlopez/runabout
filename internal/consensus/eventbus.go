package consensus

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// JSONLEventBus implements EventBus by appending to daily JSONL files.
// It mirrors the format used by ~/.automation-metrics/events/.
// prev_hash chaining is a no-op until M1 ships the hash chain extension.
type JSONLEventBus struct {
	dir string
}

// NewJSONLEventBus returns an EventBus that writes to dir (defaults to
// ~/.automation-metrics/events/ if empty).
func NewJSONLEventBus(dir string) EventBus {
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".automation-metrics", "events")
	}
	return &JSONLEventBus{dir: dir}
}

func (b *JSONLEventBus) Append(_ context.Context, eventType string, payload map[string]any) error {
	if err := os.MkdirAll(b.dir, 0o755); err != nil {
		return fmt.Errorf("eventbus mkdir: %w", err)
	}
	payload["event_type"] = eventType
	payload["timestamp"] = time.Now().UTC().Format("20060102T150405Z")
	payload["schema_version"] = "2"
	payload["layer"] = "go_cli"

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("eventbus marshal: %w", err)
	}

	path := filepath.Join(b.dir, time.Now().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("eventbus open: %w", err)
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%s\n", data)
	return err
}

// ScanConsensusEvents returns all events matching eventType for the given artifactPath
// found in any JSONL file in the bus directory at or after sinceDate.
func ScanConsensusEvents(dir, eventType, artifactPath string, since time.Time) ([]map[string]any, error) {
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".automation-metrics", "events")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read events dir: %w", err)
	}

	var results []map[string]any
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		// Parse date from filename (YYYY-MM-DD.jsonl).
		name := entry.Name()
		if len(name) < 10 {
			continue
		}
		fileDate, err := time.Parse("2006-01-02", name[:10])
		if err != nil {
			continue
		}
		if fileDate.Before(since.Truncate(24 * time.Hour)) {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		rows, err := scanJSONLFile(path, eventType, artifactPath)
		if err != nil {
			continue // best-effort; corrupted file skipped
		}
		results = append(results, rows...)
	}
	return results, nil
}

func scanJSONLFile(path, eventType, artifactPath string) ([]map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var results []map[string]any
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1MB line buffer
	for scanner.Scan() {
		line := scanner.Bytes()
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if eventType != "" && m["event_type"] != eventType {
			continue
		}
		if artifactPath != "" {
			ap, _ := m["artifact_path"].(string)
			if ap != artifactPath {
				continue
			}
		}
		results = append(results, m)
	}
	return results, scanner.Err()
}
