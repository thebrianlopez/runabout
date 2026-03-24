package effiscore

import (
	"math"
	"testing"
)

func approxEqual(a, b, tol float64) bool {
	return math.Abs(a-b) < tol
}

func TestComputeSignals_AllZero(t *testing.T) {
	s := ComputeSignals(RawMetrics{})
	if s.CacheHitRatePct != 0 {
		t.Errorf("CacheHitRatePct = %f, want 0", s.CacheHitRatePct)
	}
	if s.CacheReuseFactor != 0 {
		t.Errorf("CacheReuseFactor = %f, want 0", s.CacheReuseFactor)
	}
	if s.IORatio != 0 {
		t.Errorf("IORatio = %f, want 0", s.IORatio)
	}
	if s.TokenSavingsAbsolute != 0 {
		t.Errorf("TokenSavingsAbsolute = %f, want 0", s.TokenSavingsAbsolute)
	}
	if s.HaikuSharePct != 0 {
		t.Errorf("HaikuSharePct = %f, want 0", s.HaikuSharePct)
	}
}

func TestComputeSignals_CacheHitRate(t *testing.T) {
	r := RawMetrics{
		CacheReadTokens:    500,
		InputTokens:        300,
		CacheWrite5mTokens: 100,
		CacheWrite1hTokens: 100,
	}
	s := ComputeSignals(r)
	// 500 / (500 + 300 + 100 + 100) = 500/1000 = 50%
	if !approxEqual(s.CacheHitRatePct, 50.0, 0.01) {
		t.Errorf("CacheHitRatePct = %f, want 50.0", s.CacheHitRatePct)
	}
}

func TestComputeSignals_CacheReuseFactor(t *testing.T) {
	tests := []struct {
		name     string
		raw      RawMetrics
		wantFactor float64
	}{
		{
			name: "normal reuse",
			raw: RawMetrics{
				CacheReadTokens:    300,
				CacheWrite5mTokens: 100,
				CacheWrite1hTokens: 50,
			},
			wantFactor: 2.0, // 300 / 150
		},
		{
			name: "capped at 5x",
			raw: RawMetrics{
				CacheReadTokens:    10000,
				CacheWrite5mTokens: 100,
				CacheWrite1hTokens: 0,
			},
			wantFactor: 5.0, // 10000/100 = 100, capped at 5
		},
		{
			name: "zero writes",
			raw: RawMetrics{
				CacheReadTokens:    1000,
				CacheWrite5mTokens: 0,
				CacheWrite1hTokens: 0,
			},
			wantFactor: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := ComputeSignals(tt.raw)
			if !approxEqual(s.CacheReuseFactor, tt.wantFactor, 0.01) {
				t.Errorf("CacheReuseFactor = %f, want %f", s.CacheReuseFactor, tt.wantFactor)
			}
		})
	}
}

func TestComputeSignals_IORatio(t *testing.T) {
	tests := []struct {
		name      string
		raw       RawMetrics
		wantRatio float64
	}{
		{
			name:      "normal ratio",
			raw:       RawMetrics{InputTokens: 1000, OutputTokens: 500},
			wantRatio: 2.0,
		},
		{
			name:      "zero output",
			raw:       RawMetrics{InputTokens: 1000, OutputTokens: 0},
			wantRatio: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := ComputeSignals(tt.raw)
			if !approxEqual(s.IORatio, tt.wantRatio, 0.01) {
				t.Errorf("IORatio = %f, want %f", s.IORatio, tt.wantRatio)
			}
		})
	}
}

func TestComputeSignals_TokenSavings(t *testing.T) {
	// savings = 1000*0.9 - (0.25*5000 + 1.0*500) = 900 - 1750 = -850 → clamped to 0
	r := RawMetrics{
		CacheReadTokens:    1000,
		CacheWrite5mTokens: 5000,
		CacheWrite1hTokens: 500,
	}
	s := ComputeSignals(r)
	if s.TokenSavingsAbsolute != 0 {
		t.Errorf("TokenSavingsAbsolute = %f, want 0 (negative clamped)", s.TokenSavingsAbsolute)
	}

	// savings = 100000*0.9 - (0.25*1000 + 1.0*500) = 90000 - 750 = 89250
	r2 := RawMetrics{
		CacheReadTokens:    100000,
		CacheWrite5mTokens: 1000,
		CacheWrite1hTokens: 500,
	}
	s2 := ComputeSignals(r2)
	if !approxEqual(s2.TokenSavingsAbsolute, 89250, 0.01) {
		t.Errorf("TokenSavingsAbsolute = %f, want 89250", s2.TokenSavingsAbsolute)
	}
}

func TestComputeSignals_HaikuShare(t *testing.T) {
	r := RawMetrics{InputTokens: 1000, HaikuInputTokens: 600}
	s := ComputeSignals(r)
	if !approxEqual(s.HaikuSharePct, 60.0, 0.01) {
		t.Errorf("HaikuSharePct = %f, want 60.0", s.HaikuSharePct)
	}

	r2 := RawMetrics{InputTokens: 0, HaikuInputTokens: 0}
	s2 := ComputeSignals(r2)
	if s2.HaikuSharePct != 0 {
		t.Errorf("HaikuSharePct = %f, want 0 (zero input)", s2.HaikuSharePct)
	}
}

func TestComputeScore_Weights(t *testing.T) {
	// All dimensions at max → score should be 100
	s := Signals{
		CacheHitRatePct:      100,
		CacheReuseFactor:     5.0,
		IORatio:              1.0,
		TokenSavingsAbsolute: 100_000,
		HaikuSharePct:        100,
	}
	score := ComputeScore(s)
	if !approxEqual(score, 100, 0.01) {
		t.Errorf("max signals score = %f, want 100", score)
	}

	// All dimensions at zero → score should be close to 0
	// I/O ratio 0 normalizes to: (10-0)/9*100 = 111 → clamped to 100
	// So zero I/O still gives 20 points from the IO component
	s2 := Signals{}
	score2 := ComputeScore(s2)
	// Only IO contributes: 0.20 * 100 = 20 (since ioComponent = (10-0)/9*100 = 111 → 100)
	if !approxEqual(score2, 20, 0.5) {
		t.Errorf("zero signals score = %f, want ~20 (IO component)", score2)
	}
}

func TestTier(t *testing.T) {
	tests := []struct {
		score float64
		want  string
	}{
		{0, "poor"},
		{39.9, "poor"},
		{40, "fair"},
		{59.9, "fair"},
		{60, "good"},
		{79.9, "good"},
		{80, "excellent"},
		{100, "excellent"},
	}

	for _, tt := range tests {
		got := Tier(tt.score)
		if got != tt.want {
			t.Errorf("Tier(%f) = %q, want %q", tt.score, got, tt.want)
		}
	}
}

func TestCompute_Integration(t *testing.T) {
	r := RawMetrics{
		CacheReadTokens:    5000,
		InputTokens:        10000,
		OutputTokens:       3000,
		CacheWrite5mTokens: 2000,
		CacheWrite1hTokens: 1000,
		HaikuInputTokens:   4000,
	}
	result := Compute("test_user", 7, r)

	if result.User != "test_user" {
		t.Errorf("User = %q, want test_user", result.User)
	}
	if result.WindowDays != 7 {
		t.Errorf("WindowDays = %d, want 7", result.WindowDays)
	}
	if result.GeneratedAt == "" {
		t.Error("GeneratedAt should not be empty")
	}
	if result.Tier == "" {
		t.Error("Tier should not be empty")
	}
	if result.Score < 0 || result.Score > 100 {
		t.Errorf("Score = %f, want 0-100", result.Score)
	}
}
