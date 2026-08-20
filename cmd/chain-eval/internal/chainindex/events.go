package chainindex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Event types emitted to the automation-metrics bus (TDD section 9).
const (
	EventSchemaViolation     = "schema_violation"
	EventStatusValueObserved = "status_value_observed"
)

const (
	eventSchemaVersion = "2"
	eventLayer         = "go_cli"
	eventClass         = "background"
	eventCommand       = "chain-eval index"
	eventTimeLayout    = "20060102T150405Z"
	eventFileLayout    = "2006-01-02"
)

// busEvent is one automation-metrics envelope. Metadata is flattened into the
// envelope by the writer so consumers can filter on rule/value directly.
type busEvent struct {
	SchemaVersion string `json:"schema_version"`
	Timestamp     string `json:"timestamp"`
	Layer         string `json:"layer"`
	EventType     string `json:"event_type"`
	EventClass    string `json:"event_class"`
	Command       string `json:"command"`
	User          string `json:"user,omitempty"`

	ArtifactType string `json:"artifact_type"`
	Rule         string `json:"rule,omitempty"`
	Detected     string `json:"detected,omitempty"`
	Expected     string `json:"expected,omitempty"`
	Value        string `json:"value,omitempty"`
	Conformant   *bool  `json:"conformant,omitempty"`
	Count        int    `json:"count"`
}

// EventsDir returns the automation-metrics events directory. The
// AUTOMATION_METRICS_DIR override exists so tests never write to the real bus.
func EventsDir() string {
	base := os.Getenv("AUTOMATION_METRICS_DIR")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".automation-metrics")
	}
	return filepath.Join(base, "events")
}

// EmitSchemaEvents appends schema_violation and status_value_observed events
// for one index build.
//
// Events are aggregated by their own carried fields and carry a count, rather
// than emitted once per artifact. Two reasons, both load-bearing:
//
// First, the TDD's field lists for these events (artifact_type, rule, detected,
// expected; artifact_type, value, conformant) contain no artifact identifier,
// so a per-artifact event is not individually attributable anyway - only the
// distribution is meaningful, which is exactly what enum tuning consumes.
//
// Second, volume. The live corpus is ~1100 artifacts with several hundred
// violations, against a bus that carries 1800-3800 events on a normal day. Per
// artifact emission would add ~1500 events for a single command that is run
// many times a day, drowning the signal it exists to provide.
//
// Emission is forward-only and append-only. Historical JSONL is never read,
// edited or backfilled. Failure to emit never fails the build.
func EmitSchemaEvents(violations []SchemaViolation, records []ArtifactRecord) error {
	dir := EventsDir()
	if dir == "" {
		return nil
	}
	events := buildSchemaEvents(violations, records, nowFunc())
	if len(events) == 0 {
		return nil
	}
	return appendEvents(dir, events)
}

// buildSchemaEvents is pure so ordering and aggregation are testable without
// touching a filesystem.
func buildSchemaEvents(violations []SchemaViolation, records []ArtifactRecord, now time.Time) []busEvent {
	ts := now.UTC().Format(eventTimeLayout)
	user := os.Getenv("USER")

	type violationKeyAgg struct {
		artifactType, rule, detected, expected string
	}
	violationCounts := map[violationKeyAgg]int{}
	// Only error-severity violations are emitted; warnings are legacy drift
	// that is already inventoried and would swamp the tuning signal.
	for _, v := range violations {
		if v.Severity != SeverityError {
			continue
		}
		violationCounts[violationKeyAgg{v.ArtifactType, v.Rule, v.Detected, v.Expected}]++
	}

	// An artifact's status is conformant when no status-class violation was
	// raised against it.
	nonConformant := map[string]bool{}
	for _, v := range violations {
		switch v.Rule {
		case RuleStatusEnum, RuleStatusUnparseable, RuleStatusAbsent:
			nonConformant[v.ArtifactPath] = true
		}
	}
	type statusKeyAgg struct {
		artifactType, value string
		conformant          bool
	}
	statusCounts := map[statusKeyAgg]int{}
	for _, r := range records {
		statusCounts[statusKeyAgg{string(r.Type), r.Status, !nonConformant[r.Path]}]++
	}

	events := make([]busEvent, 0, len(violationCounts)+len(statusCounts))
	for k, count := range violationCounts {
		events = append(events, busEvent{
			SchemaVersion: eventSchemaVersion,
			Timestamp:     ts,
			Layer:         eventLayer,
			EventType:     EventSchemaViolation,
			EventClass:    eventClass,
			Command:       eventCommand,
			User:          user,
			ArtifactType:  k.artifactType,
			Rule:          k.rule,
			Detected:      k.detected,
			Expected:      k.expected,
			Count:         count,
		})
	}
	for k, count := range statusCounts {
		conformant := k.conformant
		events = append(events, busEvent{
			SchemaVersion: eventSchemaVersion,
			Timestamp:     ts,
			Layer:         eventLayer,
			EventType:     EventStatusValueObserved,
			EventClass:    eventClass,
			Command:       eventCommand,
			User:          user,
			ArtifactType:  k.artifactType,
			Value:         k.value,
			Conformant:    &conformant,
			Count:         count,
		})
	}

	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.EventType != b.EventType {
			return a.EventType < b.EventType
		}
		if a.ArtifactType != b.ArtifactType {
			return a.ArtifactType < b.ArtifactType
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		if a.Detected != b.Detected {
			return a.Detected < b.Detected
		}
		return a.Value < b.Value
	})
	return events
}

// appendEvents opens today's daily file O_APPEND and writes one JSON object per
// line. Existing content is never rewritten.
func appendEvents(dir string, events []busEvent) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create events dir: %w", err)
	}
	path := filepath.Join(dir, nowFunc().UTC().Format(eventFileLayout)+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open events file: %w", err)
	}
	defer f.Close() //nolint:errcheck

	enc := json.NewEncoder(f)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return fmt.Errorf("encode event: %w", err)
		}
	}
	return nil
}
