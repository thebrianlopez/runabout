//go:build integration

// M2: schema eval harness. Fetches recently-scored items from a live queue DB
// and asserts every UI-bound field the Android HistoryActivity reads is
// populated. Gated behind the `integration` build tag so default
// `go test ./...` stays hermetic.
//
// Run with:
//
//	LINKARI_QUEUE_DB=/path/to/queue.db go test -tags=integration ./cmd/linkari/... -run TestTriageVerdictSchema
//
// If LINKARI_QUEUE_DB is unset, the test skips. This is eval-harness
// observability for a non-deterministic scorer, not a pass/fail gate.
package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestTriageVerdictSchema(t *testing.T) {
	dbPath := os.Getenv("LINKARI_QUEUE_DB")
	if dbPath == "" {
		t.Skip("LINKARI_QUEUE_DB not set — live eval harness skipped")
	}
	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	defer q.Close()

	since := time.Now().Add(-7 * 24 * time.Hour)
	items, err := q.RecentScored(since, 20)
	if err != nil {
		t.Fatalf("recent scored: %v", err)
	}
	if len(items) == 0 {
		t.Skip("no scored items in last 7d — nothing to assert")
	}

	t.Logf("asserting UI-bound schema on %d items", len(items))
	for _, it := range items {
		// Android HistoryActivity binds: id, score, profile, verdict, slug,
		// tags, scored_at. Any missing field breaks list rendering.
		if it.ID == 0 {
			t.Errorf("id=0 row slipped through query")
		}
		if strings.TrimSpace(it.Verdict) == "" {
			t.Errorf("id=%d empty verdict — scorer did not emit body", it.ID)
		}
		if len(it.Verdict) < 40 {
			t.Errorf("id=%d truncated verdict (len=%d): %q", it.ID, len(it.Verdict), it.Verdict)
		}
		if it.Profile == "" {
			t.Errorf("id=%d empty profile", it.ID)
		}
		if it.ScoredAt == "" {
			t.Errorf("id=%d empty scored_at timestamp", it.ID)
		}
		if it.Status == "scored" {
			if it.Score == nil || *it.Score == 0 {
				t.Errorf("id=%d status=scored but score is zero/nil", it.ID)
			}
		}
		if it.Slug == "" {
			t.Logf("id=%d empty slug (non-fatal, scorer permits)", it.ID)
		}
	}
}
