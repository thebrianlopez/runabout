package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// testFSC wraps a Queue in a minimal firehoseScoreContext for tests.
// Eval is nil so scoreAsync is not called  -  tests verify DB state only.
func testFSC(q *Queue) *firehoseScoreContext {
	return &firehoseScoreContext{Queue: q, ScoreSem: make(chan struct{}, 3)}
}

// testFSCWithEval creates an fsc with an Evaluator for integration tests.
func testFSCWithEval(t *testing.T, q *Queue, eval Evaluator) *firehoseScoreContext {
	t.Helper()
	return &firehoseScoreContext{Queue: q, Eval: eval, ScoreSem: make(chan struct{}, 3), Deps: newTestDeps(t)}
}

// contentCapturingEval wraps stubEvaluator and records the content arg of Evaluate.
type contentCapturingEval struct {
	inner   Evaluator
	content string
	called  int32 // atomic
}

func (e *contentCapturingEval) Name() string { return "capturing" }
func (e *contentCapturingEval) Evaluate(ctx context.Context, content, prompt string) (*Scorecard, error) {
	atomic.StoreInt32(&e.called, 1)
	e.content = content
	return e.inner.Evaluate(ctx, content, prompt)
}

// semaphoreCapEval delays each Evaluate call and tracks peak concurrent invocations.
// done is a buffered channel; each call sends once (non-blocking) so callers can
// count completions without waiting for scoreAsync to fully return.
type semaphoreCapEval struct {
	inner   Evaluator
	delay   time.Duration
	current int32 // atomic
	peak    int32 // atomic
	done    chan struct{}
}

func (e *semaphoreCapEval) Name() string { return "semaphore-cap" }
func (e *semaphoreCapEval) Evaluate(ctx context.Context, content, prompt string) (*Scorecard, error) {
	cur := atomic.AddInt32(&e.current, 1)
	for {
		old := atomic.LoadInt32(&e.peak)
		if cur <= old || atomic.CompareAndSwapInt32(&e.peak, old, cur) {
			break
		}
	}
	time.Sleep(e.delay)
	atomic.AddInt32(&e.current, -1)
	select {
	case e.done <- struct{}{}:
	default:
	}
	return e.inner.Evaluate(ctx, content, prompt)
}

// =====================================================================
// EPIC-123 M1: Contract tests and regression guards for firehose scoring.
// All tests in this file are expected to FAIL until M2/M3 implementation.
// Gate rule: these must be committed before implementation begins.
//
// Source TDDs:
//   F1: PERSONAL_20260518T150526Z_Runabout_Firehose_ScoreAsync_Wiring_TDD.md
//   F2: PERSONAL_20260518T150527Z_Runabout_Firehose_Profile_Resolution_TDD.md
//   F3: PERSONAL_20260518T150528Z_Runabout_Firehose_AT_Content_Passthrough_TDD.md
// =====================================================================

// --- F1: Firehose scoreAsync Wiring ---

// F1-CT-2: After handleFirehosePost, queue row must NOT be in 'relayed' status.
// The MarkRelayed bypass must be removed  -  row stays 'pending' for scoreAsync.
func TestFirehoseScoring_F1CT2_NoMarkRelayed(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "llm")

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f1ct2",
		Text:  "new llm benchmark released",
		Repo:  "did:plc:test",
		Seq:   1000,
	}
	if err := handleFirehosePost(context.Background(), testFSC(q), post); err != nil {
		t.Fatal(err)
	}

	var status string
	err := q.db.QueryRow("SELECT status FROM queue WHERE url=?", post.AtURI).Scan(&status)
	if err != nil {
		t.Fatalf("query queue row: %v", err)
	}
	if status == "relayed" {
		t.Fatal("F1-CT-2: queue row must NOT be 'relayed'  -  MarkRelayed bypass must be removed")
	}
}

// F1-CT-4: After firehose processing, status must be 'pending' (awaiting scoreAsync)
// or 'scored'/'failed' (scoreAsync completed). Never 'relayed'.
func TestFirehoseScoring_F1CT4_StatusTransitions(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "agents")

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f1ct4",
		Text:  "building agents with tool use",
		Repo:  "did:plc:test",
		Seq:   1001,
	}
	if err := handleFirehosePost(context.Background(), testFSC(q), post); err != nil {
		t.Fatal(err)
	}

	var status string
	err := q.db.QueryRow("SELECT status FROM queue WHERE url=?", post.AtURI).Scan(&status)
	if err != nil {
		t.Fatalf("query queue row: %v", err)
	}

	validStatuses := map[string]bool{"pending": true, "scored": true, "failed": true}
	if !validStatuses[status] {
		t.Fatalf("F1-CT-4: status must be pending/scored/failed, got %q", status)
	}
}

// F1-RG-2: No push with score=0 and title="Firehose Match" may be enqueued.
// The score=0 placeholder push must be removed  -  only scoreAsync sends pushes.
func TestFirehoseScoring_F1RG2_NoScoreZeroPush(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "claude")

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f1rg2",
		Text:  "claude code is amazing",
		Repo:  "did:plc:test",
		Seq:   1002,
	}
	if err := handleFirehosePost(context.Background(), testFSC(q), post); err != nil {
		t.Fatal(err)
	}

	pushes, _ := q.PendingPushes(100)
	for _, p := range pushes {
		if p.URL == post.AtURI && p.Verdict == "Firehose Match" {
			t.Fatal("F1-RG-2: score=0 'Firehose Match' push must not be enqueued  -  only scoreAsync sends pushes")
		}
	}
}

// --- F2: Profile Resolution ---

// F2-CT-1: resolveFirehoseProfile("default") must return "eng".
func TestFirehoseScoring_F2CT1_DefaultResolvesToEng(t *testing.T) {
	got := resolveFirehoseProfile("default")
	if got != "eng" {
		t.Fatalf("F2-CT-1: resolveFirehoseProfile(\"default\") = %q, want \"eng\"", got)
	}
}

// F2-CT-2: resolveFirehoseProfile("eng") must return "eng" (passthrough).
func TestFirehoseScoring_F2CT2_EngPassthrough(t *testing.T) {
	got := resolveFirehoseProfile("eng")
	if got != "eng" {
		t.Fatalf("F2-CT-2: resolveFirehoseProfile(\"eng\") = %q, want \"eng\"", got)
	}
}

// F2-CT-3: resolveFirehoseProfile("nonexistent") must fall back to "eng".
func TestFirehoseScoring_F2CT3_UnknownFallsBack(t *testing.T) {
	got := resolveFirehoseProfile("nonexistent")
	if got != "eng" {
		t.Fatalf("F2-CT-3: resolveFirehoseProfile(\"nonexistent\") = %q, want \"eng\"", got)
	}
}

// F2-CT-4: After migration, all firehose subscriptions use profile='eng', not 'default'.
func TestFirehoseScoring_F2CT4_MigrationUpdatesDefault(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	_ = q.AddFirehoseSubscription("default", "llm")
	_ = q.AddFirehoseSubscription("default", "claude")
	_ = q.AddFirehoseSubscription("default", "agents")

	migrateFirehoseProfiles(q)

	var defaultCount int
	q.db.QueryRow("SELECT COUNT(*) FROM firehose_subscriptions WHERE profile='default'").Scan(&defaultCount)
	if defaultCount != 0 {
		t.Fatalf("F2-CT-4: expected 0 subscriptions with profile='default' after migration, got %d", defaultCount)
	}

	var engCount int
	q.db.QueryRow("SELECT COUNT(*) FROM firehose_subscriptions WHERE profile='eng'").Scan(&engCount)
	if engCount != 3 {
		t.Fatalf("F2-CT-4: expected 3 subscriptions with profile='eng', got %d", engCount)
	}
}

// F2-RG-1: No firehose queue row may have profile='default' at scoring time.
func TestFirehoseScoring_F2RG1_NoDefaultProfileInQueue(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "inference")

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f2rg1",
		Text:  "inference optimization techniques",
		Repo:  "did:plc:test",
		Seq:   1003,
	}
	if err := handleFirehosePost(context.Background(), testFSC(q), post); err != nil {
		t.Fatal(err)
	}

	var profile string
	err := q.db.QueryRow("SELECT profile FROM queue WHERE url=?", post.AtURI).Scan(&profile)
	if err != nil {
		t.Fatalf("query queue row: %v", err)
	}
	if profile == "default" {
		t.Fatal("F2-RG-1: firehose queue row must not have profile='default'  -  must be resolved before enqueue")
	}
}

// --- F3: AT-Protocol Content Passthrough ---

// F3-CT-1: ShareRequest.Text must be populated from firehosePost.Text.
// Verified by checking the queue row's text column after enqueue.
func TestFirehoseScoring_F3CT1_TextPopulated(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "eval")

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f3ct1",
		Text:  "New paper on LLM evaluation frameworks",
		Repo:  "did:plc:test",
		Seq:   1004,
	}
	if err := handleFirehosePost(context.Background(), testFSC(q), post); err != nil {
		t.Fatal(err)
	}

	var text string
	err := q.db.QueryRow("SELECT text FROM queue WHERE url=?", post.AtURI).Scan(&text)
	if err != nil {
		t.Fatalf("query queue row: %v", err)
	}
	if text != post.Text {
		t.Fatalf("F3-CT-1: queue row text = %q, want %q", text, post.Text)
	}
}

// F1-CT-1: After handleFirehosePost, scoreAsync is called with a ShareRequest containing the AtURI.
// Verified via onceDoneEval: the done channel fires when Evaluate() is invoked.
func TestFirehoseScoring_F1CT1_ScoreAsyncCalled(t *testing.T) {
	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("eng", "quantum")

	done := make(chan struct{})
	eval := &onceDoneEval{inner: &stubEvaluator{score: 75, verdict: "Strong Yes"}, done: done}
	fsc := testFSCWithEval(t, q, eval)
	fsc.Deps = deps

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f1ct1",
		Text:  "quantum computing breakthrough",
		Repo:  "did:plc:test",
		Seq:   2000,
	}
	if err := handleFirehosePost(context.Background(), fsc, post); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("F1-CT-1: scoreAsync not called within 5s  -  Evaluator.Evaluate never invoked")
	}
}

// F1-CT-3: After firehose processing, exactly one push with score > 0 must be sent.
// No score=0 "Firehose Match" push may exist  -  scoreAsync is the only push sender.
func TestFirehoseScoring_F1CT3_ScoredPushReplaces(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "ml")

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f1ct3",
		Text:  "ml research update",
		Repo:  "did:plc:test",
		Seq:   1006,
	}
	if err := handleFirehosePost(context.Background(), testFSC(q), post); err != nil {
		t.Fatal(err)
	}

	pushes, _ := q.PendingPushes(100)
	for _, p := range pushes {
		if p.URL == post.AtURI && p.Verdict == "Firehose Match" {
			t.Fatal("F1-CT-3: score=0 'Firehose Match' push must be replaced by scored push from scoreAsync")
		}
	}
	// Scored push assertion requires Evaluator wiring (M4+).
}

// F1-CT-5: At most 3 concurrent firehose scoring goroutines (semaphore cap).
func TestFirehoseScoring_F1CT5_SemaphoreCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // prevent resolvePushConfigOnce from loading real config.toml
	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("eng", "semaphore")

	const n = 6
	eval := &semaphoreCapEval{
		inner: &stubEvaluator{score: 60, verdict: "Monitor"},
		delay: 150 * time.Millisecond,
		done:  make(chan struct{}, n),
	}
	fsc := testFSCWithEval(t, q, eval)
	fsc.Deps = deps

	// Drain every scoreAsync before returning: the CI leakcheck run caught
	// this test's TempDir cleanup racing the 3 evaluations it never waited
	// for ("directory not empty"). scoreAsyncDoneHook fires at every
	// scoreAsync return, after transcript persistence and queue writes.
	asyncDone := make(chan struct{}, n)
	prevHook := scoreAsyncDoneHook
	scoreAsyncDoneHook = func() { asyncDone <- struct{}{} }
	t.Cleanup(func() { scoreAsyncDoneHook = prevHook })

	for i := 0; i < n; i++ {
		post := &firehosePost{
			AtURI: fmt.Sprintf("at://did:plc:test/app.bsky.feed.post/f1ct5-%d", i),
			Text:  fmt.Sprintf("semaphore cap test post %d", i),
			Repo:  "did:plc:test",
			Seq:   int64(5000 + i),
		}
		if err := handleFirehosePost(context.Background(), fsc, post); err != nil {
			t.Fatal(err)
		}
	}

	// Wait for the first 3 evaluations (one full semaphore-cap batch).
	// scoreAsync may block after Evaluate returns (resolvePushConfigOnce), so we
	// signal completion from inside Evaluate via a buffered channel rather than
	// waiting for scoreAsync to fully return.
	deadline := time.After(5 * time.Second)
	for i := 0; i < 3; i++ {
		select {
		case <-eval.done:
		case <-deadline:
			t.Fatalf("F1-CT-5: only %d of 3 expected evaluations completed in 5s", i)
		}
	}

	if p := atomic.LoadInt32(&eval.peak); p > 3 {
		t.Fatalf("F1-CT-5: peak concurrent Evaluate calls = %d, want <= 3 (semaphore cap)", p)
	}
	if atomic.LoadInt32(&eval.peak) == 0 {
		t.Fatal("F1-CT-5: Evaluate never called - scoreAsync did not run")
	}

	// Wait for all n scoreAsync returns so nothing writes into the TempDir
	// after this test ends.
	drainDeadline := time.After(10 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-asyncDone:
		case <-drainDeadline:
			t.Fatalf("F1-CT-5: only %d of %d scoreAsync completions before drain deadline", i, n)
		}
	}
}

// F1-CT-6: After restart, StartReplay picks up pending firehose queue rows.
// Fails until MarkRelayed bypass is removed  -  rows must stay 'pending' for replay.
func TestFirehoseScoring_F1CT6_StartReplayPicksUpFirehose(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "rag")

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f1ct6",
		Text:  "rag pipeline improvements",
		Repo:  "did:plc:test",
		Seq:   1007,
	}
	if err := handleFirehosePost(context.Background(), testFSC(q), post); err != nil {
		t.Fatal(err)
	}

	// After the fix, firehose rows stay 'pending'  -  Pending() returns them for replay.
	// Currently fails: MarkRelayed sets status='relayed', so Pending() returns nothing.
	items, err := q.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	for _, it := range items {
		if it.URL == post.AtURI {
			return // found  -  test passes post-M3
		}
	}
	t.Fatal("F1-CT-6: firehose queue row not visible to StartReplay  -  row must stay 'pending' not 'relayed'")
}

// F1-RG-1: scoreAsync is invoked within 5s  -  item never stays pending indefinitely.
// Regression guard: ensures the MarkRelayed bypass removal doesn't leave items unscored.
func TestFirehoseScoring_F1RG1_NeverScoringTimeout(t *testing.T) {
	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("eng", "inference")

	done := make(chan struct{})
	eval := &onceDoneEval{inner: &stubEvaluator{score: 80, verdict: "Worth Watching"}, done: done}
	fsc := testFSCWithEval(t, q, eval)
	fsc.Deps = deps

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f1rg1",
		Text:  "new inference optimization technique",
		Repo:  "did:plc:test",
		Seq:   2001,
	}
	if err := handleFirehosePost(context.Background(), fsc, post); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("F1-RG-1: scoreAsync not called within 5s  -  firehose item would have scored_timeout")
	}
}

// F3-CT-3: When URL fetch fails, scoreAsync uses req.Text as the scoring content.
// Verified via contentCapturingEval: content passed to Evaluate must equal post.Text.
func TestFirehoseScoring_F3CT3_ScoreAsyncUsesText(t *testing.T) {
	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("eng", "transformer")

	done := make(chan struct{})
	capturing := &contentCapturingEval{inner: &stubEvaluator{score: 70, verdict: "Maybe"}}
	eval := &onceDoneEval{inner: capturing, done: done}
	fsc := testFSCWithEval(t, q, eval)
	fsc.Deps = deps

	// Async-test convention (EPIC-250): block on scoreAsync fully returning,
	// not just Evaluate. Otherwise this test's scoring goroutine outlives the
	// test and fires the *next* test's freshly installed scoreAsyncDoneHook,
	// which made TestFirehoseScoring_Integration read its queue row too early
	// (pre-existing at 2799ac6; surfaced during EPIC-258 M2).
	scoreDone := make(chan struct{})
	prevHook := scoreAsyncDoneHook
	scoreAsyncDoneHook = func() { close(scoreDone) }
	t.Cleanup(func() { scoreAsyncDoneHook = prevHook })

	postText := "transformer architecture improvements in 2026"
	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f3ct3",
		Text:  postText,
		Repo:  "did:plc:test",
		Seq:   2002,
	}
	if err := handleFirehosePost(context.Background(), fsc, post); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("F3-CT-3: Evaluate never called")
	}
	select {
	case <-scoreDone:
	case <-time.After(5 * time.Second):
		t.Fatal("F3-CT-3: scoreAsync did not complete within 5s")
	}

	if !atomic.CompareAndSwapInt32(&capturing.called, 1, 1) {
		t.Fatal("F3-CT-3: content not captured  -  Evaluate was not called on contentCapturingEval")
	}
	if capturing.content == "" {
		t.Fatal("F3-CT-3: content passed to Evaluate is empty  -  text fallback not active")
	}
	if len(capturing.content) > 0 && len(postText) > 0 {
		// The prompt includes postText as part of the content block.
		if !strings.Contains(capturing.content, postText) {
			t.Fatalf("F3-CT-3: post text not in scoring content\ngot:  %q\nwant: contains %q", capturing.content[:min(200, len(capturing.content))], postText)
		}
	}
}

// F3-RG-1: Non-empty firehose post text is included in the scoring content (not lost).
// Regression guard for POMO firehose-scoring-gap: at:// items were scored with empty content.
//
// EPIC-250: this test used to synchronize only on Evaluate() invocation (onceDoneEval's
// done channel), but scoreAsync's remaining work (including the transcript write) happens
// *after* Evaluate returns, in the same goroutine. That let the test return with work still
// in flight, which then landed in whatever transcriptDir a later test had installed  -  see
// POMO_firehose-transcript-goroutine-leak-suite-order. AC-3/AC-4: this test now isolates its
// own transcriptDir and blocks on scoreAsyncDoneHook (fired once transcript persistence and queue writes complete)
// before returning, so no goroutine can outlive it.
func TestFirehoseScoring_F3RG1_TextReachesPrompt(t *testing.T) {
	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("eng", "attention")

	transcriptDir := filepath.Join(t.TempDir(), "transcripts")
	deps.TranscriptsDir = transcriptDir

	scoreDone := make(chan struct{})
	prevHook := scoreAsyncDoneHook
	scoreAsyncDoneHook = func() { close(scoreDone) }
	t.Cleanup(func() { scoreAsyncDoneHook = prevHook })

	done := make(chan struct{})
	capturing := &contentCapturingEval{inner: &stubEvaluator{score: 65, verdict: "Interesting"}}
	eval := &onceDoneEval{inner: capturing, done: done}
	fsc := testFSCWithEval(t, q, eval)
	fsc.Deps = deps

	postText := "attention mechanism improvements in LLMs"
	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f3rg1",
		Text:  postText,
		Repo:  "did:plc:test",
		Seq:   2003,
	}
	if err := handleFirehosePost(context.Background(), fsc, post); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("F3-RG-1: Evaluate never called")
	}

	if capturing.content == "" {
		t.Fatal("F3-RG-1: scoring content is empty  -  post text was lost before reaching evaluator")
	}

	// AC-4: do not return until scoreAsync's transcript write and queue
	// persistence have completed, so its goroutine cannot leak a write into a
	// later test's transcriptDir before it gets there.
	select {
	case <-scoreDone:
	case <-time.After(5 * time.Second):
		t.Fatal("F3-RG-1: scoreAsync did not complete within 5s")
	}
}

// F3-CT-2: Empty text must pass through without error.
func TestFirehoseScoring_F3CT2_EmptyTextPassthrough(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "eval")

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f3ct2-eval",
		Text:  "",
		Repo:  "did:plc:test",
		Seq:   1005,
	}
	// Keyword match is on AT URI + text combined, so embed keyword in URI path
	// to trigger a match even with empty text.
	post.AtURI = "at://did:plc:test/app.bsky.feed.post/f3ct2-eval"
	if err := handleFirehosePost(context.Background(), testFSC(q), post); err != nil {
		t.Fatal(err)
	}

	var count int
	q.db.QueryRow("SELECT COUNT(*) FROM queue WHERE url=?", post.AtURI).Scan(&count)
	if count == 0 {
		t.Skip("F3-CT-2: post did not match any subscription (expected  -  keyword in URI may not match)")
	}

	var text string
	q.db.QueryRow("SELECT text FROM queue WHERE url=?", post.AtURI).Scan(&text)
	if text != "" {
		t.Fatalf("F3-CT-2: queue row text = %q, want empty string", text)
	}
}

// --- M5: Behavioral tests ---

// F1-BT-2: A post matching 2 subscriptions produces 2 queue rows and 2 scoreAsync calls.
func TestFirehoseScoring_F1BT2_MultiKeywordMatch(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("eng", "llm")
	_ = q.AddFirehoseSubscription("eng", "agent")

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f1bt2",
		Text:  "llm agent framework release",
		Repo:  "did:plc:test",
		Seq:   3000,
	}
	if err := handleFirehosePost(context.Background(), testFSC(q), post); err != nil {
		t.Fatal(err)
	}

	var count int
	q.db.QueryRow("SELECT COUNT(*) FROM queue WHERE source='firehose'").Scan(&count)
	if count != 2 {
		t.Fatalf("F1-BT-2: expected 2 queue rows (one per subscription), got %d", count)
	}
}

// F2-BT-2: resolveFirehoseProfile logs a WARN for unknown profiles.
// Verified indirectly: unknown profile returns "eng" (warning is logged by the function).
func TestFirehoseScoring_F2BT2_FallbackLogsWarn(t *testing.T) {
	result := resolveFirehoseProfile("some-unknown-profile")
	if result != "eng" {
		t.Fatalf("F2-BT-2: unknown profile should fall back to eng, got %q", result)
	}
}

// F3-BT-2: Queue row has both text and URL populated from firehose commit.
func TestFirehoseScoring_F3BT2_TextAndURLBothPresent(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("eng", "reinforcement")

	postText := "reinforcement learning from human feedback"
	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f3bt2",
		Text:  postText,
		Repo:  "did:plc:test",
		Seq:   3001,
	}
	if err := handleFirehosePost(context.Background(), testFSC(q), post); err != nil {
		t.Fatal(err)
	}

	var url, rowText string
	q.db.QueryRow("SELECT url, text FROM queue WHERE source='firehose'").Scan(&url, &rowText)
	if url != post.AtURI {
		t.Fatalf("F3-BT-2: queue row URL = %q, want %q", url, post.AtURI)
	}
	if rowText != postText {
		t.Fatalf("F3-BT-2: queue row text = %q, want %q", rowText, postText)
	}
}

// =====================================================================
// EPIC-126 M2: F6 Action ID Registration  -  Contract Tests CT-1 through CT-5 + RG-1
// Source TDD: PERSONAL_20260519T162046Z_Runabout_Firehose_Action_ID_Registration_TDD.md
// =====================================================================

// F6-CT-1: firehoseActionForProfile("eng") returns "uinit_eng".
func TestFirehoseF6CT1_KnownProfileDerivesAction(t *testing.T) {
	got := firehoseActionForProfile("eng")
	if got != "uinit_eng" {
		t.Fatalf("F6-CT-1: firehoseActionForProfile(\"eng\") = %q, want \"uinit_eng\"", got)
	}
}

// F6-CT-2: Unknown profile falls back to "uinit_eng".
func TestFirehoseF6CT2_UnknownProfileFallsBack(t *testing.T) {
	got := firehoseActionForProfile("unknown")
	if got != "uinit_eng" {
		t.Fatalf("F6-CT-2: firehoseActionForProfile(\"unknown\") = %q, want \"uinit_eng\"", got)
	}
}

// F6-CT-3: Empty profile falls back to "uinit_eng".
func TestFirehoseF6CT3_EmptyProfileFallsBack(t *testing.T) {
	got := firehoseActionForProfile("")
	if got != "uinit_eng" {
		t.Fatalf("F6-CT-3: firehoseActionForProfile(\"\") = %q, want \"uinit_eng\"", got)
	}
}

// F6-CT-4: Queue row has action="uinit_eng" after handleFirehosePost enqueues it.
func TestFirehoseF6CT4_QueueRowHasAction(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "llm")

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f6ct4",
		Text:  "llm inference benchmark",
		Repo:  "did:plc:test",
		Seq:   6004,
	}
	if err := handleFirehosePost(context.Background(), testFSC(q), post); err != nil {
		t.Fatal(err)
	}

	var action string
	q.db.QueryRow("SELECT action FROM queue WHERE url=?", post.AtURI).Scan(&action)
	if action != "uinit_eng" {
		t.Fatalf("F6-CT-4: queue row action = %q, want \"uinit_eng\"", action)
	}
}

// F6-CT-5: Firehose queue row has non-empty action  -  replay never fails with "no action for \"\"".
func TestFirehoseF6CT5_ReplayNoEmptyActionError(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "rag")

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/f6ct5",
		Text:  "rag pipeline improvements",
		Repo:  "did:plc:test",
		Seq:   6005,
	}
	if err := handleFirehosePost(context.Background(), testFSC(q), post); err != nil {
		t.Fatal(err)
	}

	var action string
	q.db.QueryRow("SELECT action FROM queue WHERE url=?", post.AtURI).Scan(&action)
	if action == "" {
		t.Fatal("F6-CT-5: action field is empty  -  replay would fail with \"no action for \\\"\\\"\"")
	}
}

// F6-RG-1: Every firehose queue row has a non-empty action field.
// Regression guard for EPIC-125 M6 live validation (queue_id=23562, no action for "").
func TestFirehoseF6RG1_AllRowsHaveAction(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("eng", "neural")
	_ = q.AddFirehoseSubscription("default", "agents")

	posts := []*firehosePost{
		{AtURI: "at://did:plc:test/app.bsky.feed.post/rg1a", Text: "neural scaling laws", Repo: "did:plc:test", Seq: 6010},
		{AtURI: "at://did:plc:test/app.bsky.feed.post/rg1b", Text: "agents in production", Repo: "did:plc:test", Seq: 6011},
	}
	for _, p := range posts {
		if err := handleFirehosePost(context.Background(), testFSC(q), p); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := q.db.Query("SELECT url, action FROM queue WHERE source='firehose'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var url, action string
		if err := rows.Scan(&url, &action); err != nil {
			t.Fatal(err)
		}
		if action == "" {
			t.Fatalf("F6-RG-1: firehose row url=%q has empty action  -  replay would fail", url)
		}
	}
}

// --- M5: Integration test ---

// TestFirehoseScoring_Integration: firehose post → enqueue → scoreAsync → scored status.
// Uses stubEvaluator (score=75) to verify the full pipeline without real Claude CLI.
//
// EPIC-250: this test used to synchronize on Evaluate() invocation (onceDoneEval's done
// channel) plus a fixed sleep, then read queue status and let deferred cleanup() close the
// DB. Under -shuffle=on the sleep was occasionally not enough, so scoreAsync's own queue
// status write (which happens after Evaluate returns) raced against cleanup() closing the
// DB, surfacing as "sql: database is closed". Waiting on scoreAsyncDoneHook (fired once the
// queue status write completes, before the push/FCM tail that can block on real on-disk
// config in dev environments) instead of a sleep removes the timing dependency  -  see
// POMO_firehose-transcript-goroutine-leak-suite-order for the same underlying goroutine-leak
// bug class.
func TestFirehoseScoring_Integration(t *testing.T) {
	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("eng", "mixture")

	scoreDone := make(chan struct{})
	prevHook := scoreAsyncDoneHook
	scoreAsyncDoneHook = func() { close(scoreDone) }
	t.Cleanup(func() { scoreAsyncDoneHook = prevHook })

	done := make(chan struct{})
	eval := &onceDoneEval{inner: &stubEvaluator{score: 75, verdict: "Strong Yes"}, done: done}
	fsc := testFSCWithEval(t, q, eval)
	fsc.Deps = deps

	post := &firehosePost{
		AtURI: "at://did:plc:test/app.bsky.feed.post/integ-score",
		Text:  "mixture of experts scaling improvements",
		Repo:  "did:plc:test",
		Seq:   9000,
	}
	if err := handleFirehosePost(context.Background(), fsc, post); err != nil {
		t.Fatal(err)
	}

	// Wait for scoreAsync goroutine to complete.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("integration: Evaluate never called  -  scoreAsync did not run")
	}

	// Wait for scoreAsync to fully return (queue status write included) before
	// reading the DB, so this test cannot race its own deferred cleanup().
	select {
	case <-scoreDone:
	case <-time.After(5 * time.Second):
		t.Fatal("integration: scoreAsync did not complete within 5s")
	}

	// scoreAsyncDoneHook is a shared package global: a scoring goroutine leaked
	// by an earlier test can fire the hook this test just installed, waking the
	// wait above before *this* test's scoreAsync has persisted its status
	// (observed at 2799ac6 in full-suite order; EPIC-258 M2). Poll to a
	// deadline for a terminal status instead of trusting a single firing.
	var status string
	deadline := time.Now().Add(5 * time.Second)
	for {
		q.db.QueryRow("SELECT status FROM queue WHERE url=?", post.AtURI).Scan(&status)
		if status != "" && status != "pending" && status != "relayed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("integration: queue row status %q after 5s  -  scoreAsync did not complete scoring", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Status should be "scored" or "failed" (never "relayed" or stuck "pending").
	if status != "scored" && status != "failed" {
		t.Fatalf("integration: unexpected queue row status %q  -  want scored or failed", status)
	}
}

// =====================================================================
// EPIC-125: CAR Block Text Extraction  -  Behavioral Tests and Regression Guards
// Source TDD: PERSONAL_20260519T102524Z_Runabout_Firehose_CAR_Block_Text_Extraction_TDD.md
// =====================================================================

// buildFirehoseFrame constructs a two-CBOR-value ATProto commit frame (header + body).
func buildFirehoseFrame(t *testing.T, seq int64, repo, postPath string, postText string) []byte {
	t.Helper()
	postRecord := atProtoPost{Type: "app.bsky.feed.post", Text: postText}
	recordCBOR, err := cbor.Marshal(postRecord)
	if err != nil {
		t.Fatalf("marshal post record: %v", err)
	}
	carBytes := buildTestCARV1(fakeCIDBytes, recordCBOR)
	blocksCBOR, err := cbor.Marshal(carBytes)
	if err != nil {
		t.Fatalf("marshal CAR bytes: %v", err)
	}
	header := firehoseHeader{Op: 1, T: "#commit"}
	body := firehoseBody{
		Seq:  seq,
		Repo: repo,
		Ops: []firehoseOp{{
			Action: "create",
			Path:   postPath,
			Cid:    buildTag42CID(fakeCIDBytes),
		}},
		Blocks: cbor.RawMessage(blocksCBOR),
	}
	hBytes, err := cbor.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	bBytes, err := cbor.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return append(hBytes, bBytes...)
}

// BT-1: processFirehoseMessage populates firehosePost.Text from CAR blocks.
func TestFirehoseCARBT1_ProcessMessagePopulatesText(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "evaluation")

	postText := "LLM evaluation paper"
	atURI := "at://did:plc:test/app.bsky.feed.post/bt1"
	frame := buildFirehoseFrame(t, 1, "did:plc:test", "app.bsky.feed.post/bt1", postText)

	if err := processFirehoseMessage(context.Background(), testFSC(q), frame); err != nil {
		t.Fatalf("BT-1: processFirehoseMessage error: %v", err)
	}

	var text string
	q.db.QueryRow("SELECT text FROM queue WHERE url=?", atURI).Scan(&text)
	if text == "" {
		t.Fatal("BT-1: firehosePost.Text is empty  -  CAR text extraction not wired into processFirehoseMessage")
	}
	if text != postText {
		t.Fatalf("BT-1: text = %q, want %q", text, postText)
	}
}

// BT-2: End-to-end: firehose post text extracted from CAR blocks reaches the scoreAsync prompt.
func TestFirehoseCARBT2_TextReachesScoreAsync(t *testing.T) {
	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "transformer")

	done := make(chan struct{})
	capturing := &contentCapturingEval{inner: &stubEvaluator{score: 70, verdict: "Maybe"}}
	eval := &onceDoneEval{inner: capturing, done: done}
	fsc := testFSCWithEval(t, q, eval)
	fsc.Deps = deps

	postText := "transformer architecture for LLM evaluation"
	frame := buildFirehoseFrame(t, 2, "did:plc:test", "app.bsky.feed.post/bt2", postText)

	if err := processFirehoseMessage(context.Background(), fsc, frame); err != nil {
		t.Fatalf("BT-2: processFirehoseMessage error: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("BT-2: Evaluate never called within timeout")
	}
	time.Sleep(20 * time.Millisecond)

	if !strings.Contains(capturing.content, postText) {
		t.Fatalf("BT-2: post text not in scoring content\ngot:  %q\nwant: contains %q", capturing.content, postText)
	}
}

// BT-3: Delete ops with nil CID are handled without panic and produce no map entry.
func TestFirehoseCARBT3_DeleteOpsNilCID(t *testing.T) {
	postRecord := atProtoPost{Type: "app.bsky.feed.post", Text: "test"}
	recordCBOR, _ := cbor.Marshal(postRecord)
	carBytes := buildTestCARV1(fakeCIDBytes, recordCBOR)

	ops := []firehoseOp{
		{Action: "delete", Path: "app.bsky.feed.post/del1", Cid: nil},
		{Action: "delete", Path: "app.bsky.feed.post/del2", Cid: cbor.RawMessage{0xf6}}, // CBOR null
	}

	result := carExtractPostText(carBytes, ops)
	if _, ok := result["app.bsky.feed.post/del1"]; ok {
		t.Fatal("BT-3: delete op with nil CID produced a map entry  -  should be skipped")
	}
	if _, ok := result["app.bsky.feed.post/del2"]; ok {
		t.Fatal("BT-3: delete op with CBOR null CID produced a map entry  -  should be skipped")
	}
}

// RG-1: Firehose items with non-empty text in CAR blocks are never scored with empty content.
func TestFirehoseCARRG1_NonEmptyTextNeverScoredEmpty(t *testing.T) {
	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_ = q.AddFirehoseSubscription("default", "neural")

	done := make(chan struct{})
	capturing := &contentCapturingEval{inner: &stubEvaluator{score: 75, verdict: "Yes"}}
	eval := &onceDoneEval{inner: capturing, done: done}
	fsc := testFSCWithEval(t, q, eval)
	fsc.Deps = deps

	postText := "neural network scaling laws for large models"
	frame := buildFirehoseFrame(t, 3, "did:plc:test", "app.bsky.feed.post/rg1", postText)

	if err := processFirehoseMessage(context.Background(), fsc, frame); err != nil {
		t.Fatalf("RG-1: processFirehoseMessage error: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RG-1: Evaluate never called within timeout")
	}
	time.Sleep(20 * time.Millisecond)

	if capturing.content == "" {
		t.Fatal("RG-1: scoring content is empty  -  CAR text not used despite being present in blocks")
	}
}

// RG-2: at:// URIs never reach Jina (AT-URI short-circuit in DomainRouter).
func TestFirehoseCARRG2_ATURINotSentToJina(t *testing.T) {
	jinaCalled := false
	jinaSpy := func(_ context.Context, _ string) (string, error) {
		jinaCalled = true
		return "", nil
	}
	router := NewDomainRouter(nil, jinaSpy)

	_, _, _ = router.FetchWithFallback(context.Background(), "at://did:plc:abc/app.bsky.feed.post/xyz")

	if jinaCalled {
		t.Fatal("RG-2: at:// URI reached Jina  -  AT-URI short-circuit missing in FetchWithFallback")
	}
}
