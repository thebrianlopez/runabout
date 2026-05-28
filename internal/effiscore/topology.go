package effiscore

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// EmitEfficiency emits a cloud_llm_efficiency event to the topology bus.
func EmitEfficiency(result ScoreResult) {
	meta := map[string]interface{}{
		"user":            result.User,
		"window_days":     result.WindowDays,
		"cache_hit_rate":  result.Signals.CacheHitRatePct,
		"cache_reuse":     result.Signals.CacheReuseFactor,
		"io_ratio":        result.Signals.IORatio,
		"token_savings":   int64(result.Signals.TokenSavingsAbsolute),
		"haiku_share":     result.Signals.HaikuSharePct,
		"composite_score": result.Score,
		"tier":            result.Tier,
		"rel_type":        "observes:cloud_llm",
	}
	emitEvent("cloud_llm_efficiency", fmt.Sprintf("effiscore score --user %s", result.User), meta)
}

// HealthStats tracks DD API response quality.
type HealthStats struct {
	MetricsReturned int
	Nulls           int
}

// EmitHealth emits a dd_api_health event to the topology bus.
func EmitHealth(user string, h HealthStats) {
	status := "ok"
	if h.Nulls > 0 {
		status = "degraded"
	}
	if h.MetricsReturned == 0 {
		status = "down"
	}
	meta := map[string]interface{}{
		"user":             user,
		"status":           status,
		"metrics_returned": h.MetricsReturned,
		"nulls":            h.Nulls,
		"rel_type":         "observes:cloud_llm",
	}
	emitEvent("dd_api_health", fmt.Sprintf("effiscore score --user %s", user), meta)
}

func emitEvent(eventType, command string, metadata map[string]interface{}) {
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		fmt.Fprintf(os.Stderr, "effiscore: topology emit: %v\n", err)
		return
	}

	escapedMeta := strings.ReplaceAll(string(metaJSON), "'", "'\\''")

	fishCmd := fmt.Sprintf(
		"emit_jsonl --layer go_cli --event-type %s --command '%s' --metadata-json '%s'",
		eventType, command, escapedMeta,
	)

	if err := exec.Command("fish", "-c", fishCmd).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "effiscore: topology emit: %v\n", err)
	}
}
