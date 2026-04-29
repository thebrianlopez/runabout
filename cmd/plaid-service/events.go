package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// emitTransactionBatch emits a plaid_transaction_batch event (omitted if all counts are 0).
func emitTransactionBatch(syncRunID, itemID string, res *SyncResult) {
	if res.Added == 0 && res.Modified == 0 && res.Removed == 0 {
		return
	}
	writeEvent(buildPlaidEvent("plaid_transaction_batch", syncRunID, map[string]any{
		"item_id":          itemID,
		"tx_added":         res.Added,
		"tx_modified":      res.Modified,
		"tx_removed":       res.Removed,
		"cursor_committed": res.Cursor != "",
		"sync_run_id":      res.RunID,
	}))
}

// emitServiceHealth emits a plaid_service_health heartbeat (always emitted, even on 0-item ticks).
func emitServiceHealth(tickRunID string, total, synced, deferred, loginRequired int, oldestUnsyncedHrs float64, errorCount24h int) {
	writeEvent(buildPlaidEvent("plaid_service_health", tickRunID, map[string]any{
		"items_total":          total,
		"items_synced":         synced,
		"items_deferred":       deferred,
		"items_login_required": loginRequired,
		"oldest_unsynced_hrs":  oldestUnsyncedHrs,
		"error_count_24h":      errorCount24h,
	}))
}

// emitRateLimit emits a plaid_rate_limit event on 429 response.
func emitRateLimit(syncRunID, itemID string, retryAfterSecs, backoffAttempt int) {
	writeEvent(buildPlaidEvent("plaid_rate_limit", syncRunID, map[string]any{
		"item_id":          itemID,
		"retry_after_secs": retryAfterSecs,
		"backoff_attempt":  backoffAttempt,
	}))
}

// emitToolFailure emits a tool_failure event for any Plaid error.
func emitToolFailure(syncRunID, itemID, errorClass, errorCode, errorMessage string) {
	if len(errorMessage) > 200 {
		errorMessage = errorMessage[:200]
	}
	meta := map[string]any{
		"tool_name":     "plaid_sync",
		"error_class":   errorClass,
		"error_code":    errorCode,
		"error_message": errorMessage,
	}
	if itemID != "" {
		meta["item_id"] = itemID
	}
	writeEvent(buildPlaidEvent("tool_failure", syncRunID, meta))
}

type plaidEvent struct {
	Timestamp string         `json:"timestamp"`
	Layer     string         `json:"layer"`
	EventType string         `json:"event_type"`
	Command   string         `json:"command"`
	SessionID string         `json:"session_id"`
	Metadata  map[string]any `json:"metadata"`
}

func buildPlaidEvent(eventType, sessionID string, meta map[string]any) plaidEvent {
	return plaidEvent{
		Timestamp: time.Now().UTC().Format("20060102T150405Z"),
		Layer:     "go_cli",
		EventType: eventType,
		Command:   "plaid-service",
		SessionID: sessionID,
		Metadata:  meta,
	}
}

func writeEvent(e plaidEvent) {
	dir := eventsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "plaid-service: events dir: %v\n", err)
		return
	}
	path := filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl")
	data, _ := json.Marshal(e)
	data = append(data, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plaid-service: open events: %v\n", err)
		return
	}
	defer f.Close()
	f.Write(data)
}

func eventsDir() string {
	base := getenv("AUTOMATION_METRICS_DIR")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".automation-metrics")
	}
	return filepath.Join(base, "events")
}
