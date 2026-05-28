package telemetry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// NewReadyzHandler returns an http.Handler for GET /readyz.
//
// Returns 200 {"status":"ok","last_poll_ago_seconds":N} when the last
// successful poll completed within 3×interval.
// Returns 503 {"status":"not_ready","reason":"..."} otherwise.
//
// lastSuccess() returning zero time.Time indicates no successful poll has
// occurred yet (returns 503 with reason "no successful poll yet").
// nowFn is injected for deterministic testing.
func NewReadyzHandler(
	lastSuccess func() time.Time,
	interval time.Duration,
	nowFn func() time.Time,
) http.Handler {
	threshold := 3 * interval
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last := lastSuccess()
		now := nowFn()

		if last.IsZero() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "not_ready",
				"reason": "no successful poll yet",
			})
			return
		}

		age := now.Sub(last)
		if age < threshold {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"status":                "ok",
				"last_poll_ago_seconds": int(age.Seconds()),
			})
			return
		}

		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "not_ready",
			"reason": fmt.Sprintf("last poll %ds ago; threshold %ds", int(age.Seconds()), int(threshold.Seconds())),
		})
	})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
