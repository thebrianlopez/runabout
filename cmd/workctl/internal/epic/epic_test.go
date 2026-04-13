// Package epic contains one smoke test per shipped EPIC.
// Each test is named TestEPIC_NNN_* so contributors can quickly verify a
// specific EPIC's core contract is still satisfied:
//
//	go test ./internal/epic/... -run TestEPIC_001    # single EPIC
//	go test ./internal/epic/... -run TestEPIC         # all EPICs
//	go test ./internal/epic/...                       # full sweep
//
// Tests here are intentionally lightweight: they do not replace the
// package-level unit tests but serve as named regression guards that make
// it obvious which EPIC broke when a refactor goes wrong.
package epic_test

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

	"github.com/blo-grindr/runabout/cmd/workctl/internal/api"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/cache"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/config"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/export"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/insights"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/pipeline"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/templates"
)

// ── EPIC-001: GitHub Activity Integration ───────────────────────────────────
// Shipped AC: GitHubActivity model, GetUserActivity, Events API, JSONL export.

func TestEPIC_001_GitHubActivityTypeExists(t *testing.T) {
	// GitHubActivity is the canonical model for all GitHub events.
	// If this struct changes shape or disappears the EPIC-001 contract is broken.
	var a models.GitHubActivity
	a.EventType = "PushEvent"
	a.Repository = "org/repo"
	assert.Equal(t, "PushEvent", a.EventType)
	assert.Equal(t, "org/repo", a.Repository)
}

func TestEPIC_001_GitHubClientConstructor(t *testing.T) {
	// NewGitHubClient accepts a token string and returns a non-nil client.
	// A fake token is fine — construction must not require a live network.
	c, err := api.NewGitHubClient("ghp_fake_token_for_test")
	require.NoError(t, err)
	assert.NotNil(t, c)
}

// ── EPIC-002: EPIC-Driven Task Synchronization ───────────────────────────────
// Shipped AC: version metadata embedded in binary; beads issue tracking.

func TestEPIC_002_FormatPeriodIsStable(t *testing.T) {
	// EPIC-002 standardised the project versioning + task-tracking workflow.
	// FormatPeriod is a stable formatting utility that downstream consumers rely on.
	period := insights.FormatPeriod("2026-01-01", "2026-01-31")
	assert.Equal(t, "2026-01-01 to 2026-01-31", period)
}

// ── EPIC-003: Hybrid GitHub API Strategy ────────────────────────────────────
// Shipped AC: SelectStrategy switches Events/Search/GraphQL by date range.

func TestEPIC_003_SelectStrategyRecent(t *testing.T) {
	start := time.Now().Add(-7 * 24 * time.Hour) // 7 days ago → Events API
	strategy, err := api.SelectStrategy(start, api.StrategyAuto)
	require.NoError(t, err)
	assert.Equal(t, api.StrategyEvents, strategy)
}

func TestEPIC_003_SelectStrategyOld(t *testing.T) {
	start := time.Now().Add(-400 * 24 * time.Hour) // >1 year → GraphQL
	strategy, err := api.SelectStrategy(start, api.StrategyAuto)
	require.NoError(t, err)
	assert.Equal(t, api.StrategyGraphQL, strategy)
}

func TestEPIC_003_SelectStrategyOverride(t *testing.T) {
	start := time.Now().Add(-7 * 24 * time.Hour)
	strategy, err := api.SelectStrategy(start, api.StrategySearch)
	require.NoError(t, err)
	assert.Equal(t, api.StrategySearch, strategy, "explicit override must be honoured")
}

// ── EPIC-004: GitHub Activity Streaming CLI ──────────────────────────────────
// Shipped AC: ghwatch package with Poll, PR, push and workflow event handling.

func TestEPIC_004_StrategyConstantsAreDefined(t *testing.T) {
	// EPIC-004 relies on all three strategy constants being present for the
	// --github-strategy flag help text.
	strategies := []api.APIStrategy{api.StrategyEvents, api.StrategySearch, api.StrategyGraphQL, api.StrategyAuto}
	for _, s := range strategies {
		assert.NotEmpty(t, string(s), "strategy constant must not be empty")
	}
}

// ── EPIC-005: Config Profiles and Init ──────────────────────────────────────
// Shipped AC: ParseConfig supports profile-scoped YAML config files.

func TestEPIC_005_DiscoverConfigFileReturnsEmptyWhenAbsent(t *testing.T) {
	// EPIC-005 added profile-scoped config discovery with graceful degradation.
	// DiscoverConfigFile must return ("", nil) when no config file is found —
	// callers then use environment variables and flag defaults.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // point away from real config
	found, err := config.DiscoverConfigFile("")
	require.NoError(t, err, "absent config must not error")
	assert.Empty(t, found, "no config file in clean dir must return empty path")
}

// ── EPIC-006: Local Result Cache ─────────────────────────────────────────────
// Shipped AC: SQLite-backed cache with TTL, gzip, GetOrFetch.

func TestEPIC_006_CacheOpenInMemory(t *testing.T) {
	store := cache.Open(":memory:")
	require.NotNil(t, store, "in-memory cache must open successfully")
}

func TestEPIC_006_GetOrFetchCachesResult(t *testing.T) {
	store := cache.Open(":memory:")
	require.NotNil(t, store)

	calls := 0
	fetch := func() (string, error) {
		calls++
		return "hello", nil
	}

	v1, err := cache.GetOrFetch[string](store, "k", cache.SourceGitHubEvents, time.Hour, false, fetch)
	require.NoError(t, err)
	assert.Equal(t, "hello", v1)

	v2, err := cache.GetOrFetch[string](store, "k", cache.SourceGitHubEvents, time.Hour, false, fetch)
	require.NoError(t, err)
	assert.Equal(t, "hello", v2)
	assert.Equal(t, 1, calls, "second call must hit cache — fetch must not be called again")
}

// ── EPIC-007: XDG Basedir Install ───────────────────────────────────────────
// Shipped AC: WorkctlConfigDir/StateDir/CacheDir honour XDG env vars.

func TestEPIC_007_WorkctlConfigDirHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg_test_config")
	dir := config.WorkctlConfigDir()
	assert.Equal(t, "/tmp/xdg_test_config/workctl", dir)
}

func TestEPIC_007_WorkctlStateDirHonoursXDG(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg_test_state")
	dir := config.WorkctlStateDir()
	assert.Equal(t, "/tmp/xdg_test_state/workctl", dir)
}

func TestEPIC_007_WorkctlCacheDirHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg_test_cache")
	dir := config.WorkctlCacheDir()
	assert.Equal(t, "/tmp/xdg_test_cache/workctl", dir)
}

// ── EPIC-008: Career Growth Insights ────────────────────────────────────────
// Shipped AC: ExtractSignals produces a non-nil SignalSet; theme classification.

func TestEPIC_008_ExtractSignalsNonNil(t *testing.T) {
	signals := insights.ExtractSignals(nil, nil, nil)
	require.NotNil(t, signals, "ExtractSignals must never return nil")
}

func TestEPIC_008_ClassifyThemeFeature(t *testing.T) {
	theme := insights.ClassifyTheme("Story", "add user authentication")
	assert.Equal(t, insights.ThemeFeature, theme)
}

func TestEPIC_008_ClassifyThemeBug(t *testing.T) {
	theme := insights.ClassifyTheme("Bug", "login fails on iOS")
	assert.Equal(t, insights.ThemeBug, theme)
}

// ── EPIC-009: Comprehensive Test Coverage Expansion ─────────────────────────
// Shipped AC: package-level coverage ≥90%; dead csv.go deleted.

func TestEPIC_009_SignalsPathDerived(t *testing.T) {
	// SignalsPath is the key utility added in EPIC-009 clean-up / EPIC-016 M2.
	// Placed here as a regression check tied to the coverage push.
	assert.Equal(t, "weekly.signals.json", export.SignalsPath("weekly.md"))
	assert.Equal(t, "report.signals.json", export.SignalsPath("report.pdf"))
	assert.Equal(t, "out.signals.json", export.SignalsPath("out"))
}

// ── EPIC-010: Personal Report Generation ────────────────────────────────────
// Shipped AC: RenderWeekly, RenderQuarterly, RenderReview emit valid markdown.

func TestEPIC_010_RenderWeeklyProducesOutput(t *testing.T) {
	var buf bytes.Buffer
	signals := insights.ExtractSignals(nil, nil, nil)
	err := templates.RenderWeekly(&buf, signals, "2026-02-17 to 2026-02-24")
	require.NoError(t, err)
	assert.NotEmpty(t, buf.String(), "RenderWeekly must produce output")
}

func TestEPIC_010_RenderQuarterlyProducesOutput(t *testing.T) {
	var buf bytes.Buffer
	delta := insights.ComputeDelta(
		insights.ExtractSignals(nil, nil, nil),
		insights.ExtractSignals(nil, nil, nil),
		"Q3 2025", "Q4 2025",
	)
	err := templates.RenderQuarterly(&buf, delta)
	require.NoError(t, err)
	assert.NotEmpty(t, buf.String(), "RenderQuarterly must produce output")
}

// ── EPIC-011: Trend Analysis N-Period Comparison ─────────────────────────────
// Shipped AC: RenderTrends accepts N periods; TrendPeriod view model.

func TestEPIC_011_RenderTrendsEmptyPeriods(t *testing.T) {
	var buf bytes.Buffer
	err := templates.RenderTrends(&buf, nil, "1m")
	require.NoError(t, err)
	assert.NotEmpty(t, buf.String(), "RenderTrends must produce output even with no periods")
}

func TestEPIC_011_FormatPeriod(t *testing.T) {
	period := insights.FormatPeriod("2026-02-17", "2026-02-24")
	assert.Equal(t, "2026-02-17 to 2026-02-24", period)
}

// ── EPIC-012: Workflow DX — Refine My Practice ───────────────────────────────
// Shipped AC: shell activity toggle; fish history + audit log + Claude stats.

func TestEPIC_012_FishHistorySourceName(t *testing.T) {
	src := api.NewFishHistorySource()
	assert.Equal(t, "fish_history", src.Name())
}

func TestEPIC_012_AuditLogSourceName(t *testing.T) {
	src := api.NewAuditLogSource()
	assert.Equal(t, "audit_log", src.Name())
}

func TestEPIC_012_FishHistoryFetchAbsentFileReturnsEmpty(t *testing.T) {
	src := api.NewFishHistorySource()
	now := time.Now()
	events, err := src.Fetch(context.Background(), now.Add(-24*time.Hour), now)
	require.NoError(t, err)
	assert.NotNil(t, events)
}

// ── EPIC-013: Technical Foundation / Data Architecture ──────────────────────
// Shipped AC: models.Issue and models.GitHubActivity are the canonical types;
// QueryMode constants (UserMode, ProjectMode, MixedMode, SpaceMode, GitHubMode).

func TestEPIC_013_QueryModeConstants(t *testing.T) {
	modes := []models.QueryMode{
		models.UserMode,
		models.ProjectMode,
		models.SpaceMode,
		models.MixedMode,
		models.GitHubMode,
	}
	seen := map[models.QueryMode]bool{}
	for _, m := range modes {
		assert.False(t, seen[m], "QueryMode constant %d must be unique", m)
		seen[m] = true
	}
}

func TestEPIC_013_IssueKeyField(t *testing.T) {
	issue := models.Issue{Key: "WC-42", ProjectKey: "WC"}
	assert.Equal(t, "WC-42", issue.Key)
}

// ── EPIC-014: Confluence Standup Publisher ───────────────────────────────────
// Shipped AC: RenderStandupHTML + FormatStandupTitle; file-first publish.

func TestEPIC_014_FormatStandupTitleSameMonth(t *testing.T) {
	start := time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 24, 0, 0, 0, 0, time.UTC)
	title := insights.FormatStandupTitle(start, end)
	assert.Contains(t, title, "February", "title must include month name")
	assert.Contains(t, title, "2026", "title must include year")
}

func TestEPIC_014_RenderStandupHTMLProducesOutput(t *testing.T) {
	var buf bytes.Buffer
	opts := insights.StandupOpts{
		AuthorName: "Test Author",
		Period:     "2026-02-17 to 2026-02-24",
		Signals:    insights.ExtractSignals(nil, nil, nil),
		Generated:  time.Now(),
		Version:    "test",
	}
	err := insights.RenderStandupHTML(&buf, opts)
	require.NoError(t, err)
	assert.NotEmpty(t, buf.String(), "RenderStandupHTML must produce non-empty HTML")
	assert.Contains(t, buf.String(), "Test Author", "HTML must include author name")
}

// ── EPIC-015: Local Shell and AI Activity Integration ───────────────────────
// Shipped AC: FishHistorySource, AuditLogSource, ClaudeStatsSource pipeline
// sources; ExtractShellSignals, ExtractAISignals signal extractors.

func TestEPIC_015_AllLocalSourcesHaveStableNames(t *testing.T) {
	sources := []pipeline.Source{
		api.NewFishHistorySource(),
		api.NewAuditLogSource(),
		api.NewClaudeStatsSource(),
	}
	for _, src := range sources {
		name := src.Name()
		assert.NotEmpty(t, name)
		assert.Equal(t, strings.ToLower(name), name, "source name must be lowercase")
		assert.Equal(t, name, src.Name(), "source name must be stable across calls")
	}
}

func TestEPIC_015_ExtractShellSignalsNilInput(t *testing.T) {
	signals := insights.ExtractShellSignals(nil)
	require.NotNil(t, signals, "ExtractShellSignals(nil) must return non-nil")
}

func TestEPIC_015_ExtractAISignalsNilInput(t *testing.T) {
	signals := insights.ExtractAISignals(nil, nil, nil)
	require.NotNil(t, signals, "ExtractAISignals(nil,nil,nil) must return non-nil")
}

// ── EPIC-016: Pluggable Pipeline Modularity ──────────────────────────────────
// Shipped AC: Source/Extractor/Sink interfaces; FileSink dual-output contract;
// file-first MultiSink ordering; RulesExtractor tier.

func TestEPIC_016_FileSinkWritesSignalsSidecar(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "weekly.md")

	rd := &pipeline.ReportData{
		ReportType:  "weekly",
		PeriodStart: time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 2, 24, 0, 0, 0, 0, time.UTC),
		Generated:   time.Now(),
		OutputPath:  out,
		Signals:     insights.ExtractSignals(nil, nil, nil),
	}

	err := export.NewFileSink().Write(context.Background(), rd)
	require.NoError(t, err)

	sidecar := filepath.Join(dir, "weekly.signals.json")
	data, err := os.ReadFile(sidecar)
	require.NoError(t, err, "FileSink must create .signals.json sidecar")
	assert.Contains(t, string(data), `"schema_version": "1"`)
}

func TestEPIC_016_ConfluentSinkNoopOnEmptyHTML(t *testing.T) {
	sink := export.NewConfluentSink(export.ConfluentSinkConfig{
		SpaceKey: "~test", AncestorID: "1",
	})
	err := sink.Write(context.Background(), &pipeline.ReportData{})
	assert.NoError(t, err, "ConfluentSink with empty HTML must be a no-op")
}

func TestEPIC_016_MultiSinkFileFirstOrdering(t *testing.T) {
	// FileSink writes before ConfluentSink fails.
	// After the MultiSink returns an error, the .signals.json must already exist.
	dir := t.TempDir()
	out := filepath.Join(dir, "standup.md")

	rd := &pipeline.ReportData{
		ReportType:  "weekly",
		PeriodStart: time.Date(2026, 2, 17, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 2, 24, 0, 0, 0, 0, time.UTC),
		Generated:   time.Now(),
		OutputPath:  out,
		Signals:     insights.ExtractSignals(nil, nil, nil),
		HTML:        "<p>standup content</p>",
	}

	ms := pipeline.NewMultiSink(
		export.NewFileSink(),
		// ConfluentSink with nil client and non-empty HTML → returns error
		export.NewConfluentSink(export.ConfluentSinkConfig{SpaceKey: "~t", AncestorID: "1"}),
	)
	err := ms.Write(context.Background(), rd)
	assert.Error(t, err, "ConfluentSink must surface error for nil client")

	_, statErr := os.Stat(filepath.Join(dir, "standup.signals.json"))
	assert.NoError(t, statErr, "FileSink must have written sidecar before ConfluentSink ran")
}

func TestEPIC_016_PipelineInterfaceSatisfied(t *testing.T) {
	// Compiler-level assertion: types must satisfy interfaces at compile time.
	// This test is intentionally a no-op at runtime.
	var (
		_ pipeline.Sink = export.NewFileSink()
		_ pipeline.Sink = export.NewConfluentSink(export.ConfluentSinkConfig{})
		_ pipeline.Sink = pipeline.NewMultiSink()
	)
}

// ── EPIC-018: Ollama Deprecation ─────────────────────────────────────────────
// Shipped AC: OllamaClient, reflection logic, prompts deleted; OllamaExtractor
// renamed RulesExtractor at TierRules; reflect command and all Ollama CLI flags
// removed; OllamaConfig and narrative fields removed from config structs.

func TestEPIC_018_TierHierarchy(t *testing.T) {
	// TierRules (0) < TierLocalAI (1) < TierCloud (2).
	// RulesExtractor is at TierRules — the deterministic, Ollama-free tier.
	assert.Equal(t, pipeline.Tier(0), pipeline.TierRules,
		"TierRules must be tier 0 (lowest, deterministic)")
	assert.Less(t, int(pipeline.TierRules), int(pipeline.TierLocalAI),
		"TierRules must be lower than TierLocalAI")
	assert.Less(t, int(pipeline.TierLocalAI), int(pipeline.TierCloud),
		"TierLocalAI must be lower than TierCloud")
}

func TestEPIC_018_ErrNotImplementedExists(t *testing.T) {
	// ErrNotImplemented is used by CloudExtractor stub — verify it's still exported.
	assert.Error(t, pipeline.ErrNotImplemented)
	assert.Contains(t, pipeline.ErrNotImplemented.Error(), "not implemented")
}
