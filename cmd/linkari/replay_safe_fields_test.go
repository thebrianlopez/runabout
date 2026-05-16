package main

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"testing"
)

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// CT-1: ContentHash("hello world") == SHA-256 hex of "hello world"
func TestReplaySafeCT1_ContentHashKnownInput(t *testing.T) {
	input := []byte("hello world")
	h := sha256.Sum256(input)
	want := fmt.Sprintf("%x", h)

	got := ContentHash(input)
	if got != want {
		t.Errorf("CT-1: ContentHash(%q) = %q, want %q", input, got, want)
	}
	if len(got) != 64 {
		t.Errorf("CT-1: ContentHash result len=%d, want 64 (SHA-256 hex)", len(got))
	}
}

// CT-2: Queue row created at intake has non-empty 64-char hex content_hash
func TestReplaySafeCT2_ContentHashStoredOnRow(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	id, err := q.Enqueue(&ShareRequest{URL: "https://ct2f3.example.com", Type: "url", Profile: "life"})
	if err != nil {
		t.Fatal(err)
	}

	// content_hash must be populated at intake (via handleShare M9 wiring)
	var contentHash string
	err = q.db.QueryRow("SELECT content_hash FROM queue WHERE id=?", id).Scan(&contentHash)
	if err != nil {
		t.Fatalf("CT-2: SELECT content_hash failed (column may not exist yet): %v", err)
	}
	if len(contentHash) != 64 {
		t.Errorf("CT-2: content_hash len=%d, want 64 (SHA-256 hex); got %q", len(contentHash), contentHash)
	}
}

// CT-3: Queue row created at intake has a valid UUID v4 trace_id
func TestReplaySafeCT3_TraceIDStoredOnRow(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	// Inject a deterministic UUID for this test
	origGen := generateTraceID
	generateTraceID = func() string { return "12345678-1234-4123-a123-123456789abc" }
	defer func() { generateTraceID = origGen }()

	id, err := q.Enqueue(&ShareRequest{URL: "https://ct3f3.example.com", Type: "url", Profile: "life"})
	if err != nil {
		t.Fatal(err)
	}

	var traceID string
	err = q.db.QueryRow("SELECT trace_id FROM queue WHERE id=?", id).Scan(&traceID)
	if err != nil {
		t.Fatalf("CT-3: SELECT trace_id failed (column may not exist yet): %v", err)
	}
	if !uuidV4Re.MatchString(traceID) {
		t.Errorf("CT-3: trace_id=%q is not a valid UUID v4", traceID)
	}
}

// CT-4: trace_id immutable across retries — retryOrFail must not overwrite trace_id
func TestReplaySafeCT4_TraceIDImmutableAcrossRetries(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	id, err := q.Enqueue(&ShareRequest{URL: "https://ct4f3.example.com", Type: "url", Profile: "life"})
	if err != nil {
		t.Fatal(err)
	}

	// Seed a trace_id directly (simulating M9 wiring)
	const wantTraceID = "aaaaaaaa-bbbb-4ccc-addd-eeeeeeeeeeee"
	_, seedErr := q.db.Exec("UPDATE queue SET trace_id=? WHERE id=?", wantTraceID, id)
	if seedErr != nil {
		t.Skipf("CT-4: trace_id column not yet added (M8 dependency): %v", seedErr)
	}

	// Simulate a scoring failure (first attempt — will retry, not terminal)
	if rErr := retryOrFail(t.Context(), q.db, id, "scoring", fmt.Errorf("eval failed")); rErr != nil {
		t.Fatal(rErr)
	}

	var gotTraceID string
	q.db.QueryRow("SELECT trace_id FROM queue WHERE id=?", id).Scan(&gotTraceID)
	if gotTraceID != wantTraceID {
		t.Errorf("CT-4: trace_id=%q after retry, want %q (must be immutable)", gotTraceID, wantTraceID)
	}
}

// CT-5: Schema migration adds content_hash and trace_id with DEFAULT '' (not NULL).
// Tests that rows inserted WITHOUT specifying these columns get '' not NULL.
func TestReplaySafeCT5_SchemaMigrationDefaultEmpty(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	// Insert directly WITHOUT specifying content_hash or trace_id,
	// simulating a pre-migration row that gets the DEFAULT '' from the schema.
	res, err := q.db.Exec(
		`INSERT INTO queue (url, text, type, action, profile, status, queued_at, slug)
		 VALUES ('https://ct5f3-premigration.example.com', '', 'url', '', 'life', 'pending', ?, '')`,
		"2026-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("CT-5: direct insert: %v", err)
	}
	id, _ := res.LastInsertId()

	var contentHash, traceID *string
	err = q.db.QueryRow("SELECT content_hash, trace_id FROM queue WHERE id=?", id).Scan(&contentHash, &traceID)
	if err != nil {
		t.Fatalf("CT-5: columns not yet in schema (M8 dependency): %v", err)
	}

	// DEFAULT '' means pointer is non-nil with empty string value, not NULL
	if contentHash == nil {
		t.Error("CT-5: content_hash is NULL, want '' (DEFAULT '')")
	} else if *contentHash != "" {
		t.Errorf("CT-5: content_hash=%q on pre-migration row, want ''", *contentHash)
	}
	if traceID == nil {
		t.Error("CT-5: trace_id is NULL, want '' (DEFAULT '')")
	} else if *traceID != "" {
		t.Errorf("CT-5: trace_id=%q on pre-migration row, want ''", *traceID)
	}
}

// CT-6: trace_id from queue row is propagated to slog context during pipeline
func TestReplaySafeCT6_TraceIDInQueueItem(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	id, err := q.Enqueue(&ShareRequest{URL: "https://ct6f3.example.com", Type: "url", Profile: "life"})
	if err != nil {
		t.Fatal(err)
	}

	const wantTraceID = "ffffffff-eeee-4aaa-bbbb-cccccccccccc"
	_, seedErr := q.db.Exec("UPDATE queue SET trace_id=? WHERE id=?", wantTraceID, id)
	if seedErr != nil {
		t.Skipf("CT-6: trace_id column not yet added (M8 dependency): %v", seedErr)
	}

	row, getErr := q.GetByID(id)
	if getErr != nil {
		t.Fatalf("CT-6: GetByID: %v", getErr)
	}

	// After M9 wiring, QueueItem should expose TraceID; verified here.
	if row.TraceID != wantTraceID {
		t.Errorf("CT-6: QueueItem.TraceID=%q, want %q", row.TraceID, wantTraceID)
	}
}

// CT-7: Different content produces different hashes
func TestReplaySafeCT7_DifferentContentDifferentHash(t *testing.T) {
	h1 := ContentHash([]byte("content A — first document"))
	h2 := ContentHash([]byte("content B — different document"))

	if h1 == h2 {
		t.Errorf("CT-7: ContentHash produced identical hashes for different inputs: %q", h1)
	}
	if len(h1) != 64 || len(h2) != 64 {
		t.Errorf("CT-7: hash lengths: h1=%d, h2=%d, both want 64", len(h1), len(h2))
	}
}
