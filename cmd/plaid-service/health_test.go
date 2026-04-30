package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var baseTime = time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)

func syncedAt(hoursAgo float64) string {
	return baseTime.Add(-time.Duration(hoursAgo * float64(time.Hour))).Format(time.RFC3339)
}

func singleItem(lastSyncAt string) []ItemHealth {
	return []ItemHealth{{
		ItemID:     "item_1",
		LastSyncAt: lastSyncAt,
		Status:     "active",
	}}
}

// ── CT-1: 200 when all items synced within 2h ─────────────────────────────────

func TestHealthCT1_OkWhenFresh(t *testing.T) {
	resp := computeHealth(singleItem(syncedAt(1)), 0, baseTime)
	if resp.Status != "ok" {
		t.Errorf("status: got %q, want ok", resp.Status)
	}
	if resp.OldestUnsyncedHrs > 2 {
		t.Errorf("oldest_unsynced_hrs: got %v, want ≤ 2", resp.OldestUnsyncedHrs)
	}
}

// ── CT-2: 503 when any item unsynced > 4h ────────────────────────────────────

func TestHealthCT2_ErrorWhenStale(t *testing.T) {
	resp := computeHealth(singleItem(syncedAt(5)), 0, baseTime)
	if resp.Status != "error" {
		t.Errorf("status: got %q, want error", resp.Status)
	}
}

// ── CT-3: degraded when unsynced > 2h ≤ 4h ──────────────────────────────────

func TestHealthCT3_DegradedWhenModeratelyStale(t *testing.T) {
	resp := computeHealth(singleItem(syncedAt(3)), 0, baseTime)
	if resp.Status != "degraded" {
		t.Errorf("status: got %q, want degraded", resp.Status)
	}
}

// ── CT-4: degraded when errors_24h > 0, items fresh ─────────────────────────

func TestHealthCT4_DegradedOnErrors(t *testing.T) {
	resp := computeHealth(singleItem(syncedAt(1)), 1, baseTime)
	if resp.Status != "degraded" {
		t.Errorf("status: got %q, want degraded", resp.Status)
	}
	if resp.Errors24h != 1 {
		t.Errorf("errors_24h: got %d, want 1", resp.Errors24h)
	}
}

// ── CT-5: /health returns valid JSON with correct Content-Type ────────────────

func TestHealthCT5_JSONContentType(t *testing.T) {
	db := mustOpenDB(t)
	h := healthHandler(db)

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/health", nil)
	h.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	var out HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
}

// ── CT-6: health_metrics row written per item per tick ───────────────────────

func TestHealthCT6_MetricsRowWrittenPerTick(t *testing.T) {
	db := mustOpenDB(t)

	// Seed two active items.
	for _, id := range []string{"item_a", "item_b"} {
		db.Exec(`INSERT INTO plaid_items (item_id, institution_id, created_at, status) VALUES (?, '', ?, 'active')`, id, nowUTC())
		db.Exec(`INSERT INTO plaid_sync_state (item_id, retries) VALUES (?, 0)`, id)
	}

	sched := newScheduler(db, newPlaidClientFromParts(&mockTransactionsAPI{}, mustTokenStore(t, "tok"), db), mustTokenStore(t, "tok"))
	sched.writeHealthMetrics([]string{"item_a", "item_b"})

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM health_metrics`).Scan(&count)
	if count != 2 {
		t.Errorf("health_metrics rows: got %d, want 2", count)
	}
}

// ── CT-7: pruneHealthMetrics removes rows older than 7 days ──────────────────

func TestHealthCT7_PruneRemovesOldRows(t *testing.T) {
	db := mustOpenDB(t)

	old := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	db.Exec(`INSERT INTO health_metrics (sampled_at, item_id, tx_count_24h, errors_24h) VALUES (?, 'item_1', 0, 0)`, old)

	if err := pruneHealthMetrics(db, time.Now()); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM health_metrics`).Scan(&count)
	if count != 0 {
		t.Errorf("rows after prune: got %d, want 0", count)
	}
}

// ── BT-1: no active items → ok, 200 ─────────────────────────────────────────

func TestHealthBT1_NoItemsIsOk(t *testing.T) {
	resp := computeHealth(nil, 0, baseTime)
	if resp.Status != "ok" {
		t.Errorf("status: got %q, want ok", resp.Status)
	}
	if resp.OldestUnsyncedHrs != 0 {
		t.Errorf("oldest_unsynced_hrs: got %v, want 0", resp.OldestUnsyncedHrs)
	}
}

// ── BT-2: null last_sync_at treated as never synced → error status ────────────

func TestHealthBT2_NullLastSyncTreatedAsNeverSynced(t *testing.T) {
	resp := computeHealth(singleItem(""), 0, baseTime)
	if resp.Status != "error" {
		t.Errorf("status: got %q, want error (never synced)", resp.Status)
	}
}

// ── BT-3: oldest_unsynced_hrs picks the maximum lag across items ──────────────

func TestHealthBT3_OldestUnsyncedHrsPrecision(t *testing.T) {
	items := []ItemHealth{
		{ItemID: "item_a", LastSyncAt: syncedAt(1.5), Status: "active"},
		{ItemID: "item_b", LastSyncAt: syncedAt(3.2), Status: "active"},
	}
	resp := computeHealth(items, 0, baseTime)
	if resp.OldestUnsyncedHrs < 3.1 || resp.OldestUnsyncedHrs > 3.3 {
		t.Errorf("oldest_unsynced_hrs: got %v, want ~3.2", resp.OldestUnsyncedHrs)
	}
}

// ── RG-1/RG-2: compile-time + suite guard ────────────────────────────────────
// Verified by go build ./... and running the full test suite.
