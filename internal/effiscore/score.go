package effiscore

import (
	"math"
	"time"
)

// Scoring weights (from epic specification).
const (
	weightCacheHit = 0.30
	weightReuse    = 0.25
	weightIO       = 0.20
	weightSavings  = 0.15
	weightModelMix = 0.10
)

// Normalization constants.
const (
	reuseFactorCap    = 5.0
	ioRatioMaxPenalty = 10.0      // ratios >= 10x score 0
	savingsBaseline   = 100_000.0 // "good day" = 100K tokens saved
)

// RawMetrics holds the six DD query scalars.
type RawMetrics struct {
	CacheReadTokens    float64
	InputTokens        float64
	OutputTokens       float64
	CacheWrite5mTokens float64
	CacheWrite1hTokens float64
	HaikuInputTokens   float64
}

// Signals holds the five computed efficiency dimensions.
type Signals struct {
	CacheHitRatePct      float64
	CacheReuseFactor     float64
	IORatio              float64
	TokenSavingsAbsolute float64
	HaikuSharePct        float64
}

// ScoreResult is the final composite result.
type ScoreResult struct {
	User        string
	WindowDays  int
	GeneratedAt string
	Signals     Signals
	Raw         RawMetrics
	Score       float64
	Tier        string
}

// ComputeSignals derives the five efficiency dimensions from raw metrics.
func ComputeSignals(r RawMetrics) Signals {
	var s Signals

	// Cache Hit Rate: cache_read / (cache_read + input + write_5m + write_1h) * 100
	denom := r.CacheReadTokens + r.InputTokens + r.CacheWrite5mTokens + r.CacheWrite1hTokens
	if denom > 0 {
		s.CacheHitRatePct = r.CacheReadTokens / denom * 100
	}

	// Cache Reuse Factor: cache_read / (write_5m + write_1h), capped at 5x
	writes := r.CacheWrite5mTokens + r.CacheWrite1hTokens
	if writes > 0 {
		s.CacheReuseFactor = math.Min(r.CacheReadTokens/writes, reuseFactorCap)
	}

	// I/O Ratio: input / output (lower = better)
	if r.OutputTokens > 0 {
		s.IORatio = r.InputTokens / r.OutputTokens
	}

	// Token Savings: net tokens saved by caching vs. sending as raw input.
	// Reads save 90% each; writes cost a premium above base input price.
	// savings = cache_read * 0.9 - (0.25 * write_5m + 1.0 * write_1h)
	savings := r.CacheReadTokens*0.9 - (0.25*r.CacheWrite5mTokens + 1.0*r.CacheWrite1hTokens)
	s.TokenSavingsAbsolute = math.Max(savings, 0)

	// Model Mix: Haiku share of total input volume
	if r.InputTokens > 0 {
		s.HaikuSharePct = r.HaikuInputTokens / r.InputTokens * 100
	}

	return s
}

// ComputeScore returns a weighted composite score (0-100) from normalized dimensions.
func ComputeScore(s Signals) float64 {
	// Normalize each dimension to 0-100
	cacheHitComponent := clamp(s.CacheHitRatePct, 0, 100)
	reuseComponent := clamp(s.CacheReuseFactor/reuseFactorCap*100, 0, 100)
	ioComponent := clamp((ioRatioMaxPenalty-s.IORatio)/(ioRatioMaxPenalty-1)*100, 0, 100)
	savingsComponent := clamp(s.TokenSavingsAbsolute/savingsBaseline*100, 0, 100)
	modelMixComponent := clamp(s.HaikuSharePct, 0, 100)

	return weightCacheHit*cacheHitComponent +
		weightReuse*reuseComponent +
		weightIO*ioComponent +
		weightSavings*savingsComponent +
		weightModelMix*modelMixComponent
}

// Tier returns a qualitative label for the composite score.
func Tier(score float64) string {
	switch {
	case score >= 80:
		return "excellent"
	case score >= 60:
		return "good"
	case score >= 40:
		return "fair"
	default:
		return "poor"
	}
}

// Compute orchestrates signal computation, scoring, and tier assignment.
func Compute(user string, windowDays int, r RawMetrics) ScoreResult {
	signals := ComputeSignals(r)
	score := ComputeScore(signals)
	return ScoreResult{
		User:        user,
		WindowDays:  windowDays,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Signals:     signals,
		Raw:         r,
		Score:       math.Round(score*10) / 10, // one decimal place
		Tier:        Tier(score),
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
