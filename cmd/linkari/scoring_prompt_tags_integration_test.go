package main

// EPIC-127 F5 CT-5 and CT-6: integration tests for scoring prompt tag injection.
//
// CT-5: scoreAsync with non-empty UserTags injects "User-Applied Tags" section into
//
//	the system prompt received by the evaluator.
//
// CT-6: scoreAsync with nil UserTags does not add a "User-Applied Tags" section.
//
// Both tests use the existing captureEval (pdf_profile_routing_test.go) wrapped in
// onceDoneEval for synchronization, and a mock Jina server for URL content.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// runScoreAsyncCapturePrompt calls scoreAsync and waits for the evaluator to be
// invoked. Returns the system prompt that was passed to eval.Evaluate.
func runScoreAsyncCapturePrompt(t *testing.T, req *ShareRequest, q *Queue, deps *scoringDeps) string {
	t.Helper()
	inner := &captureEval{}
	done := make(chan struct{})
	wrapped := &onceDoneEval{inner: inner, done: done}
	go scoreAsync(req, q, wrapped, nil, nil, nil, deps)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Log("runScoreAsyncCapturePrompt: timed out (eval never called)")
	}
	time.Sleep(50 * time.Millisecond)
	return inner.prompt
}

// CT-5: score request with UserTags -> evaluator prompt contains "User-Applied Tags" section.
func TestF5_CT5_ScoreAsync_UserTags_InjectedIntoPrompt(t *testing.T) {
	isolateEventsDir(t)

	// Stub Jina so the URL fetch succeeds without network.
	srv := jinaBodyServer(t, http.StatusOK, "This is a test engineering article about Go concurrency.")
	deps := installJinaServer(t, srv)

	// Stub content classify to avoid subprocess.
	deps.Backend = &funcScoringBackend{complete: func(_ context.Context, _, _ string) (string, error) {
		return "eng", nil
	}}

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	const testURL = "https://example.com/ct5-tag-injection"
	req := &ShareRequest{
		Type:     "url",
		URL:      testURL,
		Profile:  "eng",
		UserTags: []string{"urgent", "reference"},
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}
	req.QueueRowID = id

	prompt := runScoreAsyncCapturePrompt(t, req, q, deps)

	if !strings.Contains(prompt, "User-Applied Tags: urgent, reference") {
		t.Errorf("CT-5 FAIL: prompt does not contain tag section; prompt=%q", prompt)
	}
}

// CT-6: score request with UserTags = nil -> prompt unchanged (no tag section).
func TestF5_CT6_ScoreAsync_NilUserTags_PromptUnchanged(t *testing.T) {
	isolateEventsDir(t)

	srv := jinaBodyServer(t, http.StatusOK, "This is a test engineering article about Go concurrency.")
	deps := installJinaServer(t, srv)

	deps.Backend = &funcScoringBackend{complete: func(_ context.Context, _, _ string) (string, error) {
		return "eng", nil
	}}

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	const testURL = "https://example.com/ct6-no-tags"
	req := &ShareRequest{
		Type:     "url",
		URL:      testURL,
		Profile:  "eng",
		UserTags: nil,
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}
	req.QueueRowID = id

	prompt := runScoreAsyncCapturePrompt(t, req, q, deps)

	if strings.Contains(prompt, "User-Applied Tags") {
		t.Errorf("CT-6 FAIL: prompt should not contain tag section when UserTags is nil; prompt=%q", prompt)
	}
}
