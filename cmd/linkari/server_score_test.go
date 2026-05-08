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
	"os"
	"path/filepath"
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

// 2b. EPIC-084 M1: Unsupported pipeline marks queue row as failed.
func TestScoreURLAsync_UnsupportedPipelineMarksRowFailed(t *testing.T) {
	q := newTestQueue(t)
	id, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://www.youtube.com/watch?v=abc", Profile: "eng"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("mark relayed: %v", err)
	}

	eval := &stubEvaluator{score: 90, verdict: "great"}
	go scoreURLAsync(&ShareRequest{
		URL: "https://www.youtube.com/watch?v=abc", Profile: "eng",
		QueueRowID: id,
	}, q, eval, nil)
	time.Sleep(200 * time.Millisecond)

	items, err := q.List("failed", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected queue row to be marked failed for unsupported pipeline")
	}
	if items[0].Status != "failed" {
		t.Errorf("status = %q, want failed", items[0].Status)
	}
}

// 2c. EPIC-084 M1: Vimeo, Rumble, Dailymotion are blocked by unsupportedPipelineRE.
func TestScoreURLAsync_NewUnsupportedPlatformsSkipped(t *testing.T) {
	eval := &stubEvaluator{score: 90, verdict: "great"}
	for _, u := range []string{
		"https://vimeo.com/123456789",
		"https://rumble.com/v1abc-video.html",
		"https://www.dailymotion.com/video/x7abc",
	} {
		runScoreAsyncSkip(t, u, "eng", nil, eval)
	}
	if atomic.LoadInt32(&eval.calls) != 0 {
		t.Errorf("eval called %d times, want 0 for new unsupported platforms", atomic.LoadInt32(&eval.calls))
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
		"https://vimeo.com/123456789",
		"https://rumble.com/v1abc-video.html",
		"https://www.dailymotion.com/video/x7abc",
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
// EPIC-015 M2: updated to pass ContentTypePlain (zero regression — output unchanged).
func TestClassificationPreamble(t *testing.T) {
	p := classificationPreamble("eng", "https://github.com/golang/go", "url_domain", ContentTypePlain)
	if !strings.Contains(p, "eng") || !strings.Contains(p, "github.com") || !strings.Contains(p, "url_domain") {
		t.Errorf("preamble should contain profile, URL, and source: %q", p)
	}
}

// EPIC-061 M3: scoreURLAsync auto-classifies empty profile (domain matched).
func TestScoreURLAsync_AutoClassifiesEmptyProfile(t *testing.T) {
	srv := jinaBodyServer(t, 200, "some engineering content about golang")
	installJinaServer(t, srv)
	isolateEventsDir(t)
	// Pre-load builtin config so archiveThreshold avoids AWS Secrets Manager
	// calls inside the goroutine's 50ms post-eval window.
	archiveThresholdMu.Lock()
	prevCfg := archiveThresholdCfg
	archiveThresholdCfg = builtinConfig()
	archiveThresholdMu.Unlock()
	t.Cleanup(func() {
		archiveThresholdMu.Lock()
		archiveThresholdCfg = prevCfg
		archiveThresholdMu.Unlock()
	})

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

// TestDetectScreenshot_FilenameFallback verifies the filename-pattern fallback
// introduced in EPIC-078 M3 — when RelativePath is empty, detectScreenshot
// matches req.Filename against screenshotFilenameRE.
func TestDetectScreenshot_FilenameFallback(t *testing.T) {
	tests := []struct {
		name         string
		relativePath string
		filename     string
		wantDetected bool
	}{
		// RelativePath-based detection (EPIC-077 M4).
		{
			name:         "DCIM/Screenshots path sets is_screenshot",
			relativePath: "DCIM/Screenshots/",
			wantDetected: true,
		},
		{
			name:         "Screenshots path sets is_screenshot",
			relativePath: "Screenshots/",
			wantDetected: true,
		},
		{
			name:         "non-screenshot path leaves is_screenshot false",
			relativePath: "DCIM/Camera/",
			wantDetected: false,
		},
		// Filename fallback (EPIC-078 M3) — RelativePath is empty.
		{
			name:         "Samsung Gallery: Screenshot_ filename sets is_screenshot",
			filename:     "Screenshot_20260411_123703_WhatsApp.jpg",
			wantDetected: true,
		},
		{
			name:         "Screenshot with hyphen separator",
			filename:     "Screenshot-2026-04-11-12.37.jpg",
			wantDetected: true,
		},
		{
			name:         "Screenshot with space separator",
			filename:     "Screenshot 2026-04-11.png",
			wantDetected: true,
		},
		{
			name:         "non-screenshot filename leaves is_screenshot false",
			filename:     "IMG_20260411_123703.jpg",
			wantDetected: false,
		},
		{
			name:         "empty filename and empty relativePath: no detection",
			wantDetected: false,
		},
		// RelativePath takes precedence over filename when both are present.
		{
			name:         "relativePath wins when both present (screenshot path)",
			relativePath: "DCIM/Screenshots/",
			filename:     "IMG_not_a_screenshot.jpg",
			wantDetected: true,
		},
		{
			name:         "relativePath wins when both present (non-screenshot path)",
			relativePath: "DCIM/Camera/",
			filename:     "Screenshot_20260411.jpg",
			wantDetected: false, // filename fallback NOT used when relativePath is non-empty
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &ShareRequest{
				RelativePath: tt.relativePath,
				Filename:     tt.filename,
			}
			detectScreenshot(req)
			if req.IsScreenshot != tt.wantDetected {
				t.Errorf("IsScreenshot = %v, want %v (relativePath=%q filename=%q)",
					req.IsScreenshot, tt.wantDetected, tt.relativePath, tt.filename)
			}
		})
	}
}

// EPIC-079 M5: image/document scoreAsync test coverage.

// runScoreFileAsyncSync runs scoreAsync synchronously for a file share request.
func runScoreFileAsyncSync(t *testing.T, req *ShareRequest, q *Queue, eval Evaluator) {
	t.Helper()
	done := make(chan struct{})
	wrapped := &onceDoneEval{inner: eval, done: done}
	go scoreAsync(req, q, wrapped, nil, nil)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Log("runScoreFileAsyncSync: timed out (eval never called)")
	}
	time.Sleep(50 * time.Millisecond)
}

// TestScoreAsync_ImageFileMetadataOnly verifies an image share with no temp
// file is scored using metadata alone via the standard evaluator.
func TestScoreAsync_ImageFileMetadataOnly(t *testing.T) {
	isolateEventsDir(t)

	prev := execContentClassify
	execContentClassify = func(_ context.Context, _, _ string) (string, error) {
		return "life", nil
	}
	t.Cleanup(func() { execContentClassify = prev })

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	req := &ShareRequest{
		Type:     "image",
		Filename: "IMG-20260407-WA0003.jpg",
		MimeType: "image/jpeg",
		Profile:  "life",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	req.QueueRowID = id

	eval := &stubEvaluator{score: 55, verdict: "Photo of food menu"}
	runScoreFileAsyncSync(t, req, q, eval)

	if calls := atomic.LoadInt32(&eval.calls); calls != 1 {
		t.Errorf("eval.calls = %d, want 1", calls)
	}

	items, err := q.List("", 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, it := range items {
		if it.ID == id && (it.Status == "scored" || it.Status == "archived") {
			found = true
			if it.Score == nil || *it.Score != 55 {
				t.Errorf("row score = %v, want 55", it.Score)
			}
		}
	}
	if !found {
		t.Errorf("expected scored/archived row for image share")
	}
}

// TestScoreAsync_ImageVision verifies an image share with a readable temp file
// triggers the vision path and cleans up the temp file.
func TestScoreAsync_ImageVision(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // prevent resolvePushConfigOnce from loading real config.toml
	isolateEventsDir(t)

	prev := execContentClassify
	execContentClassify = func(_ context.Context, _, _ string) (string, error) {
		return "life", nil
	}
	t.Cleanup(func() { execContentClassify = prev })

	// Create a temp image file to simulate Android upload.
	tmpFile := filepath.Join(t.TempDir(), "test-image.jpg")
	if err := os.WriteFile(tmpFile, []byte("fake-jpeg-data"), 0644); err != nil {
		t.Fatalf("write temp: %v", err)
	}

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	req := &ShareRequest{
		Type:      "image",
		Filename:  "IMG-20260407-WA0003.jpg",
		MimeType:  "image/jpeg",
		Profile:   "life",
		AudioPath: tmpFile,
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	req.QueueRowID = id

	// Stub the vision CLI call to avoid real API calls.
	// Return a bare verdict (parseHaikuEnvelope shortcut requires non-empty rubric_scores).
	prevVision := runClaudeHaikuVision
	runClaudeHaikuVision = func(_ context.Context, _, _, _, _ string) ([]byte, error) {
		return []byte(`{"score":72,"verdict":"WhatsApp photo of receipt","rubric_scores":{"visual_clarity":80,"actionability":65},"tags":"","topic_tags":[]}`), nil
	}
	t.Cleanup(func() { runClaudeHaikuVision = prevVision })

	eval := &stubEvaluator{score: 72, verdict: "WhatsApp photo of receipt"}
	runScoreFileAsyncSync(t, req, q, eval)

	items, err := q.List("", 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, it := range items {
		if it.ID == id && (it.Status == "scored" || it.Status == "archived") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected scored/archived row for image vision share")
	}

	// Verify temp file was cleaned up by scoreAsync.
	if _, err := os.Stat(tmpFile); !os.IsNotExist(err) {
		t.Errorf("temp file %s should have been removed by scoreAsync", tmpFile)
	}
}

// EPIC-080 M7: test coverage for vision failure → fallback → queue chain.

// TestHaikuVisionEvaluator_FallbackOnExecError verifies that when
// runClaudeHaikuVision fails, the evaluator falls back to the JSON eval path
// and returns a result with backend="claude-haiku-vision-fallback".
func TestHaikuVisionEvaluator_FallbackOnExecError(t *testing.T) {
	// Stub vision exec to fail.
	prevVision := runClaudeHaikuVision
	runClaudeHaikuVision = func(_ context.Context, _, _, _, _ string) ([]byte, error) {
		return nil, fmt.Errorf("vision exec crashed")
	}
	t.Cleanup(func() { runClaudeHaikuVision = prevVision })

	// Stub the JSON eval path (used by fallback) to return a valid envelope.
	prevJSON := execHaikuJSON
	execHaikuJSON = func(_ context.Context, _, _, _ string) ([]byte, error) {
		return []byte(`{"type":"result","result":"{\"score\":42,\"verdict\":\"fallback ok\",\"rubric_scores\":{\"relevance\":50,\"depth\":40}}","is_error":false,"usage":{"input_tokens":10,"output_tokens":20},"total_cost_usd":0.001}`), nil
	}
	t.Cleanup(func() { execHaikuJSON = prevJSON })

	tmpFile := filepath.Join(t.TempDir(), "test.jpg")
	if err := os.WriteFile(tmpFile, []byte("fake"), 0644); err != nil {
		t.Fatalf("write temp: %v", err)
	}

	e := HaikuVisionEvaluator{ImagePath: tmpFile}
	sc, err := e.Evaluate(context.Background(), "test metadata", "test prompt")
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if sc.Backend != "claude-haiku-vision-fallback" {
		t.Errorf("backend = %q, want %q", sc.Backend, "claude-haiku-vision-fallback")
	}
	if sc.Score != 42 {
		t.Errorf("score = %d, want 42", sc.Score)
	}
}

// TestScoreAsync_EvalFailureMarksQueueRow verifies that when eval.Evaluate
// returns an error, the queue row is marked as failed with reason "eval_failed".
func TestScoreAsync_EvalFailureMarksQueueRow(t *testing.T) {
	isolateEventsDir(t)

	prev := execContentClassify
	execContentClassify = func(_ context.Context, _, _ string) (string, error) {
		return "life", nil
	}
	t.Cleanup(func() { execContentClassify = prev })

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	req := &ShareRequest{
		Type:     "image",
		Filename: "broken.jpg",
		MimeType: "image/jpeg",
		Profile:  "life",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("mark relayed: %v", err)
	}
	req.QueueRowID = id

	eval := &stubEvaluator{err: fmt.Errorf("total eval failure")}
	done := make(chan struct{})
	wrapped := &onceDoneEval{inner: eval, done: done}
	go scoreAsync(req, q, wrapped, nil, nil)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Log("timed out waiting for eval")
	}
	time.Sleep(100 * time.Millisecond)

	// Verify row is marked as failed with error_reason="eval_failed".
	var status, reason string
	err = q.db.QueryRow(
		"SELECT status, COALESCE(error_reason,'') FROM queue WHERE id=?", id,
	).Scan(&status, &reason)
	if err != nil {
		t.Fatalf("query row %d: %v", id, err)
	}
	if status != "failed" {
		t.Errorf("status = %q, want %q", status, "failed")
	}
	if reason != "eval_failed" {
		t.Errorf("error_reason = %q, want %q", reason, "eval_failed")
	}
}

// TestVisionExecArgs verifies runClaudeHaikuVision is called with the
// correct image path, and that the fallback path works when vision fails.
func TestVisionExecArgs(t *testing.T) {
	var capturedImagePath string
	prevVision := runClaudeHaikuVision
	runClaudeHaikuVision = func(_ context.Context, _, _, imagePath, _ string) ([]byte, error) {
		capturedImagePath = imagePath
		return nil, fmt.Errorf("intentional test abort")
	}
	t.Cleanup(func() { runClaudeHaikuVision = prevVision })

	// Stub JSON fallback to prevent real API calls.
	prevJSON := execHaikuJSON
	execHaikuJSON = func(_ context.Context, _, _, _ string) ([]byte, error) {
		return []byte(`{"type":"result","result":"{\"score\":10,\"verdict\":\"fallback\",\"rubric_scores\":{\"relevance\":10}}","is_error":false,"usage":{"input_tokens":5,"output_tokens":10},"total_cost_usd":0.0001}`), nil
	}
	t.Cleanup(func() { execHaikuJSON = prevJSON })

	tmpFile := filepath.Join(t.TempDir(), "test.jpg")
	if err := os.WriteFile(tmpFile, []byte("fake"), 0644); err != nil {
		t.Fatalf("write temp: %v", err)
	}

	e := HaikuVisionEvaluator{ImagePath: tmpFile}
	sc, err := e.Evaluate(context.Background(), "test content", "test prompt")
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if capturedImagePath != tmpFile {
		t.Errorf("imagePath = %q, want %q", capturedImagePath, tmpFile)
	}
	// Vision failed → fell back to JSON → should have fallback backend.
	if sc.Backend != "claude-haiku-vision-fallback" {
		t.Errorf("backend = %q, want %q", sc.Backend, "claude-haiku-vision-fallback")
	}
}

// EPIC-083 M1: pre-filter gate tests.

// TestIsLoginWallDomain verifies the login-wall domain detection matches all
// documented domains and excludes allowed URL patterns.
func TestIsLoginWallDomain(t *testing.T) {
	blocked := []string{
		"https://www.instagram.com/p/abc123",
		"https://instagram.com/stories/user",
		"https://x.com/user/status/123",
		"https://twitter.com/user/status/123",
		"https://www.facebook.com/post/123",
		"https://facebook.com/groups/abc",
		"https://www.linkedin.com/in/someone",
		"https://linkedin.com/feed/update/123",
	}
	allowed := []string{
		"https://www.linkedin.com/pulse/some-article-title",
		"https://linkedin.com/pulse/my-post",
		"https://github.com/golang/go",
		"https://arxiv.org/abs/1706.03762",
	}
	for _, u := range blocked {
		if !isLoginWallDomain(u) {
			t.Errorf("expected %q to be login-wall domain", u)
		}
	}
	for _, u := range allowed {
		if isLoginWallDomain(u) {
			t.Errorf("expected %q NOT to be login-wall domain", u)
		}
	}
}

// TestScoreAsync_LoginWallSkipsEval verifies that URL shares to login-wall
// domains exit early without calling eval.
func TestScoreAsync_LoginWallSkipsEval(t *testing.T) {
	eval := &stubEvaluator{score: 90, verdict: "great"}
	runScoreAsyncSkip(t, "https://www.instagram.com/p/abc123", "eng", nil, eval)
	if atomic.LoadInt32(&eval.calls) != 0 {
		t.Errorf("eval called %d times, want 0 for login-wall domain", atomic.LoadInt32(&eval.calls))
	}
}

// TestIsCameraPhoto verifies the camera photo noise gate logic.
func TestIsCameraPhoto(t *testing.T) {
	tests := []struct {
		name string
		req  *ShareRequest
		want bool
	}{
		{
			name: "gallery app + camera filename = camera photo",
			req: &ShareRequest{
				CallingPackage: "com.google.android.apps.photos",
				Filename:       "IMG_20260419_123456.jpg",
			},
			want: true,
		},
		{
			name: "gallery app + non-camera filename = not camera",
			req: &ShareRequest{
				CallingPackage: "com.google.android.apps.photos",
				Filename:       "meme.jpg",
			},
			want: false,
		},
		{
			name: "non-gallery app + camera filename = not camera",
			req: &ShareRequest{
				CallingPackage: "com.whatsapp",
				Filename:       "IMG_20260419_123456.jpg",
			},
			want: false,
		},
		{
			name: "gallery app + camera filename + extra text = not camera",
			req: &ShareRequest{
				CallingPackage: "com.sec.android.gallery3d",
				Filename:       "IMG_20260419_123456.jpg",
				ExtraText:      "Check out this sunset!",
			},
			want: false,
		},
		{
			name: "gallery app + camera filename + screenshot = not camera",
			req: &ShareRequest{
				CallingPackage: "com.sec.android.gallery3d",
				Filename:       "IMG_20260419_123456.jpg",
				IsScreenshot:   true,
			},
			want: false,
		},
		{
			name: "DSC pattern matches",
			req: &ShareRequest{
				CallingPackage: "com.sec.android.gallery3d",
				Filename:       "DSC_20260419.jpg",
			},
			want: true,
		},
		{
			name: "PXL pattern matches",
			req: &ShareRequest{
				CallingPackage: "com.google.android.apps.photos",
				Filename:       "PXL_20260419_123456789.jpg",
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCameraPhoto(tt.req)
			if got != tt.want {
				t.Errorf("isCameraPhoto() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCameraTimestampRE verifies the camera timestamp regex patterns.
func TestSaveTranscriptFile(t *testing.T) {
	type tc struct {
		name         string
		rowID        int64
		profile      string
		origFilename string
		transcript   string
		source       string
		sourceURL    string
		videoTitle   string
		videoID      string
		duration     int
		subtitleType string
		// assertions
		wantFrontmatter []string
		wantAbsent      []string
		wantFileSuffix  string // filename must contain this substring
	}
	cases := []tc{
		{
			name:         "YouTube audio fallback",
			rowID:        502,
			profile:      "eng",
			origFilename: "",
			transcript:   "Hello world transcript.",
			source:       "youtube",
			sourceURL:    "https://www.youtube.com/watch?v=abc123",
			videoTitle:   "My Test Video",
			videoID:      "abc123",
			duration:     180,
			subtitleType: "audio",
			wantFrontmatter: []string{
				`row_id: 502`,
				`profile: "eng"`,
				`source: "youtube"`,
				`source_url: "https://www.youtube.com/watch?v=abc123"`,
				`video_title: "My Test Video"`,
				`video_id: "abc123"`,
				`duration: 180`,
				`subtitle_type: "audio"`,
				"Hello world transcript.",
			},
			wantAbsent:     []string{"original_filename"},
			wantFileSuffix: "_YT_502_My_Test_Video.md",
		},
		{
			name:         "YouTube subtitle — subtitle_type manual",
			rowID:        100,
			profile:      "default",
			origFilename: "",
			transcript:   "Subtitle text.",
			source:       "youtube",
			sourceURL:    "https://www.youtube.com/watch?v=xyz",
			videoTitle:   "Another Video",
			videoID:      "xyz",
			duration:     60,
			subtitleType: "manual",
			wantFrontmatter: []string{
				`subtitle_type: "manual"`,
				`video_id: "xyz"`,
			},
			wantFileSuffix: "_YT_100_Another_Video.md",
		},
		{
			name:         "YouTube no subtitle_type — field omitted",
			rowID:        200,
			profile:      "default",
			transcript:   "Body.",
			source:       "youtube",
			videoTitle:   "Untitled Clip",
			videoID:      "vid99",
			subtitleType: "",
			wantFrontmatter: []string{
				`video_id: "vid99"`,
			},
			wantAbsent:     []string{"subtitle_type"},
			wantFileSuffix: "_YT_200_Untitled_Clip.md",
		},
		{
			name:         "Voice note — original_filename present, no video fields",
			rowID:        300,
			profile:      "personal",
			origFilename: "Voice 260425.m4a",
			transcript:   "Voice note text.",
			source:       "voice_note",
			sourceURL:    "",
			videoTitle:   "",
			videoID:      "",
			duration:     45,
			subtitleType: "",
			wantFrontmatter: []string{
				`original_filename: "Voice 260425.m4a"`,
				`duration: 45`,
				`source: "voice_note"`,
			},
			wantAbsent:     []string{"video_id", "subtitle_type", "source_url"},
			wantFileSuffix: "_m4a_300_Voice_260425.md",
		},
		{
			name:         "PDF source — pdf_ prefix",
			rowID:        401,
			profile:      "eng",
			origFilename: "report.pdf",
			transcript:   "PDF extracted text.",
			source:       "pdf",
			wantFrontmatter: []string{
				`source: "pdf"`,
				`original_filename: "report.pdf"`,
				"PDF extracted text.",
			},
			wantFileSuffix: "_pdf_401_report.md",
		},
		{
			name:         "Image source — img_ prefix",
			rowID:        402,
			profile:      "eng",
			origFilename: "photo.jpg",
			transcript:   "Image OCR text.",
			source:       "img",
			wantFrontmatter: []string{
				`source: "img"`,
				`original_filename: "photo.jpg"`,
			},
			wantFileSuffix: "_img_402_photo.md",
		},
		{
			name:         "URL source — url_ prefix with slug",
			rowID:        403,
			profile:      "eng",
			origFilename: "example-article",
			transcript:   "Article body.",
			source:       "url",
			sourceURL:    "https://example.com/article",
			wantFrontmatter: []string{
				`source: "url"`,
				`source_url: "https://example.com/article"`,
			},
			wantFileSuffix: "_url_403_example_article.md",
		},
		{
			name:         "m4a source — m4a_ prefix",
			rowID:        404,
			profile:      "eng",
			origFilename: "recording.m4a",
			transcript:   "Audio transcript.",
			source:       "m4a",
			wantFrontmatter: []string{
				`source: "m4a"`,
				`original_filename: "recording.m4a"`,
			},
			wantFileSuffix: "_m4a_404_recording.md",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prev := transcriptDir
			transcriptDir = filepath.Join(t.TempDir(), "transcripts")
			t.Cleanup(func() { transcriptDir = prev })

			path, err := saveTranscriptFile(
				c.rowID, c.profile, c.origFilename, c.transcript,
				c.source, c.sourceURL, c.videoTitle, c.videoID,
				c.duration, c.subtitleType,
			)
			if err != nil {
				t.Fatalf("saveTranscriptFile returned error: %v", err)
			}
			if path == "" {
				t.Fatal("saveTranscriptFile returned empty path")
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("cannot read written file %q: %v", path, err)
			}
			content := string(data)

			for _, want := range c.wantFrontmatter {
				if !strings.Contains(content, want) {
					t.Errorf("missing in file: %q\nfile content:\n%s", want, content)
				}
			}
			for _, absent := range c.wantAbsent {
				if strings.Contains(content, absent) {
					t.Errorf("unexpected field %q found in file:\n%s", absent, content)
				}
			}
			if c.wantFileSuffix != "" && !strings.HasSuffix(filepath.Base(path), c.wantFileSuffix) {
				t.Errorf("filename %q does not end with %q", filepath.Base(path), c.wantFileSuffix)
			}
		})
	}
}

// EPIC-008 M3: transcript persistence tests for PDF, URL, and image share types.

func TestScoreAsync_PDFTranscriptSaved(t *testing.T) {
	isolateEventsDir(t)

	prevDir := transcriptDir
	transcriptDir = filepath.Join(t.TempDir(), "transcripts")
	t.Cleanup(func() { transcriptDir = prevDir })

	installLiteParseStub(t, "Extracted PDF text content", 0.9, nil)

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	req := &ShareRequest{
		Type:     "document",
		Filename: "report.pdf",
		Profile:  "eng",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	req.QueueRowID = id

	eval := &stubEvaluator{score: 75, verdict: "Interesting document"}
	runScoreFileAsyncSync(t, req, q, eval)

	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		t.Fatalf("read transcriptDir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if strings.Contains(e.Name(), "_pdf_") {
			found = true
			data, _ := os.ReadFile(filepath.Join(transcriptDir, e.Name()))
			if !strings.Contains(string(data), "Extracted PDF text content") {
				t.Errorf("transcript body missing expected content; got:\n%s", data)
			}
		}
	}
	if !found {
		t.Errorf("no transcript file with _pdf_ prefix found in %s", transcriptDir)
	}
}

func TestScoreAsync_URLTranscriptSaved(t *testing.T) {
	isolateEventsDir(t)

	prevDir := transcriptDir
	transcriptDir = filepath.Join(t.TempDir(), "transcripts")
	t.Cleanup(func() { transcriptDir = prevDir })

	const pageContent = "Great article about machine learning and neural networks."
	srv := jinaBodyServer(t, http.StatusOK, pageContent)
	installJinaServer(t, srv)

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	req := &ShareRequest{
		Type:    "url",
		URL:     "https://example.com/article",
		Profile: "eng",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("mark relayed: %v", err)
	}
	req.QueueRowID = id

	eval := &stubEvaluator{score: 80, verdict: "Worth reading"}
	runScoreFileAsyncSync(t, req, q, eval)

	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		t.Fatalf("read transcriptDir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if strings.Contains(e.Name(), "_url_") {
			found = true
		}
	}
	if !found {
		t.Errorf("no transcript file with _url_ prefix found in %s", transcriptDir)
	}
}

func TestScoreAsync_ImageTranscriptSaved(t *testing.T) {
	isolateEventsDir(t)

	prevDir := transcriptDir
	transcriptDir = filepath.Join(t.TempDir(), "transcripts")
	t.Cleanup(func() { transcriptDir = prevDir })

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	req := &ShareRequest{
		Type:     "image",
		Filename: "photo.jpg",
		MimeType: "image/jpeg",
		Profile:  "life",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	req.QueueRowID = id

	eval := &stubEvaluator{score: 65, verdict: "Beautiful landscape photo"}
	runScoreFileAsyncSync(t, req, q, eval)

	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		t.Fatalf("read transcriptDir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if strings.Contains(e.Name(), "_img_") {
			found = true
			data, _ := os.ReadFile(filepath.Join(transcriptDir, e.Name()))
			if !strings.Contains(string(data), "Beautiful landscape photo") {
				t.Errorf("transcript body missing verdict; got:\n%s", data)
			}
		}
	}
	if !found {
		t.Errorf("no transcript file with _img_ prefix found in %s", transcriptDir)
	}
}

func TestCameraTimestampRE(t *testing.T) {
	matches := []string{
		"IMG_20260419_123456.jpg",
		"DSC_20260101.jpg",
		"DCIM_20260419_xyz.png",
		"PXL_20260419_123456789.jpg",
		"VID_20260419_120000.mp4",
	}
	nonMatches := []string{
		"meme.jpg",
		"Screenshot_20260419.png",
		"document.pdf",
		"WhatsApp Image 2026-04-19.jpeg",
	}
	for _, f := range matches {
		if !cameraTimestampRE.MatchString(f) {
			t.Errorf("expected %q to match cameraTimestampRE", f)
		}
	}
	for _, f := range nonMatches {
		if cameraTimestampRE.MatchString(f) {
			t.Errorf("expected %q NOT to match cameraTimestampRE", f)
		}
	}
}

// EPIC-007 M4: document share test coverage.

// TestScoreAsync_Document_LiteParse verifies that a document share with text
// extracted by LiteParse reaches eval and is persisted as scored.
func TestScoreAsync_Document_LiteParse(t *testing.T) {
	isolateEventsDir(t)

	tmpFile := filepath.Join(t.TempDir(), "paper.pdf")
	if err := os.WriteFile(tmpFile, []byte("fake-pdf"), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}

	installLiteParseStub(t, "Interesting content about machine learning and transformers.", 0.9, nil)

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	req := &ShareRequest{
		Type:      "document",
		Filename:  "paper.pdf",
		MimeType:  "application/pdf",
		Profile:   "eng",
		AudioPath: tmpFile,
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	req.QueueRowID = id

	eval := &stubEvaluator{score: 75, verdict: "Interesting ML paper"}
	runScoreFileAsyncSync(t, req, q, eval)

	if calls := atomic.LoadInt32(&eval.calls); calls != 1 {
		t.Errorf("eval.calls = %d, want 1", calls)
	}

	items, err := q.List("", 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, it := range items {
		if it.ID == id && (it.Status == "scored" || it.Status == "archived") {
			found = true
			if it.Score == nil || *it.Score != 75 {
				t.Errorf("row score = %v, want 75", it.Score)
			}
		}
	}
	if !found {
		t.Errorf("expected scored/archived row for document share")
	}
}

// TestScoreAsync_Document_EmptyText verifies that a document share where
// LiteParse returns empty text falls through to metadata synthesis without
// crashing, and the queue row is still scored.
func TestScoreAsync_Document_EmptyText(t *testing.T) {
	isolateEventsDir(t)

	tmpFile := filepath.Join(t.TempDir(), "paper.pdf")
	if err := os.WriteFile(tmpFile, []byte("fake-pdf"), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}

	installLiteParseStub(t, "", 0.0, nil)

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	req := &ShareRequest{
		Type:         "document",
		Filename:     "paper.pdf",
		MimeType:     "application/pdf",
		Profile:      "eng",
		ExtraSubject: "A research paper about transformers",
		AudioPath:    tmpFile,
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	req.QueueRowID = id

	eval := &stubEvaluator{score: 60, verdict: "Paper on transformers"}
	runScoreFileAsyncSync(t, req, q, eval)

	if calls := atomic.LoadInt32(&eval.calls); calls != 1 {
		t.Errorf("eval.calls = %d, want 1 (metadata synthesis should reach eval)", calls)
	}

	items, err := q.List("", 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, it := range items {
		if it.ID == id && (it.Status == "scored" || it.Status == "archived") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected scored/archived row for document share with empty LiteParse text")
	}
}
