package pipeline

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// MetricsWriter is the interface for emitting automation metrics lines.
// The default implementation (FileMetrics) appends to the local-ai.log file.
// Test implementations write to a bytes.Buffer via NewWriterMetrics.
type MetricsWriter interface {
	Write(record MetricsRecord) error
}

// MetricsRecord is one line in the local-ai.log pipe-delimited format:
//
//	{timestamp}|{task}|{latency_ms}|{confidence_avg}|{signal_count}|{model}|{task_type}
//
// This format is shared with automation-tracking.fish and must be kept in sync.
type MetricsRecord struct {
	Timestamp     time.Time
	Task          string
	LatencyMS     int64
	ConfidenceAvg float64
	SignalCount   int
	Model         string
	TaskType      string
}

// format returns the pipe-delimited log line (no trailing newline).
func (r MetricsRecord) format() string {
	return fmt.Sprintf("%s|%s|%d|%.2f|%d|%s|%s",
		r.Timestamp.UTC().Format("20060102T150405Z"),
		r.Task,
		r.LatencyMS,
		r.ConfidenceAvg,
		r.SignalCount,
		r.Model,
		r.TaskType,
	)
}

// WriterMetrics writes MetricsRecords as pipe-delimited log lines to any io.Writer.
// Used in tests to capture metrics without touching the filesystem.
type WriterMetrics struct {
	w io.Writer
}

// NewWriterMetrics returns a MetricsWriter backed by the given io.Writer.
func NewWriterMetrics(w io.Writer) *WriterMetrics {
	return &WriterMetrics{w: w}
}

// Write formats and writes the record, appending a newline.
func (m *WriterMetrics) Write(r MetricsRecord) error {
	_, err := fmt.Fprintln(m.w, r.format())
	return err
}

// FileMetrics appends MetricsRecords to a file, creating it if absent.
// This is the production implementation used by RulesExtractor.
type FileMetrics struct {
	path string
}

// NewFileMetrics returns a FileMetrics that appends to the given path.
// The default production path is ~/.automation-metrics/local-ai.log.
func NewFileMetrics(path string) *FileMetrics {
	return &FileMetrics{path: path}
}

// DefaultMetricsPath returns the standard local-ai.log path under the user's home directory.
// Returns an empty string if the home directory cannot be determined.
func DefaultMetricsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".automation-metrics", "local-ai.log")
}

// Write appends the formatted record to the log file.
// Creates the parent directory and file if absent (O_APPEND|O_CREATE semantics).
// Write errors are returned to the caller; RulesExtractor must treat them as non-fatal.
func (m *FileMetrics) Write(r MetricsRecord) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(m.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, r.format())
	return err
}

// DiscardMetrics is a no-op MetricsWriter. Useful when metrics emission is intentionally disabled.
type DiscardMetrics struct{}

// Write implements MetricsWriter by doing nothing.
func (DiscardMetrics) Write(MetricsRecord) error { return nil }
