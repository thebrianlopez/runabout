//go:build integration

// EPIC-051 M8: end-to-end integration test that spans the CLI → queue →
// push_outbox boundary. This is the missing coverage that would have
// caught the EPIC-050 Push Path Gap before merge.
//
// Run with:
//
//	go test -tags=integration ./cmd/linkari/...
//
// The test invokes the `linkari score` command as a child process via
// `go run` against a throwaway SQLite database and asserts that the
// composed chain (score → archive → digest enqueue) left the expected
// state. This catches the class of silent dual-writer drift where one
// scoring writer skips the push enqueue entirely.
package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPushChainEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "queue.db")

	// Seed the queue with a relayed row so ScoreByURL finds it. This
	// simulates an Android share that landed via /share and is now being
	// triaged by the CLI scorer.
	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	if _, err := q.Enqueue(&ShareRequest{
		Type:    "url",
		URL:     "https://example.com/post-1",
		Profile: "eng",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Promote to relayed so ScoreByURL updates (mimics /share → worker flow).
	pending, _ := q.Pending()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending row, got %d", len(pending))
	}
	if err := q.MarkRelayed(pending[0].ID); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}
	q.Close()

	// Invoke `linkari score` as a child process. `go run` rebuilds the
	// binary on every call — slow (~3s) but it exercises the real cobra
	// wiring, including the command's LINKARI_QUEUE_DB env var resolution
	// and cmd_score.go's archive + EnqueueDigestIfDue path.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/linkari",
		"score",
		"--url", "https://example.com/post-1",
		"--score", "90",
		"--verdict", "Worth reading",
		"--profile", "eng",
		"--slug", "post-1",
		"--tags", "ai,ml",
	)
	cmd.Env = append(cmd.Environ(),
		"LINKARI_QUEUE_DB="+dbPath,
	)
	// Run from repo root so `./cmd/linkari` resolves.
	cmd.Dir = repoRoot(t)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("linkari score failed: %v\noutput:\n%s", err, string(out))
	}
	if !strings.Contains(string(out), `"status": "archived"`) {
		t.Errorf("expected archived status in CLI output:\n%s", string(out))
	}

	// Reopen the queue and assert the composed-chain state.
	q2, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("reopen queue: %v", err)
	}
	defer q2.Close()

	// 1. Queue row should be archived.
	archived, err := q2.ListArchived("eng", 10)
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("expected 1 archived row, got %d", len(archived))
	}
	if archived[0].URL != "https://example.com/post-1" {
		t.Errorf("archived URL mismatch: %q", archived[0].URL)
	}

	// 2. push_outbox should contain exactly one pending digest row.
	// This is the regression assertion — EPIC-050 shipped without this
	// row being written, and no test caught it.
	pushes, err := q2.PendingPushes(10)
	if err != nil {
		t.Fatalf("PendingPushes: %v", err)
	}
	if len(pushes) != 1 {
		t.Fatalf("expected exactly 1 pending push row, got %d: %+v", len(pushes), pushes)
	}
	p := pushes[0]
	if p.Kind != "digest" {
		t.Errorf("kind=%q want digest", p.Kind)
	}
	if p.Slug != "post-1" {
		t.Errorf("slug=%q want post-1", p.Slug)
	}
	if p.Score != 90 {
		t.Errorf("score=%d want 90", p.Score)
	}
}

// EPIC-057 M4: auto-scored ginit rows must NOT produce push_outbox entries.
// This preserves the dual-writer invariant (EPIC-051) — ginit rows bypass
// the scoring pipeline entirely.
func TestAutoScoreDoesNotWritePushOutbox(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	req := &ShareRequest{
		Action:  "ginit_auto",
		Profile: "eng",
		Type:    "text",
		Text:    "PROJ-42",
	}
	_, err = q.EnqueueScored(req, "workspace_bootstrapped")
	if err != nil {
		t.Fatal(err)
	}

	pushes, err := q.PendingPushes(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pushes) != 0 {
		t.Errorf("auto-scored row should not produce push_outbox entries, got %d: %+v", len(pushes), pushes)
	}
}

// TestTranscriptKindDoesNotConsumeDigestThrottle verifies that a push_outbox
// row with kind='transcript' does NOT trigger the throttle window checked by
// EnqueueDigestIfDue. Before the M1 fix, EnqueueTranscriptPush inserted
// kind='digest', causing subsequent scoreYouTubeAsync pushes to be silently
// throttled within the same window.
func TestTranscriptKindDoesNotConsumeDigestThrottle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	// Simulate EnqueueTranscriptPush (kind='transcript').
	if err := q.EnqueueTranscriptPush("eng", "yt-transcript-1", "transcribed", "https://youtube.com/watch?v=abc"); err != nil {
		t.Fatalf("EnqueueTranscriptPush: %v", err)
	}

	// Configure push so score floor and throttle are reachable.
	q.SetPushConfig(&PushConfig{
		NotifyMinScore: 0,
		DigestThrottleDefault: 1 * time.Hour,
	})

	// EnqueueDigestIfDue within the throttle window should NOT be blocked —
	// the transcript row must not count against the digest throttle.
	result, err := q.EnqueueDigestIfDue(context.Background(), "eng", 80, "yt-score-1", "worth it", "https://youtube.com/watch?v=abc")
	if err != nil {
		t.Fatalf("EnqueueDigestIfDue: %v", err)
	}
	if !result.Enqueued {
		t.Errorf("expected push to be enqueued (transcript row must not block digest throttle), got reason=%q seconds_until=%d",
			result.Reason, result.SecondsUntilAllowed)
	}
}

// TestScoreYouTubePushAfterTranscript verifies that after EnqueueTranscriptPush
// writes a kind='transcript' row, a subsequent EnqueueDigestIfDue call writes a
// kind='digest' row with the correct content_type set by SetPushContentType.
func TestScoreYouTubePushAfterTranscript(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	q.SetPushConfig(&PushConfig{
		NotifyMinScore: 0,
		DigestThrottleDefault: 1 * time.Hour,
	})

	// Step 1: transcript push (kind='transcript').
	if err := q.EnqueueTranscriptPush("eng", "yt-abc", "transcribed", "https://youtube.com/watch?v=abc"); err != nil {
		t.Fatalf("EnqueueTranscriptPush: %v", err)
	}

	// Step 2: score push (kind='digest') — must not be throttled by transcript row.
	result, err := q.EnqueueDigestIfDue(context.Background(), "eng", 85, "yt-abc", "worth it", "https://youtube.com/watch?v=abc")
	if err != nil {
		t.Fatalf("EnqueueDigestIfDue: %v", err)
	}
	if !result.Enqueued {
		t.Fatalf("digest push was not enqueued; reason=%q", result.Reason)
	}
	if result.ID == 0 {
		t.Fatal("expected non-zero row ID from EnqueueDigestIfDue")
	}

	// Step 3: tag content_type='youtube' (mirrors scoreYouTubeAsync).
	if err := q.SetPushContentType(result.ID, "youtube"); err != nil {
		t.Fatalf("SetPushContentType: %v", err)
	}

	// Assert: pending pushes contain exactly one digest row with content_type='youtube'.
	pushes, err := q.PendingPushes(10)
	if err != nil {
		t.Fatalf("PendingPushes: %v", err)
	}
	var digestRows []PushItem
	for _, p := range pushes {
		if p.Kind == "digest" {
			digestRows = append(digestRows, p)
		}
	}
	if len(digestRows) != 1 {
		t.Fatalf("expected 1 digest row, got %d (all pushes: %+v)", len(digestRows), pushes)
	}
	if digestRows[0].ContentType != "youtube" {
		t.Errorf("content_type=%q want youtube", digestRows[0].ContentType)
	}
}

// repoRoot walks up from the cmd/linkari test cwd until it finds the
// runabout go.mod, returning its directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	for i := 0; i < 5; i++ {
		if strings.HasSuffix(dir, "/runabout") {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("could not locate runabout root from %s", dir)
	return ""
}
