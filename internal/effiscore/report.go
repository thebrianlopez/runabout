package effiscore

import (
	"encoding/json"
	"fmt"
	"io"
)

// JSONOutput matches the interface contract with ClaudeConfig EPIC-001 M2.
type JSONOutput struct {
	User        string      `json:"user"`
	WindowDays  int         `json:"window_days"`
	GeneratedAt string      `json:"generated_at"`
	Signals     JSONSignals `json:"signals"`
	Raw         JSONRaw     `json:"raw"`
}

// JSONSignals holds the five computed dimensions.
type JSONSignals struct {
	CacheHitRatePct      float64 `json:"cache_hit_rate_pct"`
	CacheReuseFactor     float64 `json:"cache_reuse_factor"`
	IORatio              float64 `json:"io_ratio"`
	TokenSavingsAbsolute int64   `json:"token_savings_absolute"`
	HaikuSharePct        float64 `json:"haiku_share_pct"`
}

// JSONRaw holds the raw metric values.
type JSONRaw struct {
	CacheReadTokens    int64 `json:"cache_read_tokens"`
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	CacheWrite5mTokens int64 `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int64 `json:"cache_write_1h_tokens"`
}

// RenderJSON returns JSON conforming to the ClaudeConfig M2 contract.
func RenderJSON(result ScoreResult) ([]byte, error) {
	out := JSONOutput{
		User:        result.User,
		WindowDays:  result.WindowDays,
		GeneratedAt: result.GeneratedAt,
		Signals: JSONSignals{
			CacheHitRatePct:      round2(result.Signals.CacheHitRatePct),
			CacheReuseFactor:     round2(result.Signals.CacheReuseFactor),
			IORatio:              round2(result.Signals.IORatio),
			TokenSavingsAbsolute: int64(result.Signals.TokenSavingsAbsolute),
			HaikuSharePct:        round2(result.Signals.HaikuSharePct),
		},
		Raw: JSONRaw{
			CacheReadTokens:    int64(result.Raw.CacheReadTokens),
			InputTokens:        int64(result.Raw.InputTokens),
			OutputTokens:       int64(result.Raw.OutputTokens),
			CacheWrite5mTokens: int64(result.Raw.CacheWrite5mTokens),
			CacheWrite1hTokens: int64(result.Raw.CacheWrite1hTokens),
		},
	}
	return json.MarshalIndent(out, "", "  ")
}

// RenderText writes a human-readable report.
func RenderText(w io.Writer, result ScoreResult) {
	fmt.Fprintf(w, "effiscore report — %s (%dd)\n", result.User, result.WindowDays)
	fmt.Fprintf(w, "Generated: %s\n\n", result.GeneratedAt)

	fmt.Fprintln(w, "Signals")
	fmt.Fprintf(w, "  Cache Hit Rate:     %.1f%%\n", result.Signals.CacheHitRatePct)
	fmt.Fprintf(w, "  Cache Reuse Factor: %.1fx\n", result.Signals.CacheReuseFactor)
	fmt.Fprintf(w, "  I/O Ratio:          %.1f\n", result.Signals.IORatio)
	fmt.Fprintf(w, "  Token Savings:      %d\n", int64(result.Signals.TokenSavingsAbsolute))
	fmt.Fprintf(w, "  Haiku Share:        %.1f%%\n\n", result.Signals.HaikuSharePct)

	fmt.Fprintf(w, "Composite Score: %.1f  [%s]\n", result.Score, result.Tier)
}

func round2(v float64) float64 {
	return float64(int64(v*100)) / 100
}
