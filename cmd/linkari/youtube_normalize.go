// youtube_normalize.go
// EPIC-006 M2: isCanonicalYouTubeURL + normalizeYouTubeURL — manual HTTP HEAD
// redirect walker that resolves Google/t.co wrapper URLs to canonical YouTube form.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// execNormalizeURL is the test seam for URL normalization. Replace in tests to
// inject a custom normalizer without spawning HTTP requests. EPIC-006 M3.
var execNormalizeURL = normalizeYouTubeURL

// maxNormalizeRedirects is the maximum number of redirect hops normalizeYouTubeURL
// will follow before giving up and returning the original URL unchanged.
const maxNormalizeRedirects = 10

// normalizeHTTPClient is the HTTP client used by normalizeYouTubeURL for manual
// redirect walking. CheckRedirect is set to noFollow so each hop is inspected and
// logged individually. Replaced in tests to accept self-signed TLS certificates
// from httptest.NewTLSServer.
var normalizeHTTPClient = &http.Client{
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// isCanonicalYouTubeURL reports whether rawURL's host is youtube.com (or a
// subdomain) or youtu.be — a direct YouTube domain, not a redirect wrapper that
// merely contains a YouTube URL in a query parameter.
func isCanonicalYouTubeURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	// Strip port if present.
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	return host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") ||
		host == "youtu.be"
}

// normalizeYouTubeURL resolves redirect wrappers (e.g. google.com/url?url=...)
// to canonical YouTube form via a manual HTTP HEAD redirect walk. Short-circuits
// immediately when isCanonicalYouTubeURL returns true (no HTTP request made).
//
// Per-hop: emits yt_url_normalize_hop DEBUG with hop, from, to, status_code, duration_ms.
// On success: emits yt_url_normalized INFO with original_url, canonical_url, hops, total_duration_ms.
// On noop: emits yt_url_normalize_noop DEBUG.
// On degradation: emits yt_url_normalize_fallback WARN with reason and returns rawURL unchanged.
//
// Returns the original URL unchanged on timeout, network error, max redirects,
// non-HTTPS downgrade, or if no hop resolves to a canonical YouTube URL.
// Only returns a non-nil error for malformed input URL parse failures.
func normalizeYouTubeURL(ctx context.Context, rawURL string) (string, error) {
	// Short-circuit: already canonical, no HTTP call needed.
	if isCanonicalYouTubeURL(rawURL) {
		slog.Debug("yt_url_normalize_noop",
			"event_type", "yt_url_normalize_noop",
			"url", rawURL,
		)
		return rawURL, nil
	}

	// Per-call timeout: 5s or parent deadline, whichever is shorter.
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	start := time.Now()
	currentURL := rawURL
	maxReached := true

	for hop := 1; hop <= maxNormalizeRedirects; hop++ {
		hopStart := time.Now()

		req, reqErr := http.NewRequestWithContext(callCtx, http.MethodHead, currentURL, nil)
		if reqErr != nil {
			slog.Warn("yt_url_normalize_fallback",
				"event_type", "yt_url_normalize_fallback",
				"reason", "network",
				"hops_completed", hop-1,
				"error", reqErr.Error(),
			)
			return rawURL, nil
		}

		resp, doErr := normalizeHTTPClient.Do(req)
		if doErr != nil {
			reason := "network"
			if errors.Is(callCtx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason = "timeout"
			}
			slog.Warn("yt_url_normalize_fallback",
				"event_type", "yt_url_normalize_fallback",
				"reason", reason,
				"hops_completed", hop-1,
				"error", doErr.Error(),
			)
			return rawURL, nil
		}
		resp.Body.Close()

		hopDuration := time.Since(hopStart).Milliseconds()
		location := resp.Header.Get("Location")

		slog.Debug("yt_url_normalize_hop",
			"event_type", "yt_url_normalize_hop",
			"hop", hop,
			"from", currentURL,
			"to", location,
			"status_code", resp.StatusCode,
			"duration_ms", hopDuration,
		)

		// Not a redirect — chain ended without finding YouTube.
		if location == "" || resp.StatusCode < 300 || resp.StatusCode >= 400 {
			maxReached = false
			break
		}

		// Resolve Location to absolute URL.
		base, _ := url.Parse(currentURL)
		loc, parseErr := url.Parse(location)
		if parseErr != nil {
			slog.Warn("yt_url_normalize_fallback",
				"event_type", "yt_url_normalize_fallback",
				"reason", "network",
				"hops_completed", hop,
				"error", parseErr.Error(),
			)
			return rawURL, nil
		}
		nextURL := base.ResolveReference(loc).String()

		// Non-HTTPS bail-out: downgrade from HTTPS to HTTP is not permitted.
		if strings.HasPrefix(currentURL, "https://") && strings.HasPrefix(nextURL, "http://") {
			slog.Warn("yt_url_normalize_fallback",
				"event_type", "yt_url_normalize_fallback",
				"reason", "non_https_hop",
				"hops_completed", hop,
				"hop_from", currentURL,
				"hop_to", nextURL,
			)
			return rawURL, nil
		}

		// Chain resolved to canonical YouTube — success.
		if isCanonicalYouTubeURL(nextURL) {
			totalDuration := time.Since(start).Milliseconds()
			slog.Info("yt_url_normalized",
				"event_type", "yt_url_normalized",
				"original_url", rawURL,
				"canonical_url", nextURL,
				"hops", hop,
				"total_duration_ms", totalDuration,
			)
			return nextURL, nil
		}

		currentURL = nextURL
	}

	if maxReached {
		slog.Warn("yt_url_normalize_fallback",
			"event_type", "yt_url_normalize_fallback",
			"reason", "max_redirects",
			"hops_completed", maxNormalizeRedirects,
		)
	}
	return rawURL, nil
}
