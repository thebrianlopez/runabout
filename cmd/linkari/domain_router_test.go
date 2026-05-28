package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mockDomainClient is a DomainClient implementation for use in contract tests.
type mockDomainClient struct {
	fetchFn func(ctx context.Context, u *url.URL) (string, ContentType, error)
	called  atomic.Bool
}

func (m *mockDomainClient) Fetch(ctx context.Context, u *url.URL) (string, ContentType, error) {
	m.called.Store(true)
	if m.fetchFn != nil {
		return m.fetchFn(ctx, u)
	}
	return "mock-content", ContentTypePlain, nil
}

func jinaOK(_ context.Context, _ string) (string, error) {
	return "jina-content", nil
}

// CT-1: YouTube bypass — youtube.com never intercepted.
func TestDomain_CT1_YouTubeBypass(t *testing.T) {
	mock := &mockDomainClient{}
	clients := map[string]DomainClient{"youtube.com": mock}
	r := NewDomainRouter(clients, jinaOK)

	content, _, err := r.FetchWithFallback(context.Background(), "https://youtube.com/watch?v=X")
	if err != nil {
		t.Fatalf("FetchWithFallback returned error: %v", err)
	}
	if mock.called.Load() {
		t.Fatal("CT-1: registered client was called for a YouTube URL; jinaFetch should have been used")
	}
	if content != "jina-content" {
		t.Fatalf("CT-1: expected jina-content, got %q", content)
	}
}

// CT-2: YouTube bypass — youtu.be never intercepted.
func TestDomain_CT2_YoutuBeBypass(t *testing.T) {
	mock := &mockDomainClient{}
	r := NewDomainRouter(map[string]DomainClient{"youtu.be": mock}, jinaOK)

	_, _, err := r.FetchWithFallback(context.Background(), "https://youtu.be/X")
	if err != nil {
		t.Fatalf("CT-2: unexpected error: %v", err)
	}
	if mock.called.Load() {
		t.Fatal("CT-2: registered client was called for youtu.be URL")
	}
}

// CT-3: Unknown domain → Jina, ContentTypePlain returned.
func TestDomain_CT3_UnknownDomainJina(t *testing.T) {
	r := NewDomainRouter(nil, jinaOK)

	content, ct, err := r.FetchWithFallback(context.Background(), "https://example.com/page")
	if err != nil {
		t.Fatalf("CT-3: unexpected error: %v", err)
	}
	if content != "jina-content" {
		t.Fatalf("CT-3: expected jina-content, got %q", content)
	}
	if ct != ContentTypePlain {
		t.Fatalf("CT-3: expected ContentTypePlain, got %v", ct)
	}
}

// CT-4: Known domain → registered client called (not jinaFetch).
func TestDomain_CT4_KnownDomainClientCalled(t *testing.T) {
	jinaCalled := false
	jinaFn := func(_ context.Context, _ string) (string, error) {
		jinaCalled = true
		return "jina-content", nil
	}

	mock := &mockDomainClient{fetchFn: func(_ context.Context, _ *url.URL) (string, ContentType, error) {
		return "github-content", ContentTypeMarkdown, nil
	}}
	r := NewDomainRouter(map[string]DomainClient{"github.com": mock}, jinaFn)

	content, ct, err := r.FetchWithFallback(context.Background(), "https://github.com/owner/repo")
	if err != nil {
		t.Fatalf("CT-4: unexpected error: %v", err)
	}
	if !mock.called.Load() {
		t.Fatal("CT-4: registered client was not called for github.com URL")
	}
	if jinaCalled {
		t.Fatal("CT-4: jinaFetch was called; should only call registered client")
	}
	if content != "github-content" {
		t.Fatalf("CT-4: expected github-content, got %q", content)
	}
	if ct != ContentTypeMarkdown {
		t.Fatalf("CT-4: expected ContentTypeMarkdown, got %v", ct)
	}
}

// CT-5: Auth failure → Jina fallback, no request dropped.
func TestDomain_CT5_AuthFailureFallback(t *testing.T) {
	mock := &mockDomainClient{fetchFn: func(_ context.Context, _ *url.URL) (string, ContentType, error) {
		return "", 0, errors.New("auth_error: 401 Unauthorized")
	}}
	r := NewDomainRouter(map[string]DomainClient{"github.com": mock}, jinaOK)

	content, _, err := r.FetchWithFallback(context.Background(), "https://github.com/owner/repo")
	if err != nil {
		t.Fatalf("CT-5: expected no error after fallback, got: %v", err)
	}
	if content != "jina-content" {
		t.Fatalf("CT-5: expected jina-content after fallback, got %q", content)
	}
}

// CT-6: FetchWithFallback never returns error when Jina available.
func TestDomain_CT6_NeverErrorWhenJinaAvailable(t *testing.T) {
	urls := []string{
		"https://youtube.com/watch?v=X",
		"https://youtu.be/abc",
		"https://unknown-domain.com/page",
		"https://github.com/owner/repo",
	}

	mock := &mockDomainClient{fetchFn: func(_ context.Context, _ *url.URL) (string, ContentType, error) {
		return "", 0, errors.New("client always fails")
	}}
	r := NewDomainRouter(map[string]DomainClient{"github.com": mock}, jinaOK)

	for _, u := range urls {
		_, _, err := r.FetchWithFallback(context.Background(), u)
		if err != nil {
			t.Errorf("CT-6: expected nil error for %q, got: %v", u, err)
		}
	}
}

// CT-7: Jina fallback returns ContentTypePlain.
func TestDomain_CT7_JinaFallbackContentTypePlain(t *testing.T) {
	mock := &mockDomainClient{fetchFn: func(_ context.Context, _ *url.URL) (string, ContentType, error) {
		return "", 0, errors.New("client error")
	}}
	r := NewDomainRouter(map[string]DomainClient{"github.com": mock}, jinaOK)

	_, ct, err := r.FetchWithFallback(context.Background(), "https://github.com/owner/repo")
	if err != nil {
		t.Fatalf("CT-7: unexpected error: %v", err)
	}
	if ct != ContentTypePlain {
		t.Fatalf("CT-7: expected ContentTypePlain after Jina fallback, got %v", ct)
	}
}

// CT-8: Timeout respected — domain client killed at configured deadline.
// Uses a 50ms router timeout to keep the suite under 500ms while still
// verifying cancellation behaviour (the production default is 2s).
func TestDomain_CT8_ClientTimeoutRespected(t *testing.T) {
	const testTimeout = 50 * time.Millisecond

	mock := &mockDomainClient{fetchFn: func(ctx context.Context, _ *url.URL) (string, ContentType, error) {
		select {
		case <-time.After(5 * time.Second):
			return "slow-content", ContentTypePlain, nil
		case <-ctx.Done():
			return "", 0, ctx.Err()
		}
	}}
	r := NewDomainRouter(map[string]DomainClient{"github.com": mock}, jinaOK)
	r.timeout = testTimeout

	start := time.Now()
	content, _, err := r.FetchWithFallback(context.Background(), "https://github.com/owner/repo")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("CT-8: expected no error (Jina fallback), got: %v", err)
	}
	if elapsed > testTimeout+100*time.Millisecond {
		t.Fatalf("CT-8: client was not cancelled within deadline+100ms; elapsed=%v", elapsed)
	}
	if content != "jina-content" {
		t.Fatalf("CT-8: expected jina-content after timeout+fallback, got %q", content)
	}
}

// CT-9: domain_router_auth_error event emitted on client failure.
func TestDomain_CT9_AuthErrorEventEmitted(t *testing.T) {
	mock := &mockDomainClient{fetchFn: func(_ context.Context, _ *url.URL) (string, ContentType, error) {
		return "", 0, errors.New("auth failure")
	}}

	var emitted []string
	r := NewDomainRouter(map[string]DomainClient{"github.com": mock}, jinaOK)
	r.onEvent = func(eventType string, _ map[string]interface{}) {
		emitted = append(emitted, eventType)
	}

	_, _, err := r.FetchWithFallback(context.Background(), "https://github.com/owner/repo")
	if err != nil {
		t.Fatalf("CT-9: unexpected error: %v", err)
	}
	found := false
	for _, et := range emitted {
		if et == "domain_router_auth_error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("CT-9: domain_router_auth_error not emitted; got: %v", emitted)
	}
}

// CT-10: domain_router_fetch_end emitted on every call with correct fields.
func TestDomain_CT10_FetchEndEventEmitted(t *testing.T) {
	var events []map[string]interface{}
	r := NewDomainRouter(nil, jinaOK)
	r.onEvent = func(eventType string, metadata map[string]interface{}) {
		if eventType == "domain_router_fetch_end" {
			events = append(events, metadata)
		}
	}

	_, _, _ = r.FetchWithFallback(context.Background(), "https://example.com/page")
	if len(events) == 0 {
		t.Fatal("CT-10: domain_router_fetch_end not emitted")
	}
	ev := events[0]
	for _, field := range []string{"url", "domain", "client_used", "fallback_used", "latency_ms", "content_type"} {
		if _, ok := ev[field]; !ok {
			t.Errorf("CT-10: missing field %q in domain_router_fetch_end event", field)
		}
	}
	// URL must be scheme://host only — no path
	if u, ok := ev["url"].(string); ok {
		if strings.Contains(u, "/page") {
			t.Errorf("CT-10: url field must not include path; got %q", u)
		}
	}
}

// CT-11: www. prefix stripped for hostname matching.
func TestDomain_CT11_WwwPrefixStripped(t *testing.T) {
	jinaCalled := false
	jinaFn := func(_ context.Context, _ string) (string, error) {
		jinaCalled = true
		return "jina-content", nil
	}

	mock := &mockDomainClient{fetchFn: func(_ context.Context, _ *url.URL) (string, ContentType, error) {
		return "github-content", ContentTypeMarkdown, nil
	}}
	r := NewDomainRouter(map[string]DomainClient{"github.com": mock}, jinaFn)

	_, _, err := r.FetchWithFallback(context.Background(), "https://www.github.com/owner/repo")
	if err != nil {
		t.Fatalf("CT-11: unexpected error: %v", err)
	}
	if !mock.called.Load() {
		t.Fatal("CT-11: registered client not called for www.github.com URL — www. stripping failed")
	}
	if jinaCalled {
		t.Fatal("CT-11: jinaFetch called instead of registered client")
	}
}

// CT-12: YouTube bypass covers www.youtube.com and m.youtube.com.
func TestDomain_CT12_YouTubeVariantsAllBypassed(t *testing.T) {
	youtubeVariants := []string{
		"https://www.youtube.com/watch?v=X",
		"https://m.youtube.com/watch?v=X",
	}
	mock := &mockDomainClient{}
	r := NewDomainRouter(map[string]DomainClient{
		"youtube.com":     mock,
		"www.youtube.com": mock,
		"m.youtube.com":   mock,
	}, jinaOK)

	for _, u := range youtubeVariants {
		mock.called.Store(false)
		_, _, err := r.FetchWithFallback(context.Background(), u)
		if err != nil {
			t.Errorf("CT-12: unexpected error for %q: %v", u, err)
		}
		if mock.called.Load() {
			t.Errorf("CT-12: registered client was called for YouTube variant %q", u)
		}
	}
}

// BT-1: Slow domain client timeout does not block scoreAsync beyond 2.5s.
// Uses a short router timeout so the test itself runs fast (matches CT-8 pattern).
func TestDomain_BT1_SlowClientDoesNotBlock(t *testing.T) {
	const routerTimeout = 50 * time.Millisecond

	slowClient := &mockDomainClient{fetchFn: func(ctx context.Context, _ *url.URL) (string, ContentType, error) {
		select {
		case <-time.After(5 * time.Second):
			return "slow", ContentTypePlain, nil
		case <-ctx.Done():
			return "", 0, ctx.Err()
		}
	}}
	r := NewDomainRouter(map[string]DomainClient{"github.com": slowClient}, jinaOK)
	r.timeout = routerTimeout

	start := time.Now()
	_, _, err := r.FetchWithFallback(context.Background(), "https://github.com/owner/repo")
	if err != nil {
		t.Fatalf("BT-1: unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > routerTimeout+200*time.Millisecond {
		t.Fatalf("BT-1: fetch blocked longer than timeout+200ms; elapsed=%v", elapsed)
	}
}

// BT-2: Multiple registered clients route correctly.
func TestDomain_BT2_MultipleClientsCorrectRouting(t *testing.T) {
	githubCalled := false
	atlasCalled := false

	githubClient := &mockDomainClient{fetchFn: func(_ context.Context, _ *url.URL) (string, ContentType, error) {
		githubCalled = true
		return "github-content", ContentTypeMarkdown, nil
	}}
	atlasClient := &mockDomainClient{fetchFn: func(_ context.Context, _ *url.URL) (string, ContentType, error) {
		atlasCalled = true
		return "atlas-content", ContentTypeADF, nil
	}}

	r := NewDomainRouter(map[string]DomainClient{
		"github.com":    githubClient,
		"atlassian.net": atlasClient,
	}, jinaOK)

	content, ct, err := r.FetchWithFallback(context.Background(), "https://github.com/owner/repo")
	if err != nil || !githubCalled || content != "github-content" || ct != ContentTypeMarkdown {
		t.Errorf("BT-2: github routing failed: content=%q, ct=%v, err=%v, called=%v", content, ct, err, githubCalled)
	}

	content, ct, err = r.FetchWithFallback(context.Background(), "https://atlassian.net/wiki/spaces/X")
	if err != nil || !atlasCalled || content != "atlas-content" || ct != ContentTypeADF {
		t.Errorf("BT-2: atlassian routing failed: content=%q, ct=%v, err=%v, called=%v", content, ct, err, atlasCalled)
	}
}

// BT-3: NewDomainRouter with nil jinaFetch panics.
func TestDomain_BT3_NilJinaFetchPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("BT-3: expected panic for nil jinaFetch, got none")
		}
	}()
	NewDomainRouter(nil, nil)
}

// BT-4: Malformed URL routes to Jina.
func TestDomain_BT4_MalformedURLRoutesToJina(t *testing.T) {
	jinaCalled := false
	jinaFn := func(_ context.Context, _ string) (string, error) {
		jinaCalled = true
		return "jina-content", nil
	}
	mock := &mockDomainClient{}
	r := NewDomainRouter(map[string]DomainClient{"github.com": mock}, jinaFn)

	_, _, err := r.FetchWithFallback(context.Background(), "not-a-url")
	if err != nil {
		t.Fatalf("BT-4: unexpected error: %v", err)
	}
	if !jinaCalled {
		t.Fatal("BT-4: jinaFetch not called for malformed URL")
	}
	if mock.called.Load() {
		t.Fatal("BT-4: registered client called for malformed URL")
	}
}

// BT-5: RegisterClient at runtime is visible to subsequent FetchWithFallback calls.
func TestDomain_BT5_RegisterClientVisibleAtRuntime(t *testing.T) {
	jinaCalled := false
	jinaFn := func(_ context.Context, _ string) (string, error) {
		jinaCalled = true
		return "jina-content", nil
	}
	mock := &mockDomainClient{fetchFn: func(_ context.Context, _ *url.URL) (string, ContentType, error) {
		return "dynamic-content", ContentTypeMarkdown, nil
	}}

	r := NewDomainRouter(nil, jinaFn)

	// Before registration: should use Jina.
	r.FetchWithFallback(context.Background(), "https://github.com/owner/repo") //nolint
	if !jinaCalled {
		t.Fatal("BT-5: expected Jina before registration")
	}

	// Register at runtime.
	r.RegisterClient("github.com", mock)
	jinaCalled = false

	// After registration: should use the mock.
	content, _, err := r.FetchWithFallback(context.Background(), "https://github.com/owner/repo")
	if err != nil {
		t.Fatalf("BT-5: unexpected error: %v", err)
	}
	if jinaCalled {
		t.Fatal("BT-5: jinaFetch was called after RegisterClient")
	}
	if !mock.called.Load() {
		t.Fatal("BT-5: registered client was not called after RegisterClient")
	}
	if content != "dynamic-content" {
		t.Fatalf("BT-5: expected dynamic-content, got %q", content)
	}
}

// RG-1: YouTube scoring must not be intercepted by domain router.
// Source: FDD F1 contract + POMO youtube-subscription-scoring.
func TestDomain_RG1_YouTubeNeverIntercepted(t *testing.T) {
	if !IsYouTube("https://youtube.com/watch?v=X") {
		t.Fatal("RG-1: IsYouTube returned false for youtube.com — exclusion broken")
	}

	youtubeCalled := false
	youtubeClient := &mockDomainClient{fetchFn: func(_ context.Context, _ *url.URL) (string, ContentType, error) {
		youtubeCalled = true
		return "youtube-native", ContentTypePlain, nil
	}}
	r := NewDomainRouter(map[string]DomainClient{
		"youtube.com": youtubeClient,
		"youtu.be":    youtubeClient,
	}, jinaOK)

	for _, u := range []string{
		"https://youtube.com/watch?v=X",
		"https://youtu.be/X",
		"https://www.youtube.com/watch?v=X",
		"https://m.youtube.com/watch?v=X",
	} {
		youtubeCalled = false
		_, _, err := r.FetchWithFallback(context.Background(), u)
		if err != nil {
			t.Errorf("RG-1: unexpected error for %q: %v", u, err)
		}
		if youtubeCalled {
			t.Errorf("RG-1: YouTube client was called for %q — intercept invariant violated", u)
		}
	}
}

// CT-13: domain_router_fetch_start is emitted before domain_router_fetch_end on every call.
// Fields: domain (hostname), client_registered (bool), url (raw input URL).
func TestDomain_CT13_FetchStartEventEmitted(t *testing.T) {
	type emittedEvent struct {
		eventType string
		metadata  map[string]interface{}
	}

	cases := []struct {
		name             string
		url              string
		clientRegistered bool
	}{
		{"registered client", "https://github.com/owner/repo", true},
		{"unknown domain (Jina)", "https://example.com/page", false},
		{"YouTube bypass", "https://youtube.com/watch?v=X", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []emittedEvent
			mock := &mockDomainClient{fetchFn: func(_ context.Context, _ *url.URL) (string, ContentType, error) {
				return "client-content", ContentTypeMarkdown, nil
			}}
			clients := map[string]DomainClient{"github.com": mock}
			r := NewDomainRouter(clients, jinaOK)
			r.onEvent = func(eventType string, metadata map[string]interface{}) {
				events = append(events, emittedEvent{eventType, metadata})
			}

			_, _, err := r.FetchWithFallback(context.Background(), tc.url)
			if err != nil {
				t.Fatalf("CT-13 %s: unexpected error: %v", tc.name, err)
			}

			// Verify fetch_start was emitted.
			startIdx := -1
			endIdx := -1
			for i, ev := range events {
				switch ev.eventType {
				case "domain_router_fetch_start":
					startIdx = i
				case "domain_router_fetch_end":
					endIdx = i
				}
			}
			if startIdx == -1 {
				t.Fatalf("CT-13 %s: domain_router_fetch_start not emitted; got events: %v",
					tc.name, func() []string {
						var types []string
						for _, e := range events {
							types = append(types, e.eventType)
						}
						return types
					}())
			}
			if endIdx == -1 {
				t.Fatalf("CT-13 %s: domain_router_fetch_end not emitted", tc.name)
			}
			if startIdx >= endIdx {
				t.Errorf("CT-13 %s: fetch_start (idx=%d) must appear before fetch_end (idx=%d)", tc.name, startIdx, endIdx)
			}

			// Verify required fields on fetch_start.
			startMeta := events[startIdx].metadata
			for _, field := range []string{"domain", "client_registered", "url"} {
				if _, ok := startMeta[field]; !ok {
					t.Errorf("CT-13 %s: missing field %q in domain_router_fetch_start event", tc.name, field)
				}
			}
			if got, ok := startMeta["client_registered"].(bool); ok {
				if got != tc.clientRegistered {
					t.Errorf("CT-13 %s: client_registered=%v, want %v", tc.name, got, tc.clientRegistered)
				}
			} else {
				t.Errorf("CT-13 %s: client_registered field is not a bool", tc.name)
			}
			// url field must be present and match the raw input
			if gotURL, ok := startMeta["url"].(string); !ok || gotURL == "" {
				t.Errorf("CT-13 %s: url field missing or empty in fetch_start event", tc.name)
			}
		})
	}
}

// RG-2: Domain router must not silently drop requests on client failure.
// Source: FDD F1 fallback invariant.
func TestDomain_RG2_ClientFailureFallsBackToJina(t *testing.T) {
	jinaContent := "jina-fallback-content"
	jinaFn := func(_ context.Context, _ string) (string, error) {
		return jinaContent, nil
	}

	alwaysFail := &mockDomainClient{fetchFn: func(_ context.Context, _ *url.URL) (string, ContentType, error) {
		return "", 0, errors.New("simulated client failure")
	}}
	r := NewDomainRouter(map[string]DomainClient{"github.com": alwaysFail}, jinaFn)

	content, _, err := r.FetchWithFallback(context.Background(), "https://github.com/owner/repo")
	if err != nil {
		t.Fatalf("RG-2: expected no error after fallback, got: %v", err)
	}
	if content == "" {
		t.Fatal("RG-2: FetchWithFallback returned empty content after client failure — request silently dropped")
	}
	if content != jinaContent {
		t.Fatalf("RG-2: expected Jina content %q, got %q", jinaContent, content)
	}
}
