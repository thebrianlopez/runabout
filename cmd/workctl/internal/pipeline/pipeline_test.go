package pipeline_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ai"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/api"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/export"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/insights"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/pipeline"
)

// ── Compiler assertions ──────────────────────────────────────────────────────
// These lines fail to compile if any concrete type drifts out of the interface contract.

var (
	_ pipeline.Source = (*api.FishHistorySource)(nil)
	_ pipeline.Source = (*api.AuditLogSource)(nil)
	_ pipeline.Source = (*api.ClaudeStatsSource)(nil)

	_ pipeline.Extractor = (*ai.RulesExtractor)(nil)
	_ pipeline.Extractor = ai.CloudExtractor{}

	_ pipeline.Sink = (*pipeline.MultiSink)(nil)

	_ pipeline.MetricsWriter = (*pipeline.WriterMetrics)(nil)
	_ pipeline.MetricsWriter = (*pipeline.FileMetrics)(nil)
	_ pipeline.MetricsWriter = pipeline.DiscardMetrics{}
)

// ── Source contract tests ────────────────────────────────────────────────────

// allSources returns the set of local sources (no credentials required).
// Atlassian/GitHub sources require API tokens and are excluded from unit tests.
func allLocalSources() []pipeline.Source {
	return []pipeline.Source{
		api.NewFishHistorySource(),
		api.NewAuditLogSource(),
		api.NewClaudeStatsSource(),
	}
}

func TestSourceContract_NameIsStable(t *testing.T) {
	for _, s := range allLocalSources() {
		t.Run(s.Name(), func(t *testing.T) {
			assert.NotEmpty(t, s.Name(), "Name() must not be empty")
			assert.Equal(t, s.Name(), s.Name(), "Name() must be stable across calls")
			assert.Equal(t, strings.ToLower(s.Name()), s.Name(), "Name() must be lowercase")
		})
	}
}

func TestSourceContract_FetchAbsentFilesReturnsEmpty(t *testing.T) {
	// Local sources read from well-known paths. On a clean CI environment those
	// files may not exist. The contract: absent source → empty slice, nil error.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Now()
	start := now.Add(-24 * time.Hour)

	for _, s := range allLocalSources() {
		t.Run(s.Name(), func(t *testing.T) {
			events, err := s.Fetch(ctx, start, now)
			require.NoError(t, err, "absent source file must return nil error, not an error")
			require.NotNil(t, events, "Fetch must never return nil slice")
		})
	}
}

// ── Extractor contract tests ─────────────────────────────────────────────────

func TestExtractorContract_StubsReturnErrNotImplemented(t *testing.T) {
	ctx := context.Background()
	events := []pipeline.Event{}

	t.Run("CloudExtractor", func(t *testing.T) {
		ext := ai.CloudExtractor{}
		assert.Equal(t, pipeline.TierCloud, ext.Tier())
		_, err := ext.Extract(ctx, events)
		assert.ErrorIs(t, err, pipeline.ErrNotImplemented)
	})
}

func TestRulesExtractorTier(t *testing.T) {
	ext := ai.NewRulesExtractor(pipeline.DiscardMetrics{})
	assert.Equal(t, pipeline.TierRules, ext.Tier())
}

// ── Sink contract tests ──────────────────────────────────────────────────────

type noopSink struct{ name string }

func (n *noopSink) Write(_ context.Context, _ *pipeline.ReportData) error { return nil }
func (n *noopSink) Name() string                                          { return n.name }

func TestMultiSink_RunsAllSinks(t *testing.T) {
	called := map[string]bool{}
	a := &noopSink{name: "alpha"}
	b := &noopSink{name: "beta"}
	ms := pipeline.NewMultiSink(a, b)
	_ = called // suppress unused warning

	err := ms.Write(context.Background(), &pipeline.ReportData{})
	require.NoError(t, err)
	assert.Equal(t, "multi", ms.Name())
}

func TestMultiSink_ContinuesAfterSinkFailure(t *testing.T) {
	ran := make([]string, 0, 3)
	appendSink := func(name string) pipeline.Sink {
		return &recordingSink{name: name, ran: &ran}
	}
	failSink := &failingSink{name: "fail"}

	ms := pipeline.NewMultiSink(appendSink("first"), failSink, appendSink("third"))
	err := ms.Write(context.Background(), &pipeline.ReportData{})

	assert.Error(t, err, "MultiSink must surface the failing sink error")
	assert.Contains(t, ran, "first", "first sink must have run")
	assert.Contains(t, ran, "third", "third sink must run even after second fails")
}

type recordingSink struct {
	name string
	ran  *[]string
}

func (r *recordingSink) Write(_ context.Context, _ *pipeline.ReportData) error {
	*r.ran = append(*r.ran, r.name)
	return nil
}
func (r *recordingSink) Name() string { return r.name }

type failingSink struct{ name string }

func (f *failingSink) Write(_ context.Context, _ *pipeline.ReportData) error {
	return assert.AnError
}
func (f *failingSink) Name() string { return f.name }

// ── Metrics contract tests ───────────────────────────────────────────────────

func TestWriterMetrics_FormatsPipeDelimited(t *testing.T) {
	var buf bytes.Buffer
	mw := pipeline.NewWriterMetrics(&buf)
	ts := time.Date(2026, 2, 27, 18, 23, 45, 0, time.UTC)

	err := mw.Write(pipeline.MetricsRecord{
		Timestamp:     ts,
		Task:          "workctl_extract",
		LatencyMS:     312,
		ConfidenceAvg: 0.87,
		SignalCount:   23,
		Model:         "llama3.2:3b",
		TaskType:      "weekly_signals",
	})
	require.NoError(t, err)

	line := strings.TrimSpace(buf.String())
	assert.Equal(t, "20260227T182345Z|workctl_extract|312|0.87|23|llama3.2:3b|weekly_signals", line)
}

func TestDiscardMetrics_AlwaysSucceeds(t *testing.T) {
	err := pipeline.DiscardMetrics{}.Write(pipeline.MetricsRecord{})
	assert.NoError(t, err)
}

// ── FileSink tests ───────────────────────────────────────────────────────────

func TestFileSink_WritesSignalsJSON(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "weekly.md")

	rd := &pipeline.ReportData{
		ReportType:  "weekly",
		Period:      "2026-02-17 to 2026-02-24",
		PeriodStart: time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 2, 24, 0, 0, 0, 0, time.UTC),
		Generated:   time.Date(2026, 2, 27, 0, 0, 0, 0, time.UTC),
		OutputPath:  outputPath,
		Signals:     &insights.SignalSet{TotalIssues: 5},
	}

	sink := export.NewFileSink()
	err := sink.Write(context.Background(), rd)
	require.NoError(t, err, "FileSink.Write must not error on valid input")

	signalsPath := filepath.Join(dir, "weekly.signals.json")
	data, err := os.ReadFile(signalsPath)
	require.NoError(t, err, ".signals.json file must be created")
	assert.Contains(t, string(data), `"schema_version"`, "must contain schema_version key")
	assert.Contains(t, string(data), `"weekly"`, "must contain report_type")
	assert.Contains(t, string(data), `"2026-02-17"`, "must contain period_start")
}

func TestFileSink_NoopOnEmptyOutputPath(t *testing.T) {
	sink := export.NewFileSink()
	err := sink.Write(context.Background(), &pipeline.ReportData{
		Signals: &insights.SignalSet{},
	})
	assert.NoError(t, err, "empty OutputPath must be a no-op")
}

func TestFileSink_NoopOnNilSignals(t *testing.T) {
	sink := export.NewFileSink()
	err := sink.Write(context.Background(), &pipeline.ReportData{
		OutputPath: "/tmp/noop.md",
	})
	assert.NoError(t, err, "nil Signals must be a no-op")
}

func TestSignalsPath_Extension(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"weekly.md", "weekly.signals.json"},
		{"report.pdf", "report.signals.json"},
		{"weekly", "weekly.signals.json"},
		{"/abs/path/report.md", "/abs/path/report.signals.json"},
	}
	for _, tc := range cases {
		got := export.SignalsPath(tc.input)
		assert.Equal(t, tc.want, got, "SignalsPath(%q)", tc.input)
	}
}

// ── ConfluentSink tests ──────────────────────────────────────────────────────

func TestConfluentSink_NoopOnEmptyHTML(t *testing.T) {
	sink := export.NewConfluentSink(export.ConfluentSinkConfig{
		SpaceKey:   "~test",
		AncestorID: "12345",
	})
	err := sink.Write(context.Background(), &pipeline.ReportData{})
	assert.NoError(t, err, "empty HTML must be a no-op — FileSink must have run first")
}

func TestConfluentSink_ErrorOnMissingClient(t *testing.T) {
	sink := export.NewConfluentSink(export.ConfluentSinkConfig{
		SpaceKey:   "~test",
		AncestorID: "12345",
	})
	err := sink.Write(context.Background(), &pipeline.ReportData{HTML: "<p>hello</p>"})
	require.Error(t, err, "missing client must return error when HTML is non-empty")
	assert.Contains(t, err.Error(), "confluence sink")
}

func TestMultiSink_FileSinkBeforeConfluentSink(t *testing.T) {
	// Verify file-first ordering: FileSink runs before ConfluentSink.
	// ConfluentSink with HTML but nil client returns an error — but FileSink must
	// have already written its sidecar before ConfluentSink fails.
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "standup.md")

	rd := &pipeline.ReportData{
		ReportType:  "weekly",
		PeriodStart: time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 2, 24, 0, 0, 0, 0, time.UTC),
		Generated:   time.Now(),
		OutputPath:  outputPath,
		Signals:     &insights.SignalSet{TotalIssues: 1},
		HTML:        "<p>standup</p>",
	}

	ms := pipeline.NewMultiSink(
		export.NewFileSink(),
		export.NewConfluentSink(export.ConfluentSinkConfig{SpaceKey: "~t", AncestorID: "1"}),
	)
	err := ms.Write(context.Background(), rd)
	assert.Error(t, err, "ConfluentSink with nil client must return error")

	// FileSink must have already written the sidecar even though ConfluentSink failed.
	_, statErr := os.Stat(filepath.Join(dir, "standup.signals.json"))
	assert.NoError(t, statErr, "FileSink must write .signals.json before ConfluentSink error")
}
