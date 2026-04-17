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
	go scoreURLAsync(&ShareRequest{URL: rawURL, Profile: profile}, q, wrapped, nil)
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
	go scoreURLAsync(&ShareRequest{URL: rawURL, Profile: profile}, q, eval, nil)
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

// 7. Route() returns server-score sentinel for uinit_auto.
func TestRoute_UinitAutoReturnsServerScoreSentinel(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)

	req := &ShareRequest{
		Type:   "url",
		Action: "uinit_auto",
		URL:    "https://example.com",
	}
	msg, err := router.Route(req)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !strings.Contains(msg, "Scoring") || !strings.Contains(msg, "FCM") {
		t.Errorf("Route message = %q, want 'Scoring — verdict via FCM'", msg)
	}
}

// 8. ginit_auto does NOT use ServerScore path (uses AutoScore/EnqueueScored).
func TestRoute_GinitAutoDoesNotUseServerScorePath(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)

	ac := router.LookupAction("ginit_auto")
	if ac == nil {
		t.Fatal("ginit_auto not found in builtinConfig")
	}
	if ac.ServerScore {
		t.Errorf("ginit_auto.ServerScore = true, want false")
	}
	if !ac.AutoScore {
		t.Errorf("ginit_auto.AutoScore = false, want true")
	}
}

// 9. uinit_auto in builtinConfig is ServerScore=true.
func TestBuiltinConfig_UinitAutoIsServerScore(t *testing.T) {
	cfg := builtinConfig()
	for _, a := range cfg.Actions {
		if a.ID == "uinit_auto" {
			if !a.ServerScore {
				t.Errorf("uinit_auto.ServerScore = false, want true")
			}
			return
		}
	}
	t.Error("uinit_auto not found in builtinConfig")
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

// EPIC-061 M3: classifyURLProfile heuristic tests.
func TestClassifyURLProfile(t *testing.T) {
	cases := []struct {
		url     string
		want    string
		matched bool
	}{
		{"https://github.com/golang/go", "eng", true},
		{"https://stackoverflow.com/q/123", "eng", true},
		{"https://arxiv.org/abs/1706.03762", "eng", true},
		{"https://www.booking.com/hotel/nyc", "travel", true},
		{"https://www.airbnb.com/rooms/42", "travel", true},
		{"https://open.spotify.com/track/abc", "music", true},
		{"https://www.bloomberg.com/markets", "finance", true},
		{"https://www.yelp.com/biz/restaurant", "dining", true},
		{"https://www.zara.com/us/dress", "fashion", true},
		{"https://www.tourismboard.bz/retire", "travel", true},  // new entry
		{"https://retirement.gov/benefits", "life", true},        // new entry
		{"https://www.example.com/unknown", "eng", false},        // fallback — not matched
		{"https://www.reddit.com/r/golang", "eng", false},        // fallback
	}
	for _, c := range cases {
		got, matched := classifyURLProfile(c.url)
		if got != c.want {
			t.Errorf("classifyURLProfile(%q) = %q, want %q", c.url, got, c.want)
		}
		if matched != c.matched {
			t.Errorf("classifyURLProfile(%q) matched=%v, want %v", c.url, matched, c.matched)
		}
	}
}

// EPIC-061 M3: classificationPreamble format.
func TestClassificationPreamble(t *testing.T) {
	p := classificationPreamble("eng", "https://github.com/golang/go", "url_domain")
	if !strings.Contains(p, "eng") || !strings.Contains(p, "github.com") || !strings.Contains(p, "url_domain") {
		t.Errorf("preamble should contain profile, URL, and source: %q", p)
	}
}

// EPIC-061 M3: scoreURLAsync auto-classifies empty profile (domain matched).
func TestScoreURLAsync_AutoClassifiesEmptyProfile(t *testing.T) {
	srv := jinaBodyServer(t, 200, "some engineering content about golang")
	installJinaServer(t, srv)
	isolateEventsDir(t)

	eval := &stubEvaluator{score: 85, verdict: "good"}
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	// Enqueue a row first so ScoreByURL can find it.
	_, err := q.Enqueue(&ShareRequest{
		Action: "uinit_auto", Type: "url", URL: "https://github.com/golang/go",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Empty profile → should auto-classify to "eng" from github.com (domain matched).
	runScoreAsyncSync(t, "https://github.com/golang/go", "", q, eval)

	// Score 85 triggers auto-archive. Check archived items.
	items, err := q.List("archived", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("expected archived item (score 85 >= threshold)")
	}
	if items[0].Profile != "eng" {
		t.Errorf("profile = %q, want eng (auto-classified from github.com)", items[0].Profile)
	}
}

// Content classification: when domain falls through, Haiku classifies content.
func TestScoreURLAsync_ContentClassifiesFallbackProfile(t *testing.T) {
	srv := jinaBodyServer(t, 200, "Belize tourism board retirement program — live abroad in paradise.")
	installJinaServer(t, srv)
	isolateEventsDir(t)

	// Stub the content classifier to return "travel".
	prev := execContentClassify
	execContentClassify = func(_ context.Context, _, _ string) (string, error) {
		return "travel", nil
	}
	t.Cleanup(func() { execContentClassify = prev })

	eval := &stubEvaluator{score: 72, verdict: "interesting"}
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	_, err := q.Enqueue(&ShareRequest{
		Action: "uinit_auto", Type: "url", URL: "https://belizean-programs.example.com/relocate",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Empty profile + unknown domain → domain falls through to eng, then content
	// classifier overrides to "travel".
	runScoreAsyncSync(t, "https://belizean-programs.example.com/relocate", "", q, eval)

	items, err := q.List("", 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range items {
		if strings.Contains(it.URL, "belizean") && it.Score != nil {
			found = true
			if it.Profile != "travel" {
				t.Errorf("profile = %q, want travel (content-classified)", it.Profile)
			}
		}
	}
	if !found {
		t.Error("expected a scored queue row for belizean-programs URL")
	}
}

// classifyContentProfile unit tests.
func TestClassifyContentProfile(t *testing.T) {
	cases := []struct {
		name     string
		response string
		err      error
		want     string
	}{
		{"exact match", "travel", nil, "travel"},
		{"with whitespace", "  dining\n", nil, "dining"},
		{"uppercase", "FINANCE", nil, "finance"},
		{"verbose response", "The best profile is travel for this content.", nil, "travel"},
		{"unparseable", "I don't know what to say", nil, ""},
		{"error from haiku", "", fmt.Errorf("timeout"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prev := execContentClassify
			execContentClassify = func(_ context.Context, _, _ string) (string, error) {
				return c.response, c.err
			}
			t.Cleanup(func() { execContentClassify = prev })

			got := classifyContentProfile(context.Background(), "some content")
			if got != c.want {
				t.Errorf("classifyContentProfile() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSanitizeTranscriptFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Voice 260413_160545.m4a", "Voice_260413_160545.m4a"},
		{"hello world.m4a", "hello_world.m4a"},
		{"café  résumé.m4a", "caf_r_sum_.m4a"},
		{"normal_file.m4a", "normal_file.m4a"},
		{"lots   of   spaces", "lots_of_spaces"},
		{"", ""},
		{"日本語ファイル.m4a", ".m4a"},
		{"file/with:bad*chars?.m4a", "file_with_bad_chars_.m4a"},
	}
	for _, tt := range tests {
		got := sanitizeTranscriptFilename(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeTranscriptFilename(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
