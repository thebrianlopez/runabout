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

// Shield validates X-Linkari-Client headers on the Funnel mux.
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

// funnelAuthGuardMiddleware rejects requests bearing the static operator token
// on the Funnel listener. Session tokens and Jira tokens pass through.
//
// Passthrough list: /auth/google, /auth/invite, /register (G-09).
//
// Wired into FunnelMux() by EPIC-001 M1.
//
// NOTE: X-Linkari-Client is an unauthenticated signal, not a security
// boundary (G-08). This guard is the actual auth-layer enforcement.
func (s *Server) funnelAuthGuardMiddleware(next http.Handler) http.Handler {
	passthroughPaths := map[string]bool{
		"/auth/google": true,
		"/auth/invite": true,
		"/register":    true,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Passthrough endpoints skip the guard entirely.
		if passthroughPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth != "" {
			bearer := ""
			if len(auth) > 7 && auth[:7] == "Bearer " {
				bearer = auth[7:]
			}
			// Block the static operator token on funnel.
			if bearer != "" && bearer == s.token {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			// Jira token passes through (distinct from operator token).
			// Session tokens pass through (checked by authenticateRequest).
		}

		next.ServeHTTP(w, r)
	})
}
