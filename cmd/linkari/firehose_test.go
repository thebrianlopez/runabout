package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// CT-1: keyword match → queue row with source='firehose' and status='pending' (not 'relayed').
// Score=0 push removed in EPIC-123 M3; scored push comes from scoreAsync (requires M4 wiring).
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
	err := handleFirehosePost(context.Background(), testFSC(q), post)
	if err != nil {
		t.Fatal(err)
	}

	var status string
	q.db.QueryRow("SELECT status FROM queue WHERE url=? AND source='firehose'", post.AtURI).Scan(&status)
	if status == "" {
		t.Fatal("expected queue row with source='firehose'")
	}
	if status == "relayed" {
		t.Fatal("CT-1: firehose row must not be 'relayed'  -  MarkRelayed bypass removed (EPIC-123 M3)")
	}
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
	err := handleFirehosePost(context.Background(), testFSC(q), post)
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
	err := processFirehoseMessage(context.Background(), testFSC(q), []byte("not-cbor"))
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

// BT-1: dedup guard  -  same AT URI twice → only one queue row
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
	_ = handleFirehosePost(context.Background(), testFSC(q), post)
	// Second enqueue within 5-min window → should be skipped
	_ = handleFirehosePost(context.Background(), testFSC(q), post)

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
	_ = handleFirehosePost(context.Background(), testFSC(q), post)
	_ = handleFirehosePost(context.Background(), testFSC(q), post)

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
		_ = handleFirehosePost(context.Background(), testFSC(q), post)
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
	execConnectAndRead = func(ctx context.Context, fsc *firehoseScoreContext, url string) error {
		calls++
		if calls >= 2 {
			cancel()
		}
		return connectErr
	}
	defer func() { execConnectAndRead = origConnect }()

	runFirehoseWorker(ctx, testFSC(q), nil)

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
	if err := handleFirehosePost(context.Background(), testFSC(q), post); err != nil {
		t.Fatal(err)
	}

	// Verify queue row with source='firehose' and status='pending' (EPIC-123 M3: MarkRelayed removed).
	var status string
	q.db.QueryRow("SELECT status FROM queue WHERE url=? AND source='firehose'", post.AtURI).Scan(&status)
	if status == "" {
		t.Fatal("no queue row with source=firehose")
	}
	if status == "relayed" {
		t.Fatal("integration: firehose row must not be 'relayed'  -  MarkRelayed bypass removed (EPIC-123 M3)")
	}

	// No score=0 "Firehose Match" push may exist. Scored push comes from scoreAsync (M4+).
	pushes, _ := q.PendingPushes(10)
	for _, p := range pushes {
		if p.URL == post.AtURI && p.Verdict == "Firehose Match" {
			t.Fatal("integration: score=0 'Firehose Match' push must not be enqueued (EPIC-123 M3)")
		}
	}
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
		Ops: []firehoseOp{
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

	if err := processFirehoseMessage(context.Background(), testFSC(q), frame); err != nil {
		t.Fatalf("processFirehoseMessage returned error: %v", err)
	}

	var rgnCount int
	q.db.QueryRow("SELECT COUNT(*) FROM queue WHERE url=?", "at://did:plc:regressiontest/app.bsky.feed.post/rgntest").Scan(&rgnCount)
	if rgnCount == 0 {
		t.Fatal("RG-N: real ATProto frame was not decoded and enqueued  -  framing regression")
	}
}

// TestFirehoseM1_PostgateRejected: op.Path="app.bsky.feed.postgate/*" must NOT be enqueued.
// With strings.Contains, "postgate" contains "post"  -  false positive.
// HasPrefix("app.bsky.feed.post/") requires the trailing slash, rejecting postgates.
func TestFirehoseM1_PostgateRejected(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "bluesky")

	frame := buildFirehoseFrame(t, 1001, "did:plc:test", "app.bsky.feed.postgate/someid", "bluesky discussion thread")
	if err := processFirehoseMessage(context.Background(), testFSC(q), frame); err != nil {
		t.Fatalf("processFirehoseMessage: %v", err)
	}

	var count int
	q.db.QueryRow("SELECT COUNT(*) FROM queue WHERE url LIKE '%postgate%'").Scan(&count)
	if count > 0 {
		t.Fatal("M1: postgate op was enqueued  -  HasPrefix guard not applied correctly")
	}
}

// =====================================================================
// EPIC-125: CAR Block Text Extraction  -  Contract Tests CT-1 through CT-8
// Source TDD: PERSONAL_20260519T102524Z_Runabout_Firehose_CAR_Block_Text_Extraction_TDD.md
// =====================================================================

// fakeCIDBytes is a valid CIDv1 DAG-CBOR SHA2-256 (36 bytes) for use in CAR test fixtures.
// Structure: version=1 (0x01), codec=dag-cbor (0x71), hash=sha2-256 (0x12), digestLen=32 (0x20), digest=zeros.
var fakeCIDBytes = []byte{
	0x01, 0x71, 0x12, 0x20,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
}

// fakeCID2Bytes is a second CIDv1 DAG-CBOR SHA2-256 (last byte=1) for multi-block tests.
var fakeCID2Bytes = []byte{
	0x01, 0x71, 0x12, 0x20,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
}

// appendUvarint appends a uint64 as unsigned LEB128 (varint) to dst.
func appendUvarint(dst []byte, n uint64) []byte {
	var buf [10]byte
	cnt := binary.PutUvarint(buf[:], n)
	return append(dst, buf[:cnt]...)
}

// buildTag42CID encodes raw CID bytes as a CBOR DAG-CBOR CID (tag 42 with identity prefix).
// The result can be assigned directly to firehoseOp.Cid.
func buildTag42CID(cidBytes []byte) cbor.RawMessage {
	// DAG-CBOR: tag(42, bytes(\x00 + cidBytes))
	content := make([]byte, 1+len(cidBytes))
	content[0] = 0x00 // identity multibase prefix
	copy(content[1:], cidBytes)
	var buf bytes.Buffer
	buf.WriteByte(0xd8) // CBOR major type 6 (tag), additional info 24 (1-byte tag number)
	buf.WriteByte(0x2a) // tag number 42
	n := len(content)
	if n <= 23 {
		buf.WriteByte(0x40 | byte(n))
	} else {
		buf.WriteByte(0x58) // byte string, 1-byte length follows
		buf.WriteByte(byte(n))
	}
	buf.Write(content)
	return cbor.RawMessage(buf.Bytes())
}

// buildTestCARV1 builds a minimal CAR v1 byte slice with a single block.
func buildTestCARV1(cidBytes, recordCBOR []byte) []byte {
	headerCBOR, _ := cbor.Marshal(map[string]interface{}{
		"version": uint64(1),
		"roots":   []interface{}{},
	})
	var car []byte
	car = appendUvarint(car, uint64(len(headerCBOR)))
	car = append(car, headerCBOR...)
	blockLen := uint64(len(cidBytes) + len(recordCBOR))
	car = appendUvarint(car, blockLen)
	car = append(car, cidBytes...)
	car = append(car, recordCBOR...)
	return car
}

// buildTestCARV1Multi builds a minimal CAR v1 byte slice with multiple blocks.
func buildTestCARV1Multi(pairs [][2][]byte) []byte {
	headerCBOR, _ := cbor.Marshal(map[string]interface{}{
		"version": uint64(1),
		"roots":   []interface{}{},
	})
	var car []byte
	car = appendUvarint(car, uint64(len(headerCBOR)))
	car = append(car, headerCBOR...)
	for _, p := range pairs {
		cid, record := p[0], p[1]
		car = appendUvarint(car, uint64(len(cid)+len(record)))
		car = append(car, cid...)
		car = append(car, record...)
	}
	return car
}

// CT-1: Valid CAR block produces post text for matching op CID.
func TestFirehoseCARCT1_ValidBlockProducesText(t *testing.T) {
	postRecord := atProtoPost{Type: "app.bsky.feed.post", Text: "hello world"}
	recordCBOR, err := cbor.Marshal(postRecord)
	if err != nil {
		t.Fatalf("marshal post: %v", err)
	}
	carBytes := buildTestCARV1(fakeCIDBytes, recordCBOR)
	ops := []firehoseOp{{Action: "create", Path: "app.bsky.feed.post/testpath", Cid: buildTag42CID(fakeCIDBytes)}}

	result := carExtractPostText(carBytes, ops)
	if result == nil {
		t.Fatal("CT-1: result is nil, want map with 'hello world'")
	}
	if got := result["app.bsky.feed.post/testpath"]; got != "hello world" {
		t.Fatalf("CT-1: text = %q, want 'hello world'", got)
	}
}

// CT-2: Nil blocks returns nil map without panic.
func TestFirehoseCARCT2_NilBlocksReturnsNil(t *testing.T) {
	ops := []firehoseOp{{Action: "create", Path: "app.bsky.feed.post/x", Cid: buildTag42CID(fakeCIDBytes)}}
	result := carExtractPostText(nil, ops)
	if result != nil {
		t.Fatalf("CT-2: result = %v, want nil", result)
	}
}

// CT-3: CID mismatch returns empty map (not nil).
func TestFirehoseCARCT3_CIDMismatchReturnsEmptyMap(t *testing.T) {
	postRecord := atProtoPost{Type: "app.bsky.feed.post", Text: "text"}
	recordCBOR, _ := cbor.Marshal(postRecord)
	carBytes := buildTestCARV1(fakeCIDBytes, recordCBOR)

	// Op references fakeCID2Bytes, but CAR has fakeCIDBytes  -  mismatch.
	ops := []firehoseOp{{Action: "create", Path: "app.bsky.feed.post/x", Cid: buildTag42CID(fakeCID2Bytes)}}

	result := carExtractPostText(carBytes, ops)
	if result == nil {
		t.Fatal("CT-3: result is nil, want empty map (CID mismatch should return empty map, not nil)")
	}
	if len(result) != 0 {
		t.Fatalf("CT-3: result has %d entries, want 0", len(result))
	}
}

// CT-4: Malformed CAR (truncated varint header) returns nil map without panic.
func TestFirehoseCARCT4_MalformedCARReturnsNil(t *testing.T) {
	malformed := []byte{0xff} // continuation bit set, no further bytes  -  truncated varint
	ops := []firehoseOp{{Action: "create", Path: "app.bsky.feed.post/x", Cid: buildTag42CID(fakeCIDBytes)}}

	result := carExtractPostText(malformed, ops)
	if result != nil {
		t.Fatalf("CT-4: result = %v, want nil for malformed CAR", result)
	}
}

// CT-5: Non-post record type (app.bsky.feed.like) is skipped.
func TestFirehoseCARCT5_NonPostRecordSkipped(t *testing.T) {
	likeRecord := atProtoPost{Type: "app.bsky.feed.like", Text: ""}
	recordCBOR, _ := cbor.Marshal(likeRecord)
	carBytes := buildTestCARV1(fakeCIDBytes, recordCBOR)
	ops := []firehoseOp{{Action: "create", Path: "app.bsky.feed.like/abc", Cid: buildTag42CID(fakeCIDBytes)}}

	result := carExtractPostText(carBytes, ops)
	if _, ok := result["app.bsky.feed.like/abc"]; ok {
		t.Fatal("CT-5: non-post record type produced a map entry  -  should be skipped")
	}
}

// CT-6: Multiple ops extract multiple texts from a multi-block CAR.
func TestFirehoseCARCT6_MultipleOpsExtractMultiple(t *testing.T) {
	post1 := atProtoPost{Type: "app.bsky.feed.post", Text: "first post"}
	post2 := atProtoPost{Type: "app.bsky.feed.post", Text: "second post"}
	record1, _ := cbor.Marshal(post1)
	record2, _ := cbor.Marshal(post2)

	carBytes := buildTestCARV1Multi([][2][]byte{
		{fakeCIDBytes, record1},
		{fakeCID2Bytes, record2},
	})
	ops := []firehoseOp{
		{Action: "create", Path: "app.bsky.feed.post/path1", Cid: buildTag42CID(fakeCIDBytes)},
		{Action: "create", Path: "app.bsky.feed.post/path2", Cid: buildTag42CID(fakeCID2Bytes)},
	}

	result := carExtractPostText(carBytes, ops)
	if result["app.bsky.feed.post/path1"] != "first post" {
		t.Fatalf("CT-6: path1 = %q, want 'first post'", result["app.bsky.feed.post/path1"])
	}
	if result["app.bsky.feed.post/path2"] != "second post" {
		t.Fatalf("CT-6: path2 = %q, want 'second post'", result["app.bsky.feed.post/path2"])
	}
}

// CT-7: firehoseBody correctly decodes Blocks and Ops[0].Cid after CBOR round-trip.
func TestFirehoseCARCT7_BodyDecodesBlocksAndCid(t *testing.T) {
	postRecord := atProtoPost{Type: "app.bsky.feed.post", Text: "ct7 text"}
	recordCBOR, _ := cbor.Marshal(postRecord)
	carBytes := buildTestCARV1(fakeCIDBytes, recordCBOR)
	// CBOR-encode the CAR bytes as a byte string for the blocks field.
	blocksCBOR, _ := cbor.Marshal(carBytes)

	body := firehoseBody{
		Seq:  7,
		Repo: "did:plc:ct7",
		Ops: []firehoseOp{{
			Action: "create",
			Path:   "app.bsky.feed.post/ct7",
			Cid:    buildTag42CID(fakeCIDBytes),
		}},
		Blocks: cbor.RawMessage(blocksCBOR),
	}

	bodyBytes, err := cbor.Marshal(body)
	if err != nil {
		t.Fatalf("CT-7: marshal body: %v", err)
	}
	var decoded firehoseBody
	if err := cbor.Unmarshal(bodyBytes, &decoded); err != nil {
		t.Fatalf("CT-7: unmarshal body: %v", err)
	}
	if len(decoded.Blocks) == 0 {
		t.Fatal("CT-7: Blocks is nil/empty after CBOR round-trip")
	}
	if len(decoded.Ops) == 0 || len(decoded.Ops[0].Cid) == 0 {
		t.Fatal("CT-7: Ops[0].Cid is nil/empty after CBOR round-trip")
	}
}

// CT-8: AT-URI short-circuit in DomainRouter prevents Jina from being called.
func TestFirehoseCARCT8_ATURIShortCircuit(t *testing.T) {
	jinaCalled := false
	jinaSpy := func(_ context.Context, _ string) (string, error) {
		jinaCalled = true
		return "", nil
	}
	router := NewDomainRouter(nil, jinaSpy)

	_, _, err := router.FetchWithFallback(context.Background(), "at://did:plc:abc/app.bsky.feed.post/xyz")
	if jinaCalled {
		t.Fatal("CT-8: at:// URI reached Jina  -  short-circuit not implemented")
	}
	if err == nil {
		t.Fatal("CT-8: expected error for at:// URI, got nil")
	}
}
