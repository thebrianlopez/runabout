package api

import (
	"context"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/pipeline"
)

// EventsSource wraps EventsClient to satisfy pipeline.Source.
// It emits all event types from the events store as pipeline.Events,
// using Kind to distinguish event types (matches JSONL event_type values).
type EventsSource struct{ c *EventsClient }

// NewEventsSource returns a Source that reads the automation-metrics events store.
func NewEventsSource() *EventsSource {
	return &EventsSource{c: NewEventsClient()}
}

// Name implements pipeline.Source.
func (s *EventsSource) Name() string { return "events" }

// Fetch implements pipeline.Source.
func (s *EventsSource) Fetch(_ context.Context, start, end time.Time) ([]pipeline.Event, error) {
	batch, err := s.c.GetTypedEvents(start.Format(sourceDateLayout), end.Format(sourceDateLayout))
	if err != nil {
		return nil, err
	}
	var events []pipeline.Event
	for _, e := range batch.ShellEvents {
		events = append(events, pipeline.Event{Source: s.Name(), Timestamp: e.Timestamp, Kind: "shell_command", Payload: e})
	}
	for _, e := range batch.ToolCallEvents {
		events = append(events, pipeline.Event{Source: s.Name(), Timestamp: e.Timestamp, Kind: "tool_result", Payload: e})
	}
	for _, e := range batch.InferenceEvents {
		events = append(events, pipeline.Event{Source: s.Name(), Timestamp: e.Timestamp, Kind: "inference", Payload: e})
	}
	for _, e := range batch.SessionSummaries {
		events = append(events, pipeline.Event{Source: s.Name(), Timestamp: e.Timestamp, Kind: "session_summary", Payload: e})
	}
	return events, nil
}

const sourceDateLayout = "2006-01-02"

// FishHistorySource wraps FishHistoryClient to satisfy pipeline.Source.
type FishHistorySource struct{ c *FishHistoryClient }

// NewFishHistorySource returns a Source that reads fish shell history.
func NewFishHistorySource() *FishHistorySource {
	return &FishHistorySource{c: NewFishHistoryClient()}
}

// Name implements pipeline.Source.
func (s *FishHistorySource) Name() string { return "fish_history" }

// Fetch implements pipeline.Source.
func (s *FishHistorySource) Fetch(_ context.Context, start, end time.Time) ([]pipeline.Event, error) {
	cmds, err := s.c.GetCommands(start.Format(sourceDateLayout), end.Format(sourceDateLayout))
	if err != nil {
		return nil, err
	}
	events := make([]pipeline.Event, len(cmds))
	for i, cmd := range cmds {
		events[i] = pipeline.Event{
			Source:    s.Name(),
			Timestamp: cmd.Timestamp,
			Kind:      "shell_cmd",
			Payload:   cmd,
		}
	}
	return events, nil
}

// AuditLogSource wraps AuditLogClient to satisfy pipeline.Source.
type AuditLogSource struct{ c *AuditLogClient }

// NewAuditLogSource returns a Source that reads the terminal audit log.
func NewAuditLogSource() *AuditLogSource {
	return &AuditLogSource{c: NewAuditLogClient()}
}

// Name implements pipeline.Source.
func (s *AuditLogSource) Name() string { return "audit_log" }

// Fetch implements pipeline.Source.
func (s *AuditLogSource) Fetch(_ context.Context, start, end time.Time) ([]pipeline.Event, error) {
	evts, err := s.c.GetEvents(start.Format(sourceDateLayout), end.Format(sourceDateLayout))
	if err != nil {
		return nil, err
	}
	events := make([]pipeline.Event, len(evts))
	for i, e := range evts {
		events[i] = pipeline.Event{
			Source:    s.Name(),
			Timestamp: e.Timestamp,
			Kind:      "audit_event",
			Payload:   e,
		}
	}
	return events, nil
}

// ClaudeStatsSource wraps ClaudeStatsClient to satisfy pipeline.Source.
type ClaudeStatsSource struct{ c *ClaudeStatsClient }

// NewClaudeStatsSource returns a Source that reads Claude Code stats.
func NewClaudeStatsSource() *ClaudeStatsSource {
	return &ClaudeStatsSource{c: NewClaudeStatsClient()}
}

// Name implements pipeline.Source.
func (s *ClaudeStatsSource) Name() string { return "claude_stats" }

// Fetch implements pipeline.Source.
func (s *ClaudeStatsSource) Fetch(_ context.Context, start, end time.Time) ([]pipeline.Event, error) {
	activity, err := s.c.GetActivity(start.Format(sourceDateLayout), end.Format(sourceDateLayout))
	if err != nil {
		return nil, err
	}
	events := make([]pipeline.Event, len(activity))
	for i, a := range activity {
		ts, _ := time.Parse("2006-01-02", a.Date)
		events[i] = pipeline.Event{
			Source:    s.Name(),
			Timestamp: ts,
			Kind:      "ai_activity",
			Payload:   a,
		}
	}
	return events, nil
}
