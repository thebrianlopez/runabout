// youtube_normalize_test.go
// EPIC-006 M1: Contract tests CT-1 through CT-10 for normalizeYouTubeURL.
// All tests use httptest.Server mocks — no real network calls.
// Tests compile and fail on the stub implementation; M2 makes them pass.
package main

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// ─── slog capture helper ─────────────────────────────────────────────────────

// logCapture is a slog.Handler that records all log records for test assertion.
type logCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.records = append(c.records, r.Clone())
	c.mu.Unlock()
	return nil
}

func (c *logCapture) WithAttrs(_ []slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(_ string) slog.Handler      { return c }

// installLogCapture replaces the slog default handler for the test duration.
func installLogCapture(t *testing.T) *logCapture {
	t.Helper()
	lc := &logCapture{}
	orig := slog.Default()
	slog.SetDefault(slog.New(lc))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return lc
}

// countEvents counts log records whose message equals msg.
func (c *logCapture) countEvents(msg string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.records {
		if r.Message == msg {
			n++
		}
	}
	return n
}

// hasEventWithAttr reports whether any log record with message msg has an
// attribute with the given key and value (compared as strings).
func (c *logCapture) hasEventWithAttr(msg, key, val string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.records {
		if r.Message != msg {
			continue
		}
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key && a.Value.String() == val {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// eventHasKeys reports whether at least one log record with message msg
// carries all the specified attribute keys (value is not checked).
func (c *logCapture) eventHasKeys(msg string, keys ...string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.records {
		if r.Message != msg {
			continue
		}
		present := make(map[string]bool)
		r.Attrs(func(a slog.Attr) bool {
			present[a.Key] = true
			return true
		})
		all := true
		for _, k := range keys {
			if !present[k] {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// ─── TLS client helper ────────────────────────────────────────────────────────

// installInsecureTLSClient swaps normalizeHTTPClient for one that accepts
// self-signed TLS certificates (for httptest.NewTLSServer). Restored on cleanup.
func installInsecureTLSClient(t *testing.T) {
	t.Helper()
	orig := normalizeHTTPClient
	normalizeHTTPClient = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	t.Cleanup(func() { normalizeHTTPClient = orig })
}

// ─── CT-1: Google redirect resolves ──────────────────────────────────────────

// TestNormalizeYouTubeURL_CT1_GoogleRedirect verifies that a redirect wrapper
// pointing to a canonical YouTube URL is resolved to that canonical URL.
// EPIC-006 M1.
func TestNormalizeYouTubeURL_CT1_GoogleRedirect(t *testing.T) {
	canonical := "https://www.youtube.com/watch?v=abc1234"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, canonical, http.StatusFound)
	}))
	defer srv.Close()

	rawURL := srv.URL + "/url?sa=t&url=" + canonical
	got, err := normalizeYouTubeURL(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("CT-1: unexpected error: %v", err)
	}
	if got != canonical {
		t.Errorf("CT-1: got %q, want %q", got, canonical)
	}
}

// ─── CT-2: Canonical URL short-circuits ──────────────────────────────────────

// TestNormalizeYouTubeURL_CT2_CanonicalShortCircuit verifies that a canonical
// YouTube URL is returned unchanged and no HTTP request is made. EPIC-006 M1.
func TestNormalizeYouTubeURL_CT2_CanonicalShortCircuit(t *testing.T) {
	requested := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	canonical := "https://www.youtube.com/watch?v=shortcircuit"
	got, err := normalizeYouTubeURL(context.Background(), canonical)
	if err != nil {
		t.Fatalf("CT-2: unexpected error: %v", err)
	}
	if got != canonical {
		t.Errorf("CT-2: got %q, want %q", got, canonical)
	}
	if requested != 0 {
		t.Errorf("CT-2: expected 0 HTTP requests for canonical URL, got %d", requested)
	}
}

// ─── CT-3: youtu.be short-circuits ───────────────────────────────────────────

// TestNormalizeYouTubeURL_CT3_YoutuBeShortCircuit verifies that a youtu.be URL
// is already canonical and returned unchanged without any HTTP request.
// EPIC-006 M1.
func TestNormalizeYouTubeURL_CT3_YoutuBeShortCircuit(t *testing.T) {
	requested := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	youtubeURL := "https://youtu.be/dQw4w9WgXcQ"
	got, err := normalizeYouTubeURL(context.Background(), youtubeURL)
	if err != nil {
		t.Fatalf("CT-3: unexpected error: %v", err)
	}
	if got != youtubeURL {
		t.Errorf("CT-3: got %q, want %q", got, youtubeURL)
	}
	if requested != 0 {
		t.Errorf("CT-3: expected 0 HTTP requests for canonical youtu.be URL, got %d", requested)
	}
}

// ─── CT-4: Timeout falls back ────────────────────────────────────────────────

// TestNormalizeYouTubeURL_CT4_TimeoutFallback verifies that when the HTTP server
// hangs and the context deadline is exceeded, normalizeYouTubeURL returns the
// original URL without error. EPIC-006 M1.
func TestNormalizeYouTubeURL_CT4_TimeoutFallback(t *testing.T) {
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock
		w.WriteHeader(http.StatusOK)
	}))
	defer func() {
		close(unblock)
		srv.Close()
	}()

	rawURL := srv.URL + "/url?sa=t&url=https://www.youtube.com/watch?v=X"
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	got, err := normalizeYouTubeURL(ctx, rawURL)
	if err != nil {
		t.Fatalf("CT-4: unexpected error: %v", err)
	}
	if got != rawURL {
		t.Errorf("CT-4: got %q, want original %q on timeout", got, rawURL)
	}
}

// ─── CT-5: Idempotency ───────────────────────────────────────────────────────

// TestNormalizeYouTubeURL_CT5_Idempotent verifies that applying normalizeYouTubeURL
// twice yields the same result as applying it once. EPIC-006 M1.
func TestNormalizeYouTubeURL_CT5_Idempotent(t *testing.T) {
	canonical := "https://www.youtube.com/watch?v=idempotent"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, canonical, http.StatusFound)
	}))
	defer srv.Close()

	rawURL := srv.URL + "/url?src=idempotent"
	ctx := context.Background()

	first, err := normalizeYouTubeURL(ctx, rawURL)
	if err != nil {
		t.Fatalf("CT-5: first call error: %v", err)
	}
	second, err := normalizeYouTubeURL(ctx, first)
	if err != nil {
		t.Fatalf("CT-5: second call error: %v", err)
	}
	if first != second {
		t.Errorf("CT-5: idempotency violated: first=%q, second=%q", first, second)
	}
}

// ─── CT-6: Non-YouTube redirect preserved ────────────────────────────────────

// TestNormalizeYouTubeURL_CT6_NonYouTubeRedirectPreserved verifies that a
// redirect chain that never resolves to a YouTube URL returns the original URL
// unchanged. Two mock servers are used so no real external HTTP request is made.
// EPIC-006 M1.
func TestNormalizeYouTubeURL_CT6_NonYouTubeRedirectPreserved(t *testing.T) {
	// dest: non-YouTube endpoint that returns 200 (chain ends here, not YouTube).
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer dest.Close()

	// redir: redirect to dest (non-YouTube chain).
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL+"/article/123", http.StatusFound)
	}))
	defer redir.Close()

	rawURL := redir.URL + "/url?sa=t"
	got, err := normalizeYouTubeURL(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("CT-6: unexpected error: %v", err)
	}
	if got != rawURL {
		t.Errorf("CT-6: got %q, want original %q (non-YouTube redirect must not transform)", got, rawURL)
	}
}

// ─── CT-7: Multi-hop redirect ────────────────────────────────────────────────

// TestNormalizeYouTubeURL_CT7_MultiHopRedirect verifies that a 2-hop redirect
// chain (mock-hop1 → mock-hop2 → youtube) resolves to the canonical YouTube URL.
// EPIC-006 M1.
func TestNormalizeYouTubeURL_CT7_MultiHopRedirect(t *testing.T) {
	canonical := "https://www.youtube.com/watch?v=multihop"

	hop2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, canonical, http.StatusFound)
	}))
	defer hop2.Close()

	hop1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, hop2.URL+"/redirect", http.StatusMovedPermanently)
	}))
	defer hop1.Close()

	rawURL := hop1.URL + "/short/abc"
	got, err := normalizeYouTubeURL(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("CT-7: unexpected error: %v", err)
	}
	if got != canonical {
		t.Errorf("CT-7: got %q, want %q (multi-hop failed)", got, canonical)
	}
}

// ─── CT-8: Max redirects exceeded ────────────────────────────────────────────

// TestNormalizeYouTubeURL_CT8_MaxRedirects verifies that when a server returns
// more than maxNormalizeRedirects consecutive redirects, normalizeYouTubeURL
// returns the original URL and emits yt_url_normalize_fallback with
// reason=max_redirects. EPIC-006 M1.
func TestNormalizeYouTubeURL_CT8_MaxRedirects(t *testing.T) {
	lc := installLogCapture(t)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
	}))
	defer srv.Close()

	rawURL := srv.URL + "/loop"
	got, err := normalizeYouTubeURL(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("CT-8: unexpected error: %v", err)
	}
	if got != rawURL {
		t.Errorf("CT-8: got %q, want original %q on max redirects", got, rawURL)
	}
	if !lc.hasEventWithAttr("yt_url_normalize_fallback", "reason", "max_redirects") {
		t.Error("CT-8: expected yt_url_normalize_fallback event with reason=max_redirects")
	}
}

// ─── CT-9: Non-HTTPS bail-out ────────────────────────────────────────────────

// TestNormalizeYouTubeURL_CT9_NonHTTPSBailOut verifies that when a redirect
// chain downgrades from HTTPS to HTTP, normalizeYouTubeURL stops and returns
// the original URL, emitting yt_url_normalize_fallback with reason=non_https_hop.
// EPIC-006 M1.
func TestNormalizeYouTubeURL_CT9_NonHTTPSBailOut(t *testing.T) {
	lc := installLogCapture(t)
	installInsecureTLSClient(t)

	// HTTPS server (TLS) that redirects to a plain-HTTP target.
	httpTarget := "http://plain.example.com/page"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpTarget, http.StatusFound)
	}))
	defer srv.Close()

	rawURL := srv.URL + "/url?sa=t"
	got, err := normalizeYouTubeURL(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("CT-9: unexpected error: %v", err)
	}
	if got != rawURL {
		t.Errorf("CT-9: got %q, want original %q on non-HTTPS bail-out", got, rawURL)
	}
	if !lc.hasEventWithAttr("yt_url_normalize_fallback", "reason", "non_https_hop") {
		t.Error("CT-9: expected yt_url_normalize_fallback event with reason=non_https_hop")
	}
}

// ─── CT-10: Per-hop logging ───────────────────────────────────────────────────

// ─── M4 behavioral tests: event logging ──────────────────────────────────────

// TestNormalizeYouTubeURL_BT3_NormalizedEventEmitted verifies that a successful
// normalization emits a yt_url_normalized INFO event with original_url and
// canonical_url attributes. EPIC-006 M4.
func TestNormalizeYouTubeURL_BT3_NormalizedEventEmitted(t *testing.T) {
	lc := installLogCapture(t)

	canonical := "https://www.youtube.com/watch?v=bt3event"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, canonical, http.StatusFound)
	}))
	defer srv.Close()

	rawURL := srv.URL + "/url?bt3=1"
	_, err := normalizeYouTubeURL(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("BT-3: unexpected error: %v", err)
	}
	if !lc.hasEventWithAttr("yt_url_normalized", "original_url", rawURL) {
		t.Errorf("BT-3: yt_url_normalized event missing original_url=%q", rawURL)
	}
	if !lc.hasEventWithAttr("yt_url_normalized", "canonical_url", canonical) {
		t.Errorf("BT-3: yt_url_normalized event missing canonical_url=%q", canonical)
	}
}

// TestNormalizeYouTubeURL_BT4_TimeoutFallbackEvent verifies that a context
// timeout emits a yt_url_normalize_fallback WARN event with reason=timeout.
// EPIC-006 M4.
func TestNormalizeYouTubeURL_BT4_TimeoutFallbackEvent(t *testing.T) {
	lc := installLogCapture(t)

	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock
		w.WriteHeader(http.StatusOK)
	}))
	defer func() {
		close(unblock)
		srv.Close()
	}()

	rawURL := srv.URL + "/url?bt4=1"
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := normalizeYouTubeURL(ctx, rawURL)
	if err != nil {
		t.Fatalf("BT-4: unexpected error: %v", err)
	}
	if !lc.hasEventWithAttr("yt_url_normalize_fallback", "reason", "timeout") {
		t.Error("BT-4: expected yt_url_normalize_fallback event with reason=timeout")
	}
}

// TestNormalizeYouTubeURL_CT10_PerHopLogging verifies that each redirect hop
// emits a yt_url_normalize_hop event with hop, from, to, and status_code fields.
// A 2-hop chain must produce exactly 2 events. EPIC-006 M1.
func TestNormalizeYouTubeURL_CT10_PerHopLogging(t *testing.T) {
	lc := installLogCapture(t)

	canonical := "https://www.youtube.com/watch?v=perhop"

	hop2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, canonical, http.StatusFound)
	}))
	defer hop2.Close()

	hop1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, hop2.URL+"/final", http.StatusFound)
	}))
	defer hop1.Close()

	rawURL := hop1.URL + "/start"
	got, err := normalizeYouTubeURL(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("CT-10: unexpected error: %v", err)
	}
	if got != canonical {
		t.Errorf("CT-10: got %q, want %q", got, canonical)
	}

	hopCount := lc.countEvents("yt_url_normalize_hop")
	if hopCount != 2 {
		t.Errorf("CT-10: got %d yt_url_normalize_hop events, want 2", hopCount)
	}
	if !lc.eventHasKeys("yt_url_normalize_hop", "hop", "from", "to", "status_code") {
		t.Error("CT-10: yt_url_normalize_hop event missing required fields (hop, from, to, status_code)")
	}
}
