package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// auditEventWithMetadata extends auditEventRaw with raw metadata for
// session_summary and inference event parsing.
type auditEventWithMetadata struct {
	auditEventRaw
	Metadata json.RawMessage `json:"metadata"`
}

// sessionSummaryMetadata is the JSON shape of session_summary event metadata.
type sessionSummaryMetadata struct {
	TotalEvents          int            `json:"total_events"`
	ToolEvents           int            `json:"tool_events"`
	PromptCount          int            `json:"prompt_count"`
	UniqueCommands       int            `json:"unique_commands"`
	ToolDistribution     map[string]int `json:"tool_distribution"`
	GraduationCandidates int            `json:"graduation_candidates"`
	FirstEvent           string         `json:"first_event"`
	LastEvent            string         `json:"last_event"`
}

// inferenceMetadata is the JSON shape of inference event metadata.
type inferenceMetadata struct {
	CostEstimateUSD float64 `json:"cost_estimate_usd"`
	TokenEstimate   int     `json:"token_estimate"`
}

// AuditLogClient reads commands from event log directories.
//
// Deprecated: use EventsClient for post-2026-01-23 data. AuditLogClient
// remains available as a fallback for pre-events-era date ranges and for
// tests that verify the legacy parsing path.
//
// Primary: ~/.automation-metrics/events/ (unified telemetry pipeline).
// Fallback: ~/Downloads/terminal-history/ (legacy audit log).
type AuditLogClient struct {
	dirs []string // searched in order; first file found wins per day
}

// NewAuditLogClient returns a client that reads from the automation-metrics
// events directory (primary) with fallback to the legacy terminal-history path.
func NewAuditLogClient() *AuditLogClient {
	home, _ := os.UserHomeDir()
	return &AuditLogClient{
		dirs: []string{
			filepath.Join(home, ".automation-metrics", "events"),
			filepath.Join(home, "Downloads", "terminal-history"),
		},
	}
}

// newAuditLogClientAt returns a client reading from a single directory (for testing).
func newAuditLogClientAt(dir string) *AuditLogClient {
	return &AuditLogClient{dirs: []string{dir}}
}

// auditEventRaw is the on-disk JSON schema.
// Handles three schema generations:
//   - 2025 (v1): version, no session_id/cwd, space-separated timestamp
//   - 2026-early (v2): version, session_id/cwd, ISO 8601 timestamp
//   - 2026-late (v3/events): schema_version, layer instead of source, compact UTC timestamp
type auditEventRaw struct {
	Version       string `json:"version"`
	SchemaVersion string `json:"schema_version"`
	Timestamp     string `json:"timestamp"`
	EventType     string `json:"event_type"`
	Command       string `json:"command"`
	Source        string `json:"source"` // v1/v2: "interactive_shell" or "claude_code"
	Layer         string `json:"layer"`  // v3: "fish" or "cloud_llm"
	// v2+ fields — zero-value when absent in older files.
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	User      string `json:"user"`
	ToolName  string `json:"tool_name"`
}

// GetEvents returns all audit events in [startDate, endDate] (YYYY-MM-DD).
// Searches directories in priority order; uses the first file found per day.
// Returns an empty slice (not an error) when no log directories exist (NF4).
func (c *AuditLogClient) GetEvents(startDate, endDate string) ([]models.AuditEvent, error) {
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return nil, fmt.Errorf("invalid start date %q: %w", startDate, err)
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return nil, fmt.Errorf("invalid end date %q: %w", endDate, err)
	}
	endInclusive := end.Add(24*time.Hour - time.Second)

	var result []models.AuditEvent
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dayFile := d.Format("2006-01-02") + ".jsonl"
		for _, dir := range c.dirs {
			filename := filepath.Join(dir, dayFile)
			events, err := parseAuditLogFile(filename, start.Unix(), endInclusive.Unix())
			if err != nil {
				return nil, err
			}
			if len(events) > 0 {
				result = append(result, events...)
				break // found data in this directory; skip lower-priority dirs
			}
		}
	}
	return result, nil
}

// parseAuditLogFile reads one JSONL audit log file.
// Returns empty (not error) when the file is absent (NF4).
func parseAuditLogFile(path string, startEpoch, endEpoch int64) ([]models.AuditEvent, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}
	defer f.Close()

	var result []models.AuditEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1 MB — handles long commands

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var raw auditEventRaw
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
		result = append(result, models.AuditEvent{
			Timestamp: ts,
			Command:   redactSensitive(strings.TrimSpace(raw.Command)),
			SessionID: raw.SessionID,
			Cwd:       raw.Cwd,
			Source:    resolveSource(raw),
			ToolName:  raw.ToolName,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading audit log %s: %w", path, err)
	}
	return result, nil
}

// resolveSource normalises the event source across schema versions.
// v1/v2 use "source" directly; v3 (events pipeline) uses "layer".
func resolveSource(raw auditEventRaw) string {
	if raw.Source != "" {
		return raw.Source
	}
	switch raw.Layer {
	case "fish":
		return "interactive_shell"
	case "cloud_llm":
		return "claude_code"
	default:
		return raw.Layer // pass-through for unknown layers
	}
}

// GetSessionSummaries returns session summaries and per-session cost estimates
// from session_summary + inference events in [startDate, endDate].
// It reads the same JSONL files as GetEvents but extracts different event types.
func (c *AuditLogClient) GetSessionSummaries(startDate, endDate string) ([]models.SessionSummary, error) {
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return nil, fmt.Errorf("invalid start date %q: %w", startDate, err)
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return nil, fmt.Errorf("invalid end date %q: %w", endDate, err)
	}
	endInclusive := end.Add(24*time.Hour - time.Second)

	// First pass: collect inference costs per session_id.
	sessionCosts := make(map[string]float64)
	// Second pass result: session summaries.
	var summaries []models.SessionSummary

	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dayFile := d.Format("2006-01-02") + ".jsonl"
		for _, dir := range c.dirs {
			filename := filepath.Join(dir, dayFile)
			sums, costs, err := parseSessionSummaryFile(filename, start.Unix(), endInclusive.Unix())
			if err != nil {
				return nil, err
			}
			if len(sums) > 0 || len(costs) > 0 {
				summaries = append(summaries, sums...)
				for sid, cost := range costs {
					sessionCosts[sid] += cost
				}
				break // primary dir found data
			}
		}
	}

	// Attach accumulated inference costs to their session summaries.
	for i := range summaries {
		if cost, ok := sessionCosts[summaries[i].SessionID]; ok {
			summaries[i].CostEstimateUSD = cost
		}
	}

	return summaries, nil
}

// parseSessionSummaryFile reads one JSONL file, extracting session_summary
// events and inference cost estimates. Returns summaries and a map of
// session_id → accumulated cost from inference events.
func parseSessionSummaryFile(path string, startEpoch, endEpoch int64) ([]models.SessionSummary, map[string]float64, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open audit log %s: %w", path, err)
	}
	defer f.Close()

	var summaries []models.SessionSummary
	costs := make(map[string]float64)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var raw auditEventWithMetadata
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		ts, err := parseAuditTimestamp(raw.Timestamp)
		if err != nil {
			continue
		}
		if ts.Unix() < startEpoch || ts.Unix() > endEpoch {
			continue
		}

		switch raw.EventType {
		case "session_summary":
			var meta sessionSummaryMetadata
			if err := json.Unmarshal(raw.Metadata, &meta); err != nil {
				continue
			}
			firstEvent, _ := parseAuditTimestamp(meta.FirstEvent)
			lastEvent, _ := parseAuditTimestamp(meta.LastEvent)
			summaries = append(summaries, models.SessionSummary{
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
			if meta.CostEstimateUSD > 0 && raw.SessionID != "" {
				costs[raw.SessionID] += meta.CostEstimateUSD
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading audit log %s: %w", path, err)
	}
	return summaries, costs, nil
}

// parseAuditTimestamp handles timestamp formats across schema generations:
//   - 2025 (v1): "2025-04-01 11:59:01"         (space-separated, treated as UTC)
//   - 2026-early (v2): "2026-02-23T09:26:35-0600" (ISO 8601, no colon in TZ offset)
//   - 2026-late (v3): "20260228T231954Z"         (compact UTC from emit_jsonl)
func parseAuditTimestamp(ts string) (time.Time, error) {
	// Compact UTC from automation-metrics events pipeline (e.g. 20260228T231954Z)
	if t, err := time.Parse("20060102T150405Z", ts); err == nil {
		return t, nil
	}
	// ISO 8601 with numeric offset, no colon (e.g. -0600)
	if t, err := time.Parse("2006-01-02T15:04:05-0700", ts); err == nil {
		return t, nil
	}
	// RFC3339 with colon offset (e.g. -06:00) — future-proof
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t, nil
	}
	// 2025 space-separated format (treated as UTC)
	return time.Parse("2006-01-02 15:04:05", ts)
}
