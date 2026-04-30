package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"time"
)

// ── types ─────────────────────────────────────────────────────────────────────

type HealthResponse struct {
	Status            string       `json:"status"`
	Items             []ItemHealth `json:"items"`
	Errors24h         int          `json:"errors_24h"`
	OldestUnsyncedHrs float64      `json:"oldest_unsynced_hrs"`
}

type ItemHealth struct {
	ItemID        string `json:"item_id"`
	InstitutionID string `json:"institution_id"`
	LastSyncAt    string `json:"last_sync_at"`
	NextSyncAt    string `json:"next_sync_at"`
	Status        string `json:"status"`
	TxCount24h    int    `json:"tx_count_24h"`
}

// ── computeHealth (pure) ──────────────────────────────────────────────────────

// computeHealth derives a HealthResponse from active items and a reference time.
// Pure function — no DB access.
func computeHealth(items []ItemHealth, errors24h int, now time.Time) HealthResponse {
	oldest := 0.0
	for _, item := range items {
		var hrs float64
		if item.LastSyncAt == "" {
			hrs = 999 // never synced
		} else {
			t, err := time.Parse(time.RFC3339, item.LastSyncAt)
			if err != nil {
				hrs = 999
			} else {
				hrs = now.Sub(t).Hours()
				if hrs < 0 {
					hrs = 0
				}
			}
		}
		if hrs > oldest {
			oldest = hrs
		}
	}

	status := "ok"
	if oldest > 4 {
		status = "error"
	} else if oldest > 2 || errors24h > 0 {
		status = "degraded"
	}

	return HealthResponse{
		Status:            status,
		Items:             items,
		Errors24h:         errors24h,
		OldestUnsyncedHrs: math.Round(oldest*10) / 10,
	}
}

// ── HTTP handler ──────────────────────────────────────────────────────────────

// healthHandler returns an http.Handler serving GET /health.
func healthHandler(db *sql.DB) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := queryActiveItemHealth(db)
		errors24h := queryErrors24h(db)
		resp := computeHealth(items, errors24h, time.Now().UTC())

		code := http.StatusOK
		if resp.Status == "error" {
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(resp)
	})
}

func queryActiveItemHealth(db *sql.DB) []ItemHealth {
	rows, err := db.Query(`
		SELECT pi.item_id, pi.institution_id, pi.status,
		       COALESCE(ss.last_sync_at, ''), COALESCE(ss.next_sync_at, '')
		FROM plaid_items pi
		LEFT JOIN plaid_sync_state ss ON ss.item_id = pi.item_id
		WHERE pi.status = 'active'
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	var items []ItemHealth
	for rows.Next() {
		var item ItemHealth
		if err := rows.Scan(&item.ItemID, &item.InstitutionID, &item.Status, &item.LastSyncAt, &item.NextSyncAt); err != nil {
			continue
		}
		db.QueryRow(`SELECT COUNT(*) FROM plaid_transactions_raw WHERE item_id = ? AND ingested_at > ?`,
			item.ItemID, cutoff).Scan(&item.TxCount24h)
		items = append(items, item)
	}
	return items
}

func queryErrors24h(db *sql.DB) int {
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM plaid_sync_journal WHERE status = 'error' AND started_at > ?`, cutoff).Scan(&count)
	return count
}

// ── health_metrics persistence ────────────────────────────────────────────────

// writeHealthMetric writes one snapshot row per item per tick.
func writeHealthMetric(db *sql.DB, itemID, lastSyncAt string, txCount24h, errors24h int) error {
	_, err := db.Exec(`
		INSERT INTO health_metrics (sampled_at, item_id, last_sync_at, tx_count_24h, errors_24h)
		VALUES (?, ?, ?, ?, ?)`,
		nowUTC(), itemID, lastSyncAt, txCount24h, errors24h,
	)
	return err
}

// pruneHealthMetrics deletes rows older than 7 days from now.
func pruneHealthMetrics(db *sql.DB, now time.Time) error {
	cutoff := now.UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	_, err := db.Exec(`DELETE FROM health_metrics WHERE sampled_at < ?`, cutoff)
	return err
}

// runWeeklyPrune runs pruneHealthMetrics on a 7-day ticker until ctx is cancelled.
func runWeeklyPrune(ctx context.Context, db *sql.DB) {
	ticker := time.NewTicker(7 * 24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			pruneHealthMetrics(db, t)
		}
	}
}
