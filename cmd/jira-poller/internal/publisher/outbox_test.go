package publisher_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/publisher"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/types"
	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := publisher.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func makeEvent(id string) types.TransitionEvent {
	return types.TransitionEvent{
		IssueKey:       "PROJ-1",
		ProjectKey:     "PROJ",
		IssueType:      "Bug",
		Summary:        "Test issue",
		FromStatus:     "To Do",
		ToStatus:       "In Progress",
		TransitionedAt: time.Unix(1000, 0).UTC(),
		TransitionedBy: types.UserRef{AccountID: "u1", DisplayName: "Alice"},
		Labels:         []string{},
		ChangelogID:    id,
		Self:           "https://jira.example.com/PROJ-1",
	}
}

func fixedNow(ts int64) func() time.Time {
	return func() time.Time { return time.Unix(ts, 0) }
}

// fakeSink captures delivered events and optionally returns an error.
type fakeSink struct {
	Delivered [][]types.TransitionEvent
	Err       error
}

func (f *fakeSink) Deliver(_ context.Context, events []types.TransitionEvent) error {
	f.Delivered = append(f.Delivered, events)
	return f.Err
}

// CT-6: Publish inserts all events to pending_events.
func TestPublish_CT6_InsertsAll(t *testing.T) {
	db := openTestDB(t)
	p := publisher.NewSQLitePublisher(db)

	events := []types.TransitionEvent{makeEvent("id1"), makeEvent("id2"), makeEvent("id3")}
	events[1].ChangelogID = "id2"
	events[2].ChangelogID = "id3"

	_, err := p.Publish(context.Background(), events)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var count int
	db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pending_events`).Scan(&count)
	if count != 3 {
		t.Errorf("want 3 rows, got %d", count)
	}
}

// CT-7: Publish returns all ChangelogIDs in PublishResult.Succeeded.
func TestPublish_CT7_SucceededIDs(t *testing.T) {
	db := openTestDB(t)
	p := publisher.NewSQLitePublisher(db)

	e1, e2 := makeEvent("id1"), makeEvent("id2")
	result, err := p.Publish(context.Background(), []types.TransitionEvent{e1, e2})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(result.Succeeded) != 2 {
		t.Fatalf("want 2 Succeeded, got %d", len(result.Succeeded))
	}
	if result.Succeeded[0] != "id1" || result.Succeeded[1] != "id2" {
		t.Errorf("Succeeded = %v", result.Succeeded)
	}
}

// CT-8: Publish reports constraint-conflicting event in PublishResult.Failed.
func TestPublish_CT8_DuplicateInFailed(t *testing.T) {
	db := openTestDB(t)
	p := publisher.NewSQLitePublisher(db)
	ctx := context.Background()

	// First insert.
	if _, err := p.Publish(ctx, []types.TransitionEvent{makeEvent("id1")}); err != nil {
		t.Fatal(err)
	}

	// Second insert of same ID — UNIQUE constraint. INSERT OR IGNORE means it won't fail
	// but RowsAffected=0, so we treat it like a duplicate skip (goes to Succeeded with
	// OR IGNORE semantics, not Failed). Actually per the TDD: "already in outbox →
	// len(Failed)==1, Succeeded excludes it". But INSERT OR IGNORE doesn't error.
	// Let me check: the TDD says "Publish reports constraint-conflicting event in
	// PublishResult.Failed". This implies the event is in Failed. But INSERT OR IGNORE
	// silently ignores duplicates. The implementation needs to check RowsAffected.
	//
	// Per the implementation: INSERT OR IGNORE + check RowsAffected. If 0, it's a
	// duplicate and goes to Failed, not Succeeded.
	e := makeEvent("id1")
	e.Summary = "updated" // different content but same event_id
	result, err := p.Publish(ctx, []types.TransitionEvent{e})
	if err != nil {
		t.Fatalf("Publish duplicate: %v", err)
	}
	if len(result.Failed) != 1 {
		t.Errorf("want 1 Failed, got %d", len(result.Failed))
	}
	if len(result.Succeeded) != 0 {
		t.Errorf("want 0 Succeeded for duplicate, got %d", len(result.Succeeded))
	}
	// Confirm only 1 row in DB.
	var count int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_events WHERE event_id='id1'`).Scan(&count)
	if count != 1 {
		t.Errorf("pending_events count for id1 = %d, want 1", count)
	}
}

// CT-9: Publish returns ErrOutboxWrite when DB exec fails entirely.
func TestPublish_CT9_ClosedDB_ErrOutboxWrite(t *testing.T) {
	db := openTestDB(t)
	p := publisher.NewSQLitePublisher(db)
	db.Close()

	_, err := p.Publish(context.Background(), []types.TransitionEvent{makeEvent("id1")})
	if !errors.Is(err, publisher.ErrOutboxWrite) {
		t.Errorf("err = %v, want ErrOutboxWrite", err)
	}
}

// CT-10: Outbox payload JSON matches epic.md schema field names.
func TestPublish_CT10_PayloadSchema(t *testing.T) {
	db := openTestDB(t)
	p := publisher.NewSQLitePublisher(db)

	ev := types.TransitionEvent{
		IssueKey:       "INFRA-1234",
		ProjectKey:     "INFRA",
		IssueType:      "Story",
		Summary:        "Build it",
		FromStatus:     "In Progress",
		ToStatus:       "Done",
		TransitionedAt: time.Unix(1000, 0).UTC(),
		TransitionedBy: types.UserRef{AccountID: "u1", DisplayName: "Alice", Email: "alice@example.com"},
		Labels:         []string{"backend"},
		ChangelogID:    "10234",
		Self:           "https://jira.example.com/INFRA-1234",
	}
	if _, err := p.Publish(context.Background(), []types.TransitionEvent{ev}); err != nil {
		t.Fatal(err)
	}

	var payload string
	db.QueryRowContext(context.Background(),
		`SELECT payload FROM pending_events WHERE event_id = ?`, "10234",
	).Scan(&payload)

	// Check required field names are present.
	for _, field := range []string{"issue_key", "changelog_id", "transitioned_by", "transitioned_at", "to_status"} {
		if !contains(payload, `"`+field+`"`) {
			t.Errorf("payload missing field %q: %s", field, payload)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && findIn(s, sub)
}

func findIn(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// CT-11: Drain worker delivers pending events to fake Sink.
func TestDrain_CT11_DeliversPending(t *testing.T) {
	db := openTestDB(t)
	p := publisher.NewSQLitePublisher(db)
	ctx := context.Background()

	const now int64 = 1000
	// Insert two events.
	e1, e2 := makeEvent("id1"), makeEvent("id2")
	if _, err := p.Publish(ctx, []types.TransitionEvent{e1, e2}); err != nil {
		t.Fatal(err)
	}

	sink := &fakeSink{}
	publisher.DrainOnce(ctx, db, sink, fixedNow(now))

	if len(sink.Delivered) != 1 {
		t.Fatalf("want 1 Deliver call, got %d", len(sink.Delivered))
	}
	if len(sink.Delivered[0]) != 2 {
		t.Errorf("want 2 events delivered, got %d", len(sink.Delivered[0]))
	}

	// Rows should be marked delivered.
	var count int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_events WHERE status='delivered'`).Scan(&count)
	if count != 2 {
		t.Errorf("want 2 delivered rows, got %d", count)
	}
}

// CT-12: Drain worker increments attempts and applies backoff on Sink.Deliver error.
func TestDrain_CT12_BackoffOnError(t *testing.T) {
	db := openTestDB(t)
	p := publisher.NewSQLitePublisher(db)
	ctx := context.Background()

	const now int64 = 1000
	if _, err := p.Publish(ctx, []types.TransitionEvent{makeEvent("id1")}); err != nil {
		t.Fatal(err)
	}

	sink := &fakeSink{Err: errors.New("delivery failed")}
	publisher.DrainOnce(ctx, db, sink, fixedNow(now))

	var attempts int
	var nextAttemptAt int64
	db.QueryRowContext(ctx,
		`SELECT attempts, next_attempt_at FROM pending_events WHERE event_id='id1'`,
	).Scan(&attempts, &nextAttemptAt)

	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	// After first failure: next_attempt_at = now + 30s
	if nextAttemptAt != now+30 {
		t.Errorf("next_attempt_at = %d, want %d (now+30)", nextAttemptAt, now+30)
	}
}

// CT-13: Publish with 0 events returns empty PublishResult, no DB writes.
func TestPublish_CT13_EmptyInput(t *testing.T) {
	db := openTestDB(t)
	p := publisher.NewSQLitePublisher(db)

	result, err := p.Publish(context.Background(), []types.TransitionEvent{})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(result.Succeeded) != 0 || len(result.Failed) != 0 {
		t.Errorf("want empty result, got %+v", result)
	}

	var count int
	db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pending_events`).Scan(&count)
	if count != 0 {
		t.Errorf("want 0 rows, got %d", count)
	}
}
