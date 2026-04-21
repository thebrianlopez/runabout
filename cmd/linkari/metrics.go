package main

// MetricsCollector is the central sink for server-side operational metrics.
// It aggregates data from the scoring pipeline for future emission to an
// external metrics backend (e.g. Datadog via DogStatsD or OTEL).
//
// Current state (GAP-07/GAP-08): scaffolding only. The collector is wired
// into the serve path and gated on `metrics.enabled` in server.yaml, but
// metric emission stubs are no-ops until a backend is configured.
//
// Planned metric streams:
//   - linkari.llm.cost_usd   (sourced from UpdateScoringCost / scoring_cost_usd col)
//   - scoring.success_rate   (baseline: 71.1% scored vs timeout)
//   - timeout_rate           (baseline: 28.9%)
//   - digest.delivery_rate   (baseline: 43.8%)
type MetricsCollector struct {
	enabled bool
}

// NewMetricsCollector returns an initialized MetricsCollector. When
// cfg.MetricsEnabled() is false (explicit `metrics: {enabled: false}` in
// server.yaml), returns nil — callers must nil-check before use.
func NewMetricsCollector(cfg *ServerConfig) *MetricsCollector {
	if cfg == nil || !cfg.MetricsEnabled() {
		return nil
	}
	return &MetricsCollector{enabled: true}
}

// RecordPrefilterDrop records a share that was pre-filtered before scoring.
// Reason is the machine-readable skip tag (e.g. "login_wall_domain",
// "unsupported_pipeline"). EPIC-001 M6: stub — no-op until metrics backend
// is configured.
func (m *MetricsCollector) RecordPrefilterDrop(reason string) {
	if m == nil || !m.enabled {
		return
	}
	// TODO(GAP-08): emit linkari.prefilter.drop counter with tag reason=reason
	// Example: statsd.Incr("linkari.prefilter.drop", tags{reason}, 1)
}

// RecordScoringCost captures per-call LLM cost and image token data after
// UpdateScoringCost persists the row. This is the intended source for the
// future linkari.llm.cost_usd metric stream.
//
// Current implementation: no-op stub. Replace with DogStatsD/OTEL emission
// once a metrics backend is configured.
func (m *MetricsCollector) RecordScoringCost(rowID int64, costUSD float64, imageTokensEstimated int) {
	if m == nil || !m.enabled {
		return
	}
	// TODO(GAP-07): emit linkari.llm.cost_usd gauge with tags:
	//   row_id, image_tokens_estimated
	// Example: statsd.Gauge("linkari.llm.cost_usd", costUSD, tags, 1)
}
