package dedupe_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/blo-grindr/runabout/cmd/jira-poller/internal/dedupe"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dedupe.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	if err := dedupe.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func fixedClock(ts int64) func() time.Time {
	return func() time.Time { return time.Unix(ts, 0) }
}

// CT-1: Mark returns isNew=true on first call.
func TestMark_CT1_FirstCall_IsNew(t *testing.T) {
	db := openTestDB(t)
	store := dedupe.NewSQLiteStore(db, time.Now)

	isNew, err := store.Mark(context.Background(), "INFRA-1:10", 7*24*time.Hour)
	if err != nil {
		t.Fatalf("Mark: %v", err)
	}
	if !isNew {
		t.Error("want isNew=true on first call")
	}
}

// CT-2: Mark returns (false, nil) on duplicate.
func TestMark_CT2_Duplicate_NotNew(t *testing.T) {
	db := openTestDB(t)
	store := dedupe.NewSQLiteStore(db, time.Now)
	ctx := context.Background()

	if _, err := store.Mark(ctx, "INFRA-1:10", time.Hour); err != nil {
		t.Fatalf("first Mark: %v", err)
	}
	isNew, err := store.Mark(ctx, "INFRA-1:10", time.Hour)
	if err != nil {
		t.Fatalf("second Mark: %v", err)
	}
	if isNew {
		t.Error("want isNew=false on duplicate")
	}
}

// CT-3: Mark returns (false, ErrDedupeWrite) when DB is closed.
func TestMark_CT3_ClosedDB_ErrDedupeWrite(t *testing.T) {
	db := openTestDB(t)
	store := dedupe.NewSQLiteStore(db, time.Now)
	db.Close()

	isNew, err := store.Mark(context.Background(), "X:1", time.Hour)
	if err == nil {
		t.Fatal("expected error on closed DB")
	}
	if !errors.Is(err, dedupe.ErrDedupeWrite) {
		t.Errorf("err = %v, want ErrDedupeWrite", err)
	}
	if isNew {
		t.Error("want isNew=false on error")
	}
}

// CT-4: seen_events row has correct event_id and expires_at after Mark.
func TestMark_CT4_RowExpiry(t *testing.T) {
	db := openTestDB(t)
	const epoch int64 = 1000
	store := dedupe.NewSQLiteStore(db, fixedClock(epoch))

	ttl := 7 * 24 * time.Hour
	if _, err := store.Mark(context.Background(), "INFRA-1:10", ttl); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	var gotEventID string
	var gotExpiresAt int64
	err := db.QueryRowContext(context.Background(),
		`SELECT event_id, expires_at FROM seen_events WHERE event_id = ?`,
		"INFRA-1:10",
	).Scan(&gotEventID, &gotExpiresAt)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}

	wantExpiresAt := epoch + int64(ttl.Seconds())
	if gotEventID != "INFRA-1:10" {
		t.Errorf("event_id = %q, want INFRA-1:10", gotEventID)
	}
	if gotExpiresAt != wantExpiresAt {
		t.Errorf("expires_at = %d, want %d", gotExpiresAt, wantExpiresAt)
	}
}

// CT-5: expires_at uses injected nowFn, not wall clock.
func TestMark_CT5_NowFnInjection(t *testing.T) {
	db := openTestDB(t)
	const epoch int64 = 1000
	store := dedupe.NewSQLiteStore(db, fixedClock(epoch))

	const ttl = 10 * time.Second
	if _, err := store.Mark(context.Background(), "X:2", ttl); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	var expiresAt int64
	db.QueryRowContext(context.Background(),
		`SELECT expires_at FROM seen_events WHERE event_id = ?`, "X:2",
	).Scan(&expiresAt)

	if expiresAt != epoch+10 {
		t.Errorf("expires_at = %d, want %d (epoch + 10s)", expiresAt, epoch+10)
	}
}
