package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

// EventsClient reads ~/.automation-metrics/events/ JSONL files and returns
// all event types as a TypedEventBatch in a single pass per file.
//
// It supersedes the dual-pass approach in AuditLogClient:
//   - AuditLogClient.GetEvents()           → now EventsClient.GetTypedEvents().ShellEvents
//   - AuditLogClient.GetSessionSummaries() → now EventsClient.GetTypedEvents().SessionSummaries
//
// Deprecated: AuditLogClient — use EventsClient for post-2026-01-23 data.
// Deprecated: ClaudeStatsClient — EventsClient provides richer session-level data;
// retain ClaudeStatsClient only as fallback for pre-2026-01-23 date ranges.
type EventsClient struct {
	dir string
}

// NewEventsClient returns a client reading from the default automation-metrics
// events directory (~/.automation-metrics/events/).
func NewEventsClient() *EventsClient {
	home, _ := os.UserHomeDir()
	return &EventsClient{
		dir: filepath.Join(home, ".automation-metrics", "events"),
	}
}

// newEventsClientAt returns a client reading from a custom directory (for testing).
func newEventsClientAt(dir string) *EventsClient {
	return &EventsClient{dir: dir}
}

// eventsRaw is the raw JSON shape common to all event types in the events store.
// Fields present vary by event_type; absent fields zero-value cleanly.
type eventsRaw struct {
	SchemaVersion string          `json:"schema_version"`
	Version       string          `json:"version"`
	Timestamp     string          `json:"timestamp"`
	EventType     string          `json:"event_type"`
	Command       string          `json:"command"`
	Source        string          `json:"source"`
	Layer         string          `json:"layer"`
	SessionID     string          `json:"session_id"`
	Cwd           string          `json:"cwd"`
	ToolName      string          `json:"tool_name"`
	Metadata      json.RawMessage `json:"metadata"`
}

// toolResultMeta is the metadata shape for event_type == "tool_result".
type toolResultMeta struct {
	GraduationCandidate bool   `json:"graduation_candidate"`
	FirstWord           string `json:"first_word"`
	ToolName            string `json:"tool_name"`
}

// GetTypedEvents reads all JSONL files in [startDate, endDate] (YYYY-MM-DD)
// from the events directory and returns a TypedEventBatch.
// Malformed JSON lines and absent files are silently skipped (NF4).
func (c *EventsClient) GetTypedEvents(startDate, endDate string) (*models.TypedEventBatch, error) {
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return nil, fmt.Errorf("invalid start date %q: %w", startDate, err)
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return nil, fmt.Errorf("invalid end date %q: %w", endDate, err)
	}
	endInclusive := end.Add(24*time.Hour - time.Second)

	batch := &models.TypedEventBatch{}
	// Accumulate inference costs per session_id to attach to session summaries.
	sessionCosts := make(map[string]float64)

	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		filename := filepath.Join(c.dir, d.Format("2006-01-02")+".jsonl")
		if err := parseEventsFile(filename, start.Unix(), endInclusive.Unix(), batch, sessionCosts); err != nil {
			return nil, err
		}
	}

	// Attach accumulated costs to session summaries.
	for i := range batch.SessionSummaries {
		if cost, ok := sessionCosts[batch.SessionSummaries[i].SessionID]; ok {
			batch.SessionSummaries[i].CostEstimateUSD = cost
		}
	}

	return batch, nil
}

// parseEventsFile reads a single JSONL events file and appends typed events
// into batch. sessionCosts accumulates inference costs per session_id for
// later attachment to SessionSummaries. Absent files are not an error (NF4).
func parseEventsFile(path string, startEpoch, endEpoch int64, batch *models.TypedEventBatch, sessionCosts map[string]float64) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open events file %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var raw eventsRaw
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue // skip malformed lines
		}
		ts, err := parseAuditTimestamp(raw.Timestamp)
		if err != nil {
			continue
		}
		if ts.Unix() < startEpoch || ts.Unix() > endEpoch {
			continue
		}

		layer := resolveLayer(raw)

		switch raw.EventType {
		case "session_summary":
			var meta sessionSummaryMetadata
			if err := json.Unmarshal(raw.Metadata, &meta); err != nil {
				continue
			}
			firstEvent, _ := parseAuditTimestamp(meta.FirstEvent)
			lastEvent, _ := parseAuditTimestamp(meta.LastEvent)
			batch.SessionSummaries = append(batch.SessionSummaries, models.SessionSummary{
				SessionID:            raw.SessionID,
				Cwd:                  raw.Cwd,
				Timestamp:            ts,
				TotalEvents:          meta.TotalEvents,
				ToolEvents:           meta.ToolEvents,
				PromptCount:          meta.PromptCount,
				UniqueCommands:       meta.UniqueCommands,
				ToolDistribution:     meta.ToolDistribution,
				GraduationCandidates: meta.GraduationCandidates,
				FirstEvent:           firstEvent,
				LastEvent:            lastEvent,
			})

		case "inference":
			var meta inferenceMetadata
			if err := json.Unmarshal(raw.Metadata, &meta); err != nil {
				continue
			}
			batch.InferenceEvents = append(batch.InferenceEvents, models.InferenceEvent{
				Timestamp:       ts,
				SessionID:       raw.SessionID,
				CostEstimateUSD: meta.CostEstimateUSD,
				TokenEstimate:   meta.TokenEstimate,
			})
			if meta.CostEstimateUSD > 0 && raw.SessionID != "" {
				sessionCosts[raw.SessionID] += meta.CostEstimateUSD
			}

		case "tool_result":
			var meta toolResultMeta
			if raw.Metadata != nil {
				_ = json.Unmarshal(raw.Metadata, &meta) // best-effort; missing metadata is fine
			}
			toolName := raw.ToolName
			if toolName == "" {
				toolName = meta.ToolName
			}
			batch.ToolCallEvents = append(batch.ToolCallEvents, models.ToolCallEvent{
				Timestamp:           ts,
				SessionID:           raw.SessionID,
				Cwd:                 raw.Cwd,
				ToolName:            toolName,
				Command:             strings.TrimSpace(raw.Command),
				FirstWord:           meta.FirstWord,
				GraduationCandidate: meta.GraduationCandidate,
			})

		default:
			// All other event types (shell_command, function_call, legacy v1/v2) that
			// carry a command are treated as shell events.
			cmd := strings.TrimSpace(raw.Command)
			if cmd == "" {
				continue
			}
			batch.ShellEvents = append(batch.ShellEvents, models.ShellEvent{
				Timestamp: ts,
				Command:   redactSensitive(cmd),
				SessionID: raw.SessionID,
				Cwd:       raw.Cwd,
				Layer:     layer,
				ToolName:  raw.ToolName,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading events file %s: %w", path, err)
	}
	return nil
}

// ShellEventsToAuditEvents converts a slice of ShellEvents to the legacy AuditEvent type.
// Used as a bridge until E3 (ExtractLocalSignals) replaces the split extraction functions.
// Layer names are mapped back to Source values that the existing signal extraction expects:
//
//	"fish"             → "interactive_shell"
//	"cloud_llm"        → "claude_code"
//	everything else   → passed through as-is
func ShellEventsToAuditEvents(shell []models.ShellEvent) []models.AuditEvent {
	result := make([]models.AuditEvent, len(shell))
	for i, s := range shell {
		result[i] = models.AuditEvent{
			Timestamp: s.Timestamp,
			Command:   s.Command,
			SessionID: s.SessionID,
			Cwd:       s.Cwd,
			Source:    layerToSource(s.Layer),
			ToolName:  s.ToolName,
		}
	}
	return result
}

// layerToSource maps v3 layer names back to the Source values used by signal extraction.
func layerToSource(layer string) string {
	switch layer {
	case "fish":
		return "interactive_shell"
	case "cloud_llm":
		return "claude_code"
	default:
		return layer // "interactive_shell", "go_cli", etc. pass through
	}
}

// resolveLayer returns the canonical layer string for an event.
// v3 events use "layer"; v1/v2 events use "source". For v1/v2, map
// "interactive_shell" → "interactive_shell" and "claude_code" → "cloud_llm"
// so callers always see consistent v3-style layer names.
func resolveLayer(raw eventsRaw) string {
	if raw.Layer != "" {
		return raw.Layer
	}
	switch raw.Source {
	case "claude_code":
		return "cloud_llm"
	default:
		return raw.Source // "interactive_shell" and others pass through as-is
	}
}
