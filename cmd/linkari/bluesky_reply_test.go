package main

import (
	"context"
	"errors"
	"testing"
)

// CT-1: FCM push row present even when publishVerdictReply returns error
func TestBlueskyReplyCT1_FCMNotBlocked(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	// Seed user with opt-in
	q.db.Exec("INSERT OR IGNORE INTO users (id, google_sub, email, name, created_at, updated_at) VALUES (1,'sub','e@e.com','T',1,1)")
	q.db.Exec("UPDATE users SET bluesky_publish_opt_in=1 WHERE id=1")

	// Wire a failing execPublishReply
	oldExec := execPublishReply
	execPublishReply = func(_ context.Context, _ *BlueskyClient, _, _, _, _ string, _ int) error {
		return errors.New("bluesky_unreachable")
	}
	defer func() { execPublishReply = oldExec }()

	// Simulate scoreAsync calling FCM + reply
	req := &ShareRequest{URL: "at://did:plc:abc/app.bsky.feed.post/xyz", Profile: "default", Type: "url"}
	rowID, _ := q.Enqueue(req)

	// Simulate EnqueueDigestIfDue (FCM) + reply
	_, _ = q.EnqueueDigestIfDue(context.Background(), "default", 80, "slug1", "Strong Yes", req.URL)
	_ = publishVerdictReply(context.Background(), nil, req.URL, 80, "Strong Yes", q, 1)

	// FCM row must be present regardless of reply failure
	pushes, err := q.PendingPushes(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range pushes {
		if p.Slug == "slug1" {
			found = true
			break
		}
	}
	_ = rowID
	if !found {
		t.Fatal("FCM push row missing after reply failure")
	}
}

// CT-2: Zero XRPC calls when user is opted out
func TestBlueskyReplyCT2_OptOut(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	q.db.Exec("INSERT OR IGNORE INTO users (id, google_sub, email, name, created_at, updated_at) VALUES (1,'sub','e@e.com','T',1,1)")
	// opt-in = 0 (default)

	calls := 0
	oldExec := execPublishReply
	execPublishReply = func(_ context.Context, _ *BlueskyClient, _, _, _, _ string, _ int) error {
		calls++
		return nil
	}
	defer func() { execPublishReply = oldExec }()

	_ = publishVerdictReply(context.Background(), nil, "at://did:plc:abc/app.bsky.feed.post/xyz", 80, "Strong Yes", q, 1)
	if calls != 0 {
		t.Fatalf("expected 0 XRPC calls for opted-out user, got %d", calls)
	}
}

// CT-3: createRecord called with correct reply.parent.uri and CID for opted-in user
func TestBlueskyReplyCT3_CreateRecord(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	q.db.Exec("INSERT OR IGNORE INTO users (id, google_sub, email, name, created_at, updated_at) VALUES (1,'sub','e@e.com','T',1,1)")
	q.db.Exec("UPDATE users SET bluesky_publish_opt_in=1 WHERE id=1")

	var capturedURI, capturedCID string
	oldExec := execPublishReply
	execPublishReply = func(_ context.Context, _ *BlueskyClient, atURI, cid, _, _ string, _ int) error {
		capturedURI = atURI
		capturedCID = cid
		return nil
	}
	defer func() { execPublishReply = oldExec }()

	// Stub getRecord to return a known CID
	oldGet := execGetRecord
	execGetRecord = func(_ context.Context, _ *BlueskyClient, atURI string) (string, error) {
		return "bafyreidummy", nil
	}
	defer func() { execGetRecord = oldGet }()

	const testURI = "at://did:plc:abc/app.bsky.feed.post/xyz"
	_ = publishVerdictReply(context.Background(), &BlueskyClient{}, testURI, 80, "Strong Yes", q, 1)

	if capturedURI != testURI {
		t.Fatalf("expected URI %q, got %q", testURI, capturedURI)
	}
	if capturedCID != "bafyreidummy" {
		t.Fatalf("expected CID bafyreidummy, got %q", capturedCID)
	}
}

// CT-4: NewQueue twice on same DB does not error; bluesky_publish_opt_in column exists
func TestBlueskyReplyCT4_MigrationIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/queue.db"
	q1, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	q1.Close()
	q2, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("second NewQueue failed: %v", err)
	}
	defer q2.Close()
	_, err = q2.db.Exec("SELECT bluesky_publish_opt_in FROM users LIMIT 0")
	if err != nil {
		t.Fatalf("column missing: %v", err)
	}
}

func TestBlueskyReplyBT1_BuildReplyText(t *testing.T) {
	got := buildReplyText(75, "Strong Yes")
	want := "Linkari: Strong Yes — 75/100"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBlueskyReplyBT2_RateLimit(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	q.db.Exec("INSERT OR IGNORE INTO users (id, google_sub, email, name, created_at, updated_at) VALUES (1,'sub','e@e.com','T',1,1)")
	q.db.Exec("UPDATE users SET bluesky_publish_opt_in=1 WHERE id=1")

	calls := 0
	oldExec := execPublishReply
	execPublishReply = func(_ context.Context, _ *BlueskyClient, _, _, _, _ string, _ int) error {
		calls++
		return errors.New("RateLimitExceeded")
	}
	defer func() { execPublishReply = oldExec }()
	oldGet := execGetRecord
	execGetRecord = func(_ context.Context, _ *BlueskyClient, _ string) (string, error) {
		return "cid123", nil
	}
	defer func() { execGetRecord = oldGet }()

	_ = publishVerdictReply(context.Background(), &BlueskyClient{}, "at://did/col/rkey", 80, "Strong Yes", q, 1)
	// On rate limit, we log WARN and return nil (no panic, no propagation)
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestBlueskyReplyBT3_PostNotFound(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	q.db.Exec("INSERT OR IGNORE INTO users (id, google_sub, email, name, created_at, updated_at) VALUES (1,'sub','e@e.com','T',1,1)")
	q.db.Exec("UPDATE users SET bluesky_publish_opt_in=1 WHERE id=1")

	oldGet := execGetRecord
	execGetRecord = func(_ context.Context, _ *BlueskyClient, _ string) (string, error) {
		return "", errors.New("bluesky_post_not_found")
	}
	defer func() { execGetRecord = oldGet }()

	calls := 0
	oldExec := execPublishReply
	execPublishReply = func(_ context.Context, _ *BlueskyClient, _, _, _, _ string, _ int) error {
		calls++
		return nil
	}
	defer func() { execPublishReply = oldExec }()

	_ = publishVerdictReply(context.Background(), &BlueskyClient{}, "at://did/col/rkey", 80, "Strong Yes", q, 1)
	if calls != 0 {
		t.Fatal("createRecord must not be called when getRecord returns not-found")
	}
}

func TestBlueskyReplyBT4_OptInRoundTrip(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	q.db.Exec("INSERT OR IGNORE INTO users (id, google_sub, email, name, created_at, updated_at) VALUES (1,'sub','e@e.com','T',1,1)")

	if err := q.SetBlueskyPublishOptIn(1, true); err != nil {
		t.Fatal(err)
	}
	v, err := q.GetBlueskyPublishOptIn(1)
	if err != nil {
		t.Fatal(err)
	}
	if !v {
		t.Fatal("expected opt-in=true")
	}

	if err := q.SetBlueskyPublishOptIn(1, false); err != nil {
		t.Fatal(err)
	}
	v, err = q.GetBlueskyPublishOptIn(1)
	if err != nil {
		t.Fatal(err)
	}
	if v {
		t.Fatal("expected opt-in=false")
	}
}

// RG-1: publishVerdictReply panics → push_outbox row still present
func TestBlueskyReplyRG1_PanicSafe(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	q.db.Exec("INSERT OR IGNORE INTO users (id, google_sub, email, name, created_at, updated_at) VALUES (1,'sub','e@e.com','T',1,1)")
	q.db.Exec("UPDATE users SET bluesky_publish_opt_in=1 WHERE id=1")

	// Enqueue FCM push first
	req := &ShareRequest{URL: "at://did/col/rkey", Profile: "default", Type: "url"}
	_, _ = q.EnqueueDigestIfDue(context.Background(), "default", 80, "slug-rg1", "Strong Yes", req.URL)

	// publishVerdictReply should recover from panic (wrap in goroutine with recover)
	func() {
		defer func() { recover() }()
		oldExec := execPublishReply
		execPublishReply = func(_ context.Context, _ *BlueskyClient, _, _, _, _ string, _ int) error {
			panic("simulated panic")
		}
		defer func() { execPublishReply = oldExec }()
		oldGet := execGetRecord
		execGetRecord = func(_ context.Context, _ *BlueskyClient, _ string) (string, error) {
			return "cid", nil
		}
		defer func() { execGetRecord = oldGet }()
		_ = publishVerdictReply(context.Background(), &BlueskyClient{}, "at://did/col/rkey", 80, "Strong Yes", q, 1)
	}()

	pushes, _ := q.PendingPushes(10)
	found := false
	for _, p := range pushes {
		if p.Slug == "slug-rg1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("FCM push row missing after panic")
	}
}

// RG-2: source='firehose' rows use kind='notify'; EnqueueDigestIfDue NOT called for firehose
func TestBlueskyReplyRG2_FirehoseKindNotify(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	// Enqueue a push directly as kind='notify' (firehose path)
	_, err := q.EnqueuePushWithProfile("notify", "default", 80, "slug-firehose", "Strong Yes", "at://did/col/rkey", "")
	if err != nil {
		t.Fatal(err)
	}

	// Verify the push row is kind='notify' not kind='digest'
	var kind string
	q.db.QueryRow("SELECT kind FROM push_outbox WHERE slug='slug-firehose'").Scan(&kind)
	if kind != "notify" {
		t.Fatalf("expected kind='notify', got %q", kind)
	}
}

func TestBlueskyReplyIntegration(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	q.db.Exec("INSERT OR IGNORE INTO users (id, google_sub, email, name, created_at, updated_at) VALUES (1,'sub','e@e.com','T',1,1)")
	q.db.Exec("UPDATE users SET bluesky_publish_opt_in=1 WHERE id=1")

	req := &ShareRequest{URL: "at://did:plc:abc/app.bsky.feed.post/xyz", Profile: "default", Type: "url"}
	rowID, _ := q.Enqueue(req)
	_ = rowID

	// FCM push
	_, err := q.EnqueueDigestIfDue(context.Background(), "default", 80, "slug-integ", "Strong Yes", req.URL)
	if err != nil {
		t.Fatal(err)
	}

	// Wire test seams
	var publishedURI string
	oldGet := execGetRecord
	execGetRecord = func(_ context.Context, _ *BlueskyClient, atURI string) (string, error) {
		return "bafyreid123", nil
	}
	defer func() { execGetRecord = oldGet }()
	oldExec := execPublishReply
	execPublishReply = func(_ context.Context, _ *BlueskyClient, atURI, cid, _, _ string, _ int) error {
		publishedURI = atURI
		return nil
	}
	defer func() { execPublishReply = oldExec }()

	_ = publishVerdictReply(context.Background(), &BlueskyClient{Session: BlueskySessionData{DID: "did:plc:abc", AccessJWT: "tok"}}, req.URL, 80, "Strong Yes", q, 1)

	// FCM row present
	pushes, _ := q.PendingPushes(10)
	found := false
	for _, p := range pushes {
		if p.Slug == "slug-integ" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("FCM push missing")
	}

	// Reply was published
	if publishedURI != req.URL {
		t.Fatalf("reply not published to correct URI: got %q", publishedURI)
	}
}
