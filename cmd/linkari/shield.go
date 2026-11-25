package main

import (
	"log/slog"
	"net/http"
	"regexp"
	"sync"
)

// clientHeaderRegex validates the X-Linkari-Client header format:
//
//	<client>/<version>            e.g. "android/1.2.3"
//	<client>/<version>/<flavor>   e.g. "android/1.2.3/cloud"
//
// Segments: alphanumeric, hyphens, underscores; version may include dots.
var clientHeaderRegex = regexp.MustCompile(`^[\w\-]+/[\w.\-]+(/[\w\-]+)?$`)

// Shield validates X-Linkari-Client headers on the serving mux (Mux).
// Two modes:
//   - "log" (default): invalid/missing headers emit a debug log but pass through.
//   - "enforce": invalid/missing headers receive 403 Forbidden.
//
// OPTIONS requests are always exempt (CORS preflight — permanent invariant, G-14).
type Shield struct {
	mu   sync.RWMutex
	mode string // "log" or "enforce"
}

// NewShield creates a Shield with the given mode. Empty mode defaults to "log".
func NewShield(mode string) *Shield {
	if mode == "" {
		mode = "log"
	}
	return &Shield{mode: mode}
}

// Reload updates the shield mode at runtime (SIGHUP).
func (s *Shield) Reload(mode string) {
	if mode == "" {
		mode = "log"
	}
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
}

// Mode returns the current mode (safe for concurrent reads).
func (s *Shield) Mode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

// Middleware returns an http.Handler that validates the X-Linkari-Client header.
func (s *Shield) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// OPTIONS exempt — permanent invariant for CORS preflight (G-14).
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		header := r.Header.Get("X-Linkari-Client")
		valid := header != "" && clientHeaderRegex.MatchString(header)

		if !valid {
			mode := s.Mode()
			if mode == "enforce" {
				slog.Debug(
					"shield blocked request",
					"event_type", "shield_blocked",
					"enforced", true,
					"path", r.URL.Path,
					"header", header,
					"remote", r.RemoteAddr,
				)
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			// mode: log — pass through with debug log (G-12: NOT slog.Warn).
			slog.Debug(
				"shield would block request",
				"event_type", "shield_blocked",
				"enforced", false,
				"path", r.URL.Path,
				"header", header,
				"remote", r.RemoteAddr,
			)
		}

		next.ServeHTTP(w, r)
	})
}

// History: the funnelAuthGuardMiddleware from EPIC-001 M1 (which blocked the
// static operator token on the public funnel) was deleted with the funnel
// mux in EPIC-272 - with the perimeter moved to the operator Ingress it had
// no legitimate remaining scope and would have 401'd the primary Android
// /share credential. X-Linkari-Client is an unauthenticated signal, not a
// security boundary (G-08).
