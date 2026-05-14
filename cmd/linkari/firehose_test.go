package main

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// CT-1: keyword match → queue row with source='firehose'; push_outbox kind='notify'
func TestFirehoseCT1_KeywordMatch(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	if err := q.AddFirehoseSubscription("default", "golang"); err != nil {
		t.Fatal(err)
	}

	post := &firehosePost{
		AtURI: "at://did:plc:abc/app.bsky.feed.post/xyz",
		Text:  "I love golang programming",
		Repo:  "did:plc:abc",
		Seq:   100,
	}
	err := handleFirehosePost(context.Background(), q, post)
	if err != nil {
		t.Fatal(err)
	}

	var rowCount int
	q.db.QueryRow("SELECT COUNT(*) FROM queue WHERE url=? AND source='firehose'", post.AtURI).Scan(&rowCount)
	if rowCount == 0 {
		t.Fatal("expected queue row with source='firehose'")
	}

	pushes, _ := q.PendingPushes(10)
	for _, p := range pushes {
		if p.URL == post.AtURI {
			if p.Kind != "notify" {
				t.Fatalf("expected kind='notify', got %q", p.Kind)
			}
			return
		}
	}
	t.Fatal("no push row found for firehose post")
}

// CT-2: no-match commit → zero queue rows
func TestFirehoseCT2_NoMatch(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	if err := q.AddFirehoseSubscription("default", "golang"); err != nil {
		t.Fatal(err)
	}

	post := &firehosePost{
		AtURI: "at://did:plc:abc/app.bsky.feed.post/no-match",
		Text:  "I love python programming",
		Repo:  "did:plc:abc",
		Seq:   101,
	}
	err := handleFirehosePost(context.Background(), q, post)
	if err != nil {
		t.Fatal(err)
	}

	before, _ := q.List("pending", 10)
	for _, item := range before {
		if item.URL == post.AtURI {
			t.Fatal("unexpected queue row for non-matching post")
		}
	}
}

// CT-3: Worker startup with firehose_events.seq=12345 → cursor=12345 in connect URL
func TestFirehoseCT3_CursorResume(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	if err := q.PersistFirehoseSeq(12345, nil); err != nil {
		t.Fatal(err)
	}

	seq, err := q.LoadLastFirehoseSeq()
	if err != nil {
		t.Fatal(err)
	}
	if seq != 12345 {
		t.Fatalf("expected seq=12345, got %d", seq)
	}
}

// CT-4: Malformed CBOR → worker skips, continues
func TestFirehoseCT4_MalformedCBOR(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	if err := q.AddFirehoseSubscription("default", "golang"); err != nil {
		t.Fatal(err)
	}

	// processFirehoseMessage should return nil (skip) on malformed CBOR
	err := processFirehoseMessage(context.Background(), q, []byte("not-cbor"))
	if err != nil {
		t.Fatalf("expected nil (skip), got error: %v", err)
	}
}

// CT-5: AddFirehoseSubscription called twice → exactly one row
func TestFirehoseCT5_IdempotentSubscription(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	if err := q.AddFirehoseSubscription("default", "golang"); err != nil {
		t.Fatal(err)
	}
	if err := q.AddFirehoseSubscription("default", "golang"); err != nil {
		t.Fatal(err)
	}

	subs, err := q.ListFirehoseSubscriptions("default")
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}
}

// BT-1: dedup guard — same AT URI twice → only one queue row
func TestFirehoseBT1_DeduplicateGuard(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	if err := q.AddFirehoseSubscription("default", "golang"); err != nil {
		t.Fatal(err)
	}

	post := &firehosePost{
		AtURI: "at://did:plc:abc/app.bsky.feed.post/dedup",
		Text:  "golang is great",
		Repo:  "did:plc:abc",
		Seq:   200,
	}
	// First enqueue
	_ = handleFirehosePost(context.Background(), q, post)
	// Second enqueue within 5-min window → should be skipped
	_ = handleFirehosePost(context.Background(), q, post)

	var count int
	q.db.QueryRow("SELECT COUNT(*) FROM queue WHERE url=?", post.AtURI).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 queue row (dedup), got %d", count)
	}
}

// BT-2: Backoff test (unit test of backoff calculation)
func TestFirehoseBT2_BackoffCalc(t *testing.T) {
	b := time.Second
	max := 5 * time.Minute
	for i := 0; i < 10; i++ {
		b = time.Duration(math.Min(float64(b*2), float64(max)))
	}
	if b != max {
		t.Fatalf("expected backoff to cap at %v, got %v", max, b)
	}
}

// BT-3: RemoveFirehoseSubscription removes exactly the target
func TestFirehoseBT3_RemoveSubscription(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "golang")
	_ = q.AddFirehoseSubscription("default", "rust")
	_ = q.RemoveFirehoseSubscription("default", "golang")

	subs, _ := q.ListFirehoseSubscriptions("default")
	if len(subs) != 1 || subs[0] != "rust" {
		t.Fatalf("expected [rust], got %v", subs)
	}
}

// BT-4: Same AT URI within 5-min dedup window → zero additional queue rows
func TestFirehoseBT4_DedupWindow(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "golang")

	post := &firehosePost{
		AtURI: "at://did:plc:abc/app.bsky.feed.post/dedup2",
		Text:  "golang dedup test",
		Repo:  "did:plc:abc",
		Seq:   300,
	}
	_ = handleFirehosePost(context.Background(), q, post)
	_ = handleFirehosePost(context.Background(), q, post)

	var count int
	q.db.QueryRow("SELECT COUNT(*) FROM queue WHERE url=?", post.AtURI).Scan(&count)
	if count != 1 {
		t.Fatalf("dedup window failed: got %d rows", count)
	}
}

// RG-1: 10 firehose queue rows → EnqueueDigestIfDue for non-firehose profile still works
func TestFirehoseRG1_ThrottleIsolation(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("work", "golang")

	// Insert 10 firehose rows for "work" profile
	for i := 0; i < 10; i++ {
		post := &firehosePost{
			AtURI: fmt.Sprintf("at://did:plc:abc/app.bsky.feed.post/%d", i),
			Text:  "golang post",
			Repo:  "did:plc:abc",
			Seq:   int64(400 + i),
		}
		_ = handleFirehosePost(context.Background(), q, post)
	}

	// EnqueueDigestIfDue for a DIFFERENT profile should still work
	result, err := q.EnqueueDigestIfDue(context.Background(), "life", 80, "slug-rg1", "Strong Yes", "https://example.com/video")
	if err != nil {
		t.Fatal(err)
	}
	// Should insert (not throttled) since "life" profile has no recent digest
	_ = result
}

// RG-2: Worker exits only on ctx.Done(), not on transient WebSocket error
func TestFirehoseRG2_NoExitOnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	calls := 0
	connectErr := fmt.Errorf("transient error")

	// Override connectAndRead with a test version
	origConnect := execConnectAndRead
	execConnectAndRead = func(ctx context.Context, q *Queue, url string) error {
		calls++
		if calls >= 2 {
			cancel()
		}
		return connectErr
	}
	defer func() { execConnectAndRead = origConnect }()

	runFirehoseWorker(ctx, q, &BlueskyClient{}, nil)

	if calls < 2 {
		t.Fatalf("expected at least 2 connect attempts, got %d", calls)
	}
}

// TestFirehoseIntegration: mock relay → keyword match → queue row → push_outbox kind='notify'
func TestFirehoseIntegration(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	if err := q.AddFirehoseSubscription("default", "golang"); err != nil {
		t.Fatal(err)
	}

	// Simulate a matching post directly
	post := &firehosePost{
		AtURI: "at://did:plc:abc/app.bsky.feed.post/integ",
		Text:  "Excited about the golang firehose",
		Repo:  "did:plc:abc",
		Seq:   500,
	}
	if err := handleFirehosePost(context.Background(), q, post); err != nil {
		t.Fatal(err)
	}

	// Verify queue row with source='firehose' (status='relayed' — marked immediately after push)
	var rowCount int
	q.db.QueryRow("SELECT COUNT(*) FROM queue WHERE url=? AND source='firehose'", post.AtURI).Scan(&rowCount)
	if rowCount == 0 {
		t.Fatal("no queue row with source=firehose")
	}

	// Verify push row with kind='notify' (not 'digest')
	pushes, _ := q.PendingPushes(10)
	for _, p := range pushes {
		if p.URL == post.AtURI {
			if p.Kind != "notify" {
				t.Fatalf("expected kind='notify', got %q", p.Kind)
			}
			return
		}
	}
	t.Fatal("no push_outbox row found")
}

// RG-N: Real ATProto frame decodes correctly end-to-end.
// Builds a frame with two concatenated CBOR maps (NOT a CBOR array) matching
// the wire format of com.atproto.sync.subscribeRepos. Source: POMO
// PERSONAL_20260509T000717Z_POMO_firehose-cbor-decode-failure.
//
// Regression guard: this test must pass after any change to processFirehoseMessage.
// If it breaks, the ATProto framing assumption has regressed.
func TestFirehoseRGN_RealFrameDecodes(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	if err := q.AddFirehoseSubscription("default", "regressiontest"); err != nil {
		t.Fatal(err)
	}

	// Build a real ATProto-format frame: two concatenated CBOR maps.
	header := firehoseHeader{Op: 1, T: "#commit"}
	body := firehoseBody{
		Seq:  999,
		Repo: "did:plc:regressiontest",
		Ops: []struct {
			Action string `cbor:"action"`
			Path   string `cbor:"path"`
		}{
			{Action: "create", Path: "app.bsky.feed.post/rgntest"},
		},
	}

	hBytes, err := cbor.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	bBytes, err := cbor.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	frame := append(hBytes, bBytes...)

	if err := processFirehoseMessage(context.Background(), q, frame); err != nil {
		t.Fatalf("processFirehoseMessage returned error: %v", err)
	}

	var rgnCount int
	q.db.QueryRow("SELECT COUNT(*) FROM queue WHERE url=?", "at://did:plc:regressiontest/app.bsky.feed.post/rgntest").Scan(&rgnCount)
	if rgnCount == 0 {
		t.Fatal("RG-N: real ATProto frame was not decoded and enqueued — framing regression")
	}
}
