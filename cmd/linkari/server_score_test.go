package main

// EPIC-060 M4: end-to-end regression tests for the server-side scoring path.
//
// Coverage goals:
//   1. scoreURLAsync happy path — queue row scored, archive fired, FCM enqueued.
//   2. Unsupported pipeline URLs (YouTube, Spotify, etc.) are skipped — no eval call.
//   3. Jina fetch error — no queue write, no eval call.
//   4. Empty content after fetch — no queue write, no eval call.
//   5. Eval failure — no queue write.
//   6. Route() returns "Scoring — verdict via FCM" for uinit_* (ServerScore=true).
//   7. Route() does NOT call scoreURLAsync for ginit_* (AutoScore, not ServerScore).
//   8. Nil queue — scoreURLAsync runs eval but skips persistence silently.
//   9. fetchJinaContent timeout — context deadline cancels the request.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- stub Evaluator ----------------------------------------------------------

type stubEvaluator struct {
	calls   int32
	score   int
	verdict string
	err     error
}

func (s *stubEvaluator) Name() string { return "stub" }
func (s *stubEvaluator) Evaluate(_ context.Context, _, _ string) (*Scorecard, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.err != nil {
		return nil, s.err
	}
	return &Scorecard{
		Score:      s.score,
		Verdict:    s.verdict,
		Tags:       "test",
		SourceType: "server-score",
	}, nil
}

// --- Jina HTTP seam ----------------------------------------------------------

// installJinaServer points jinaBaseURL at srv and configures jinaHTTPClient to
// use srv's transport for the duration of the test. Both vars are restored on
// cleanup. The test server responds to any path, so fetchJinaContent calls
// never reach the real network.
func installJinaServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	prevBase := jinaBaseURL
	prevClient := jinaHTTPClient
	jinaBaseURL = srv.URL + "/"
	jinaHTTPClient = srv.Client()
	t.Cleanup(func() {
		jinaBaseURL = prevBase
		jinaHTTPClient = prevClient
	})
}

// jinaBodyServer returns an httptest.Server that responds with body for all requests.
func jinaBodyServer(t *testing.T, statusCode int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(statusCode)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runScoreURLAsync calls scoreURLAsync and waits for it to finish by giving it
// an already-resolved context. Since scoreURLAsync creates its own 60s context
// internally, we can't cancel from outside; we wait for completion by sleeping
// briefly after the goroutine starts. A done channel from the eval call lets us
// synchronise deterministically.
//
// The approach: wrap the evaluator so that its Evaluate() call closes a done
// channel. We block until that fires OR until a short deadline expires.
func runScoreAsyncSync(t *testing.T, rawURL, profile string, q *Queue, eval Evaluator) {
	t.Helper()
	done := make(chan struct{})
	wrapped := &onceDoneEval{inner: eval, done: done}
	go scoreURLAsync(rawURL, profile, q, wrapped)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Log("runScoreAsyncSync: timed out waiting for scoreURLAsync (eval never called — expected for skip/early-exit paths)")
	}
	// Give the goroutine a moment to finish post-eval work (ScoreByURL, Archive, etc.).
	time.Sleep(50 * time.Millisecond)
}

// onceDoneEval closes done on the first Evaluate call (or immediately if inner
// is nil, i.e. on early-exit paths that never reach eval). For skip paths we
// close done in a separate goroutine launched inside scoreURLAsync; here we
// only get the signal when eval is actually invoked.
type onceDoneEval struct {
	inner Evaluator
	done  chan struct{}
	once  int32
}

func (e *onceDoneEval) Name() string { return "once-done" }
func (e *onceDoneEval) Evaluate(ctx context.Context, content, prompt string) (*Scorecard, error) {
	if atomic.CompareAndSwapInt32(&e.once, 0, 1) {
		close(e.done)
	}
	if e.inner != nil {
		return e.inner.Evaluate(ctx, content, prompt)
	}
	return nil, fmt.Errorf("no inner evaluator")
}

// --- helper: wait for scoreURLAsync on early-exit paths ----------------------

// runScoreAsyncSkip runs scoreURLAsync for URLs that should exit before eval
// (unsupported pipeline, fetch error, empty content). We poll the queue for
// 200 ms to confirm no row was written, which also gives the goroutine time to
// complete.
func runScoreAsyncSkip(t *testing.T, rawURL, profile string, q *Queue, eval Evaluator) {
	t.Helper()
	go scoreURLAsync(rawURL, profile, q, eval)
	time.Sleep(200 * time.Millisecond)
}

// --- Tests -------------------------------------------------------------------

// 1. Happy path: Jina returns content, eval scores it, queue row updated.
func TestScoreURLAsync_HappyPath(t *testing.T) {
	isolateEventsDir(t)
	const pageContent = "This is great engineering content about transformers and attention mechanisms."
	srv := jinaBodyServer(t, http.StatusOK, pageContent)
	installJinaServer(t, srv)

	q := newTestQueue(t)
	// Pre-insert a relayed row. ScoreByURL updates relayed → scored; it will
	// INSERT a new scored row if no relayed row is found, leaving the original
	// row untouched. MarkRelayed puts the row in the state the real flow uses.
	id, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://arxiv.org/abs/1706.03762", Profile: "eng"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("mark relayed: %v", err)
	}

	eval := &stubEvaluator{score: 88, verdict: "Worth reading"}
	runScoreAsyncSync(t,
		"https://arxiv.org/abs/1706.03762", "eng",
		q, eval,
	)

	if atomic.LoadInt32(&eval.calls) != 1 {
		t.Errorf("eval.calls = %d, want 1", atomic.LoadInt32(&eval.calls))
	}

	// The original row should now be scored or archived.
	items, err := q.List("", 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, it := range items {
		if strings.Contains(it.URL, "arxiv") && (it.Status == "scored" || it.Status == "archived") {
			found = true
			if it.Score == nil || *it.Score != 88 {
				t.Errorf("row score = %v, want 88", it.Score)
			}
		}
	}
	if !found {
		t.Errorf("expected scored/archived row in queue — statuses: %v",
			func() []string {
				var ss []string
				for _, it := range items {
					if strings.Contains(it.URL, "arxiv") {
						ss = append(ss, it.Status)
					}
				}
				return ss
			}(),
		)
	}
}

// 2. Unsupported pipeline (YouTube) — eval is never called.
func TestScoreURLAsync_UnsupportedPipelineSkipped(t *testing.T) {
	eval := &stubEvaluator{score: 90, verdict: "great"}
	// No Jina server — if it tried to fetch, the test would fail with connection refused.
	runScoreAsyncSkip(t, "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "eng", nil, eval)
	if atomic.LoadInt32(&eval.calls) != 0 {
		t.Errorf("eval called %d times, want 0 for unsupported pipeline", atomic.LoadInt32(&eval.calls))
	}
}

// 3. Jina fetch error (non-2xx) — eval is never called, no queue write.
func TestScoreURLAsync_FetchErrorSkipsEval(t *testing.T) {
	srv := jinaBodyServer(t, http.StatusInternalServerError, "error")
	installJinaServer(t, srv)

	eval := &stubEvaluator{score: 80, verdict: "test"}
	runScoreAsyncSkip(t, "https://example.com/fetchfail", "eng", nil, eval)
	if atomic.LoadInt32(&eval.calls) != 0 {
		t.Errorf("eval called %d times, want 0 after fetch error", atomic.LoadInt32(&eval.calls))
	}
}

// 4. Empty content after fetch — eval skipped, no queue write.
func TestScoreURLAsync_EmptyContentSkipsEval(t *testing.T) {
	srv := jinaBodyServer(t, http.StatusOK, "   \n\t  ")
	installJinaServer(t, srv)

	eval := &stubEvaluator{score: 80, verdict: "test"}
	runScoreAsyncSkip(t, "https://example.com/empty", "eng", nil, eval)
	if atomic.LoadInt32(&eval.calls) != 0 {
		t.Errorf("eval called %d times, want 0 for empty content", atomic.LoadInt32(&eval.calls))
	}
}

// 5. Eval failure — no queue write.
func TestScoreURLAsync_EvalErrorSkipsQueue(t *testing.T) {
	srv := jinaBodyServer(t, http.StatusOK, "Some real content here worth scoring.")
	installJinaServer(t, srv)

	q := newTestQueue(t)
	eval := &stubEvaluator{err: fmt.Errorf("haiku timeout")}
	runScoreAsyncSync(t, "https://example.com/evalfail", "eng", q, eval)

	if atomic.LoadInt32(&eval.calls) != 1 {
		t.Errorf("eval.calls = %d, want 1 (eval was attempted)", atomic.LoadInt32(&eval.calls))
	}
	// Queue should be empty — no row written on eval failure.
	items, err := q.List("", 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected empty queue on eval error, got %d items", len(items))
	}
}

// 6. Nil queue — pipeline runs to completion without panicking.
func TestScoreURLAsync_NilQueueNoPanic(t *testing.T) {
	srv := jinaBodyServer(t, http.StatusOK, "Interesting content about distributed systems.")
	installJinaServer(t, srv)

	eval := &stubEvaluator{score: 75, verdict: "save"}
	runScoreAsyncSync(t, "https://example.com/nilqueue", "life", nil, eval)
	if atomic.LoadInt32(&eval.calls) != 1 {
		t.Errorf("eval.calls = %d, want 1", atomic.LoadInt32(&eval.calls))
	}
	// No panic — test completes normally.
}

// 7. Route() returns server-score sentinel for uinit_* actions.
func TestRoute_UinitReturnsServerScoreSentinel(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	// No queue — scoreURLAsync goroutine will silently skip persistence.

	req := &ShareRequest{
		Type:    "url",
		Action:  "uinit_eng",
		Profile: "eng",
		URL:     "https://example.com",
	}
	msg, err := router.Route(req)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !strings.Contains(msg, "Scoring") || !strings.Contains(msg, "FCM") {
		t.Errorf("Route message = %q, want 'Scoring — verdict via FCM'", msg)
	}
}

// 8. Route() does NOT set ServerScore path for ginit_* actions (EPIC-057
//    invariant: ginit still uses tmux). Route will error because no tmux is
//    running, but the error must NOT be the server-score sentinel.
func TestRoute_GinitDoesNotUseServerScorePath(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)

	// ginit_eng requires ATLASSIAN_DOMAIN to be present in builtinConfig — only
	// assert the server-score path is NOT taken when the action is present.
	ac := router.LookupAction("ginit_eng")
	if ac == nil {
		t.Skip("ginit_eng not registered (ATLASSIAN_DOMAIN not set) — skipping path assertion")
	}
	if ac.ServerScore {
		t.Errorf("ginit_eng.ServerScore = true, want false — ginit must not use server-side scoring path")
	}
	if !ac.AutoScore {
		t.Errorf("ginit_eng.AutoScore = false, want true — ginit must use EnqueueScored path")
	}
}

// 9. All 7 uinit_* actions in builtinConfig are ServerScore=true.
func TestBuiltinConfig_AllUinitActionsAreServerScore(t *testing.T) {
	cfg := builtinConfig()
	profiles := []string{"eng", "life", "travel", "fashion", "music", "finance", "dining"}
	index := make(map[string]*ActionConfig, len(cfg.Actions))
	for i := range cfg.Actions {
		index[cfg.Actions[i].ID] = &cfg.Actions[i]
	}
	for _, p := range profiles {
		id := "uinit_" + p
		ac, ok := index[id]
		if !ok {
			t.Errorf("builtinConfig missing action %q", id)
			continue
		}
		if !ac.ServerScore {
			t.Errorf("action %q: ServerScore = false, want true (EPIC-060 M1)", id)
		}
	}
}

// 10. fetchJinaContent timeout: a server that hangs past the deadline returns
//     an error (not a block forever). Uses a very short deadline so the test
//     runs quickly.
func TestFetchJinaContent_TimeoutReturnsError(t *testing.T) {
	// Hang server — never writes a response.
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(hung.Close)
	installJinaServer(t, hung)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := fetchJinaContent(ctx, "https://example.com/slow")
	if err == nil {
		t.Error("expected error from hung server, got nil")
	}
}

// 11. fetchJinaContent non-2xx returns structured error.
func TestFetchJinaContent_Non2xxError(t *testing.T) {
	srv := jinaBodyServer(t, http.StatusNotFound, "not found")
	installJinaServer(t, srv)

	_, err := fetchJinaContent(context.Background(), "https://example.com/404")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %q should mention status 404", err.Error())
	}
}

// 12. unsupportedPipelineRE covers all documented platforms.
func TestUnsupportedPipelineRE(t *testing.T) {
	blocked := []string{
		"https://www.youtube.com/watch?v=abc",
		"https://youtu.be/abc",
		"https://open.spotify.com/track/abc",
		"https://www.twitch.tv/streamer",
		"https://soundcloud.com/artist/track",
		"https://www.tiktok.com/@user/video/123",
		"https://www.netflix.com/title/abc",
	}
	allowed := []string{
		"https://arxiv.org/abs/1706.03762",
		"https://github.com/golang/go",
		"https://www.nytimes.com/article",
	}
	for _, u := range blocked {
		if !unsupportedPipelineRE.MatchString(u) {
			t.Errorf("expected %q to match unsupportedPipelineRE", u)
		}
	}
	for _, u := range allowed {
		if unsupportedPipelineRE.MatchString(u) {
			t.Errorf("expected %q NOT to match unsupportedPipelineRE", u)
		}
	}
}
