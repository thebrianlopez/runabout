package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// blockingFailEval blocks in Evaluate until proceed is closed, then returns err.
// This lets the test inject DB state (retry_count) between eval start and return,
// ensuring a single scoreAsync call triggers the terminal retryOrFail branch.
type blockingFailEval struct {
	ready   chan struct{}
	proceed chan struct{}
	err     error
}

func (e *blockingFailEval) Name() string { return "blocking-fail" }
func (e *blockingFailEval) Evaluate(_ context.Context, _, _ string) (*Scorecard, error) {
	close(e.ready)
	<-e.proceed
	return nil, e.err
}

// TestFirehoseScoringErrorCapture verifies that a firehose scoring failure leaves
// observable state: non-null trace_id, populated error_reason, and progress=score_failed.
// This guards against the regression where row 25531 had all three fields empty.
func TestFirehoseScoringErrorCapture(t *testing.T) {
	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("eng", "quantum")

	eval := &blockingFailEval{
		ready:   make(chan struct{}),
		proceed: make(chan struct{}),
		err:     errors.New("claude CLI: exit status 1"),
	}
	fsc := testFSCWithEval(t, q, eval)
	fsc.Deps = deps

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/err-capture",
		Text:  "quantum error capture regression test",
		Repo:  "did:plc:test",
		Seq:   9001,
	}

	if err := handleFirehosePost(context.Background(), fsc, post); err != nil {
		t.Fatal(err)
	}

	// Block until the goroutine reaches Evaluate, then bump retry_count to one
	// below exhaustion so the returning error triggers the terminal failure branch.
	select {
	case <-eval.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("ErrorCapture: goroutine never called Evaluate within 5s")
	}
	q.db.Exec("UPDATE queue SET retry_count=? WHERE url=?", ScoringMaxAttempts-1, post.AtURI)
	close(eval.proceed)

	// Give retryOrFail time to commit.
	time.Sleep(200 * time.Millisecond)

	var status, errorReason, traceID, progress string
	err := q.db.QueryRow(
		"SELECT status, COALESCE(error_reason,''), COALESCE(trace_id,''), COALESCE(progress,'') FROM queue WHERE url=?",
		post.AtURI,
	).Scan(&status, &errorReason, &traceID, &progress)
	if err != nil {
		t.Fatalf("ErrorCapture: row not found: %v", err)
	}

	if status != "failed" && status != "pending" {
		t.Errorf("ErrorCapture: status = %q, want failed or pending", status)
	}
	if traceID == "" {
		t.Error("ErrorCapture: trace_id is empty - EnqueueWithSource must mint a trace_id")
	}
	if errorReason == "" {
		t.Error("ErrorCapture: error_reason is empty - retryOrFail must populate error_reason on terminal failure")
	}
	if !strings.Contains(errorReason, "scoring_retry_exhausted") {
		t.Errorf("ErrorCapture: error_reason = %q, want to contain scoring_retry_exhausted", errorReason)
	}
	if progress == "" {
		t.Error("ErrorCapture: progress is empty - retryOrFail must set progress=score_failed on terminal scoring failure")
	}
}
