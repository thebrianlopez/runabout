package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// readEvents reads all JSONL events written to the temp event bus dir.
func readEvents(t *testing.T, dir string) []map[string]any {
	t.Helper()
	path := filepath.Join(dir, "events", time.Now().UTC().Format("2006-01-02")+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("open events file: %v", err)
	}
	defer f.Close()

	var events []map[string]any
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("parse event line %q: %v", line, err)
		}
		events = append(events, m)
	}
	return events
}

func eventsOfType(events []map[string]any, eventType string) []map[string]any {
	var out []map[string]any
	for _, e := range events {
		if e["event_type"] == eventType {
			out = append(out, e)
		}
	}
	return out
}

func metaFloat(t *testing.T, e map[string]any, key string) float64 {
	t.Helper()
	meta, ok := e["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata missing or wrong type")
	}
	v, ok := meta[key].(float64)
	if !ok {
		t.Fatalf("metadata[%q] not float64 (got %T = %v)", key, meta[key], meta[key])
	}
	return v
}

// ── CT-1: emitTransactionBatch writes event when counts > 0 ──────────────────

func TestEvCT1_TransactionBatchWrittenWhenCounts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	emitTransactionBatch("run_1", "item_1", &SyncResult{Added: 5, Modified: 1, RunID: "run_1"})

	events := readEvents(t, dir)
	batches := eventsOfType(events, "plaid_transaction_batch")
	if len(batches) != 1 {
		t.Fatalf("expected 1 plaid_transaction_batch, got %d", len(batches))
	}
	if v := metaFloat(t, batches[0], "tx_added"); v != 5 {
		t.Errorf("tx_added: got %v, want 5", v)
	}
}

// ── CT-2: emitTransactionBatch NOT written when all counts == 0 ───────────────

func TestEvCT2_TransactionBatchSuppressedOnZeroCounts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	emitTransactionBatch("run_1", "item_1", &SyncResult{Added: 0, Modified: 0, Removed: 0})

	events := readEvents(t, dir)
	if len(eventsOfType(events, "plaid_transaction_batch")) != 0 {
		t.Error("plaid_transaction_batch should not be emitted when all counts are 0")
	}
}

// ── CT-3: emitServiceHealth always written, even on 0-item tick ───────────────

func TestEvCT3_ServiceHealthAlwaysEmitted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	emitServiceHealth("run_1", 0, 0, 0, 0, 0, 0)

	events := readEvents(t, dir)
	health := eventsOfType(events, "plaid_service_health")
	if len(health) != 1 {
		t.Fatalf("expected 1 plaid_service_health, got %d", len(health))
	}
	if v := metaFloat(t, health[0], "items_total"); v != 0 {
		t.Errorf("items_total: got %v, want 0", v)
	}
}

// ── CT-4: emitRateLimit written on 429 ────────────────────────────────────────

func TestEvCT4_RateLimitEmitted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	emitRateLimit("run_1", "item_1", 60, 1)

	events := readEvents(t, dir)
	rls := eventsOfType(events, "plaid_rate_limit")
	if len(rls) != 1 {
		t.Fatalf("expected 1 plaid_rate_limit, got %d", len(rls))
	}
	if v := metaFloat(t, rls[0], "retry_after_secs"); v != 60 {
		t.Errorf("retry_after_secs: got %v, want 60", v)
	}
}

// ── CT-5: session_id equals sync_run_id on all events ────────────────────────

func TestEvCT5_SessionIDEqualsRunID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	const runID = "test-run-id-abc"
	emitTransactionBatch(runID, "item_1", &SyncResult{Added: 1, RunID: runID})
	emitServiceHealth(runID, 1, 1, 0, 0, 0, 0)
	emitRateLimit(runID, "item_1", 60, 1)

	for _, e := range readEvents(t, dir) {
		if sid, _ := e["session_id"].(string); sid != runID {
			t.Errorf("event_type=%q: session_id got %q, want %q", e["event_type"], sid, runID)
		}
	}
}

// ── CT-6: writeEvent creates correct UTC-date file ────────────────────────────

func TestEvCT6_CorrectUTCDateFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	emitServiceHealth("run_1", 0, 0, 0, 0, 0, 0)

	expectedFile := filepath.Join(dir, "events", time.Now().UTC().Format("2006-01-02")+".jsonl")
	if _, err := os.Stat(expectedFile); err != nil {
		t.Errorf("expected events file %s not found: %v", expectedFile, err)
	}
}

// ── CT-7: each line in JSONL is valid JSON ────────────────────────────────────

func TestEvCT7_EachLineValidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	emitTransactionBatch("run_1", "item_1", &SyncResult{Added: 3})
	emitServiceHealth("run_1", 1, 1, 0, 0, 0, 0)
	emitRateLimit("run_1", "item_1", 60, 1)

	path := filepath.Join(dir, "events", time.Now().UTC().Format("2006-01-02")+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open events file: %v", err)
	}
	defer f.Close()

	lineNum := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("line %d is not valid JSON: %v (content: %s)", lineNum, err, line)
		}
	}
	if lineNum != 3 {
		t.Errorf("expected 3 lines, got %d", lineNum)
	}
}

// ── BT-1: writeEvent appends — two calls produce two lines ───────────────────

func TestEvBT1_AppendsOnSecondCall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	emitServiceHealth("run_1", 1, 1, 0, 0, 0, 0)
	emitServiceHealth("run_2", 2, 2, 0, 0, 0, 0)

	events := readEvents(t, dir)
	if len(events) != 2 {
		t.Errorf("expected 2 events after 2 emits, got %d", len(events))
	}
}

// ── BT-3: cursor_committed true on successful sync ────────────────────────────

func TestEvBT3_CursorCommittedTrue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	emitTransactionBatch("run_1", "item_1", &SyncResult{Added: 2, Cursor: "cur_xyz", RunID: "run_1"})

	batches := eventsOfType(readEvents(t, dir), "plaid_transaction_batch")
	if len(batches) == 0 {
		t.Fatal("no plaid_transaction_batch emitted")
	}
	meta := batches[0]["metadata"].(map[string]any)
	if committed, _ := meta["cursor_committed"].(bool); !committed {
		t.Errorf("cursor_committed: got %v, want true", meta["cursor_committed"])
	}
}

// ── BT-4: oldest_unsynced_hrs computed from SQLite last_sync_at ───────────────

func TestEvBT4_OldestUnsyncedHrsFromDB(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	twoHoursAgo := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	mustExec(t, db, `UPDATE plaid_sync_state SET last_sync_at = ? WHERE item_id = 'item_1'`, twoHoursAgo)

	hrs := computeOldestUnsyncedHrs(db)
	if hrs < 1.9 || hrs > 2.1 {
		t.Errorf("oldest_unsynced_hrs: got %.2f, want ≈2.0", hrs)
	}
}

// ── RG-1: plaid_service_health emitted even when all items fail ───────────────

func TestEvRG1_HealthEmittedWhenAllItemsFail(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	client := &mockPlaidClient{
		syncTransactions: func(_ context.Context, _ string) (*SyncResult, error) {
			return nil, &SyncError{EventClass: "cursor_corrupted", Err: errors.New("api error")}
		},
	}
	s := newScheduler(db, client, nil)
	s.tick()

	health := eventsOfType(readEvents(t, dir), "plaid_service_health")
	if len(health) == 0 {
		t.Error("plaid_service_health must be emitted even when all items fail")
	}
	if v := metaFloat(t, health[0], "items_synced"); v != 0 {
		t.Errorf("items_synced: got %v, want 0", v)
	}
}

// ── RG-2: session_id consistent across events within one tick ─────────────────

func TestEvRG2_SessionIDConsistentWithinTick(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	seedAccount(t, db, "acct_1", "item_1")

	client := &mockPlaidClient{
		syncTransactions: func(_ context.Context, _ string) (*SyncResult, error) {
			return &SyncResult{Added: 1, Cursor: "c1", RunID: "run_x"}, nil
		},
	}
	s := newScheduler(db, client, nil)
	s.tick()

	events := readEvents(t, dir)
	if len(events) == 0 {
		t.Fatal("no events emitted")
	}
	firstSID, _ := events[0]["session_id"].(string)
	if firstSID == "" {
		t.Fatal("session_id is blank")
	}
	for i, e := range events {
		sid, _ := e["session_id"].(string)
		if sid != firstSID {
			t.Errorf("event %d: session_id %q != %q", i, sid, firstSID)
		}
	}
}
