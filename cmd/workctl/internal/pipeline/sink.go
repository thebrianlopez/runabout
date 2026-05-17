package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/insights"
)

// ReportData is the pipeline-level data contract passed to Sinks.
// It carries the minimum fields needed by any output destination (file, Confluence, etc.).
// cmd/workctl converts its internal ReportData to *pipeline.ReportData before calling Sinks.
type ReportData struct {
	ReportType  string // "weekly" | "quarterly" | "review"
	Period      string // human-readable period label, e.g. "2026-02-20 to 2026-02-27"
	PeriodStart time.Time
	PeriodEnd   time.Time
	Generated   time.Time
	OutputPath  string              // filesystem path for file-based sinks
	Signals     *insights.SignalSet // extracted signals; may be nil for dry-run

	// HTML holds the pre-rendered Confluence storage-format HTML for publish sinks.
	// Empty when the report is not being published.
	HTML string
}

// Sink is a pluggable output destination for a completed ReportData.
type Sink interface {
	// Write serializes the report to the destination.
	// A Sink must not assume any other Sink has run first.
	Write(ctx context.Context, r *ReportData) error

	// Name returns a stable, lowercase identifier (e.g. "file", "confluence").
	Name() string
}

// MultiSink runs a slice of Sinks in declaration order.
// A failure in sink N does NOT prevent sinks N+1..M from running,
// but all errors are collected and returned as a joined error.
//
// Callers must place FileSink before ConfluentSink (or any remote sink) to
// establish the file-canonical record model: the local file is the authoritative
// record; remote publication is a secondary distribution step.
type MultiSink struct {
	sinks []Sink
}

// NewMultiSink returns a MultiSink that writes to each sink in the given order.
func NewMultiSink(sinks ...Sink) *MultiSink {
	return &MultiSink{sinks: sinks}
}

// Write calls each registered sink in declaration order.
// All sinks run regardless of individual failures; errors are joined.
func (m *MultiSink) Write(ctx context.Context, r *ReportData) error {
	errs := make([]error, 0, len(m.sinks))
	for _, s := range m.sinks {
		if err := s.Write(ctx, r); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Name implements Sink.
func (m *MultiSink) Name() string { return "multi" }
