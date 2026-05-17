package ai

import (
	"context"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/insights"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/pipeline"
)

// RulesExtractor implements pipeline.Extractor at TierRules.
// It extracts a SignalSet from a slice of Events using deterministic rules for
// structured payloads (shell_cmd, audit_event, ai_activity). After each call
// it emits a metrics record to ~/.automation-metrics/local-ai.log.
type RulesExtractor struct {
	metrics pipeline.MetricsWriter
}

// NewRulesExtractor constructs a RulesExtractor.
// metrics may be nil; if nil, a FileMetrics backed by DefaultMetricsPath() is used.
// Pass pipeline.DiscardMetrics{} to silence metrics emission.
func NewRulesExtractor(metrics pipeline.MetricsWriter) *RulesExtractor {
	if metrics == nil {
		path := pipeline.DefaultMetricsPath()
		if path != "" {
			metrics = pipeline.NewFileMetrics(path)
		} else {
			metrics = pipeline.DiscardMetrics{}
		}
	}
	return &RulesExtractor{metrics: metrics}
}

// Tier implements pipeline.Extractor.
func (e *RulesExtractor) Tier() pipeline.Tier { return pipeline.TierRules }

// Extract derives a SignalSet from the given events.
// Events with structured payloads (shell_cmd, audit_event, ai_activity) are
// processed deterministically using the insights extractors. Events of unknown
// kinds are silently ignored — the interface accepts mixed-source slices.
// After each call it emits a metrics record to local-ai.log; metric emission
// errors are silently dropped per EPIC-016 spec.
func (e *RulesExtractor) Extract(ctx context.Context, events []pipeline.Event) (*insights.SignalSet, error) {
	start := time.Now()

	// Separate events by kind into typed slices.
	var shellCmds []models.ShellCommand
	var auditEvts []models.AuditEvent
	var aiActivity []models.AIActivity

	for _, ev := range events {
		switch ev.Kind {
		case "shell_cmd":
			if cmd, ok := ev.Payload.(models.ShellCommand); ok {
				shellCmds = append(shellCmds, cmd)
			}
		case "audit_event":
			if ae, ok := ev.Payload.(models.AuditEvent); ok {
				auditEvts = append(auditEvts, ae)
			}
		case "ai_activity":
			if aa, ok := ev.Payload.(models.AIActivity); ok {
				aiActivity = append(aiActivity, aa)
			}
		}
		// Unknown kinds (e.g. "issue", "pr") are intentionally ignored;
		// those events carry payloads for future extractors.
	}

	// Build signal set from structured payloads using deterministic extractors.
	s := &insights.SignalSet{
		ThemeCounts: make(map[insights.Theme]int),
	}
	s.ShellActivity = insights.ExtractShellSignals(shellCmds)
	s.AIActivity = insights.ExtractAISignals(aiActivity, auditEvts, nil)

	// signalCount reflects the volume of structured activity processed.
	signalCount := s.ShellActivity.TotalCommands + s.AIActivity.TotalSessions

	latency := time.Since(start).Milliseconds()

	// Best-effort metrics emission; errors are silently dropped per EPIC-016 spec.
	_ = e.metrics.Write(pipeline.MetricsRecord{
		Timestamp:     time.Now().UTC(),
		Task:          "workctl_extract",
		LatencyMS:     latency,
		ConfidenceAvg: 1.0, // deterministic extraction has full confidence
		SignalCount:   signalCount,
		Model:         "rules",
		TaskType:      "weekly_signals",
	})

	return s, nil
}

// CloudExtractor is a stub satisfying pipeline.Extractor at TierCloud.
// Cloud LLM fallback is deferred to a future epic.
type CloudExtractor struct{}

// Tier implements pipeline.Extractor.
func (CloudExtractor) Tier() pipeline.Tier { return pipeline.TierCloud }

// Extract implements pipeline.Extractor.
// Returns ErrNotImplemented until a cloud LLM client is wired.
func (CloudExtractor) Extract(_ context.Context, _ []pipeline.Event) (*insights.SignalSet, error) {
	return nil, pipeline.ErrNotImplemented
}
