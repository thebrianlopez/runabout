package export

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/insights"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/pipeline"
)

// SignalsEnvelope is the top-level JSON schema for .signals.json sidecar files.
// schema_version is a string to allow non-breaking suffixes (e.g. "1.1").
// Consumers must tolerate unknown fields (standard Go JSON unmarshalling).
type SignalsEnvelope struct {
	SchemaVersion string              `json:"schema_version"` // always "1"
	Generated     string              `json:"generated"`      // RFC3339 UTC
	PeriodStart   string              `json:"period_start"`   // YYYY-MM-DD
	PeriodEnd     string              `json:"period_end"`     // YYYY-MM-DD
	ReportType    string              `json:"report_type"`    // "weekly" | "quarterly" | "review"
	ExtractorTier string              `json:"extractor_tier"` // "rules" | "local_ai" | "cloud"
	Signals       *insights.SignalSet `json:"signals"`
}

// FileSink writes a .signals.json sidecar file alongside each report file.
// The sidecar path is derived by replacing the output file's extension with
// ".signals.json" (e.g. "weekly.md" → "weekly.signals.json").
//
// FileSink must be placed BEFORE ConfluentSink in a MultiSink to enforce the
// file-canonical record model.
type FileSink struct{}

// NewFileSink returns a Sink that writes .signals.json sidecar files.
func NewFileSink() *FileSink { return &FileSink{} }

// Name implements pipeline.Sink.
func (s *FileSink) Name() string { return "file" }

// Write serializes the SignalSet to a .signals.json file beside the report.
// If rd.OutputPath is empty or rd.Signals is nil, Write is a no-op (returns nil).
func (s *FileSink) Write(_ context.Context, rd *pipeline.ReportData) error {
	if rd.OutputPath == "" || rd.Signals == nil {
		return nil
	}

	env := SignalsEnvelope{
		SchemaVersion: "1",
		Generated:     rd.Generated.UTC().Format(time.RFC3339),
		PeriodStart:   rd.PeriodStart.Format("2006-01-02"),
		PeriodEnd:     rd.PeriodEnd.Format("2006-01-02"),
		ReportType:    rd.ReportType,
		ExtractorTier: "local_ai",
		Signals:       rd.Signals,
	}

	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}

	jsonPath := SignalsPath(rd.OutputPath)
	if err := os.MkdirAll(filepath.Dir(jsonPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(jsonPath, data, 0o600)
}

// SignalsPath derives the .signals.json path from a report output path.
// Strips the existing extension and appends ".signals.json".
//
//	"weekly.md"  → "weekly.signals.json"
//	"report.pdf" → "report.signals.json"
//	"weekly"     → "weekly.signals.json"
func SignalsPath(outputPath string) string {
	ext := filepath.Ext(outputPath)
	base := strings.TrimSuffix(outputPath, ext)
	return base + ".signals.json"
}
