package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler is a trivial handler that returns 200 OK for shield middleware tests.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestShieldMiddleware(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		method     string
		header     string // X-Linkari-Client value; empty = no header
		wantStatus int
	}{
		// enforce mode
		{
			name:       "valid header enforce passes",
			mode:       "enforce",
			method:     http.MethodGet,
			header:     "android/1.2.3",
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid header with flavor enforce passes",
			mode:       "enforce",
			method:     http.MethodGet,
			header:     "android/1.2.3/cloud",
			wantStatus: http.StatusOK,
		},
		{
			name:       "chrome extension header enforce passes",
			mode:       "enforce",
			method:     http.MethodGet,
			header:     "chrome-extension/0.5.1",
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing header enforce returns 403",
			mode:       "enforce",
			method:     http.MethodGet,
			header:     "",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "invalid format enforce returns 403",
			mode:       "enforce",
			method:     http.MethodGet,
			header:     "garbage",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "slash only enforce returns 403",
			mode:       "enforce",
			method:     http.MethodGet,
			header:     "/",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "too many segments enforce returns 403",
			mode:       "enforce",
			method:     http.MethodGet,
			header:     "a/b/c/d",
			wantStatus: http.StatusForbidden,
		},
		// log mode
		{
			name:       "missing header log mode passes",
			mode:       "log",
			method:     http.MethodGet,
			header:     "",
			wantStatus: http.StatusOK,
		},
		{
			name:       "invalid header log mode passes",
			mode:       "log",
			method:     http.MethodGet,
			header:     "garbage",
			wantStatus: http.StatusOK,
		},
		// OPTIONS exempt (G-04: test shield middleware in isolation, not via cors)
		{
			name:       "OPTIONS exempt enforce mode",
			mode:       "enforce",
			method:     http.MethodOptions,
			header:     "",
			wantStatus: http.StatusOK,
		},
		{
			name:       "OPTIONS exempt log mode",
			mode:       "log",
			method:     http.MethodOptions,
			header:     "",
			wantStatus: http.StatusOK,
		},
		// empty mode defaults to log
		{
			name:       "empty mode defaults to log",
			mode:       "",
			method:     http.MethodGet,
			header:     "",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewShield(tt.mode)
			handler := s.Middleware(okHandler)

			req := httptest.NewRequest(tt.method, "/share", nil)
			if tt.header != "" {
				req.Header.Set("X-Linkari-Client", tt.header)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestShieldReload(t *testing.T) {
	s := NewShield("enforce")

	// Enforce mode: missing header → 403
	req := httptest.NewRequest(http.MethodGet, "/share", nil)
	w := httptest.NewRecorder()
	s.Middleware(okHandler).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("enforce: got %d, want 403", w.Code)
	}

	// Reload to log mode: missing header → 200
	s.Reload("log")
	req = httptest.NewRequest(http.MethodGet, "/share", nil)
	w = httptest.NewRecorder()
	s.Middleware(okHandler).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("after reload to log: got %d, want 200", w.Code)
	}

	// Reload back to enforce: missing header → 403
	s.Reload("enforce")
	req = httptest.NewRequest(http.MethodGet, "/share", nil)
	w = httptest.NewRecorder()
	s.Middleware(okHandler).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("after reload to enforce: got %d, want 403", w.Code)
	}
}

func TestFunnelMuxUsesShield(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, nil, NewRingLog(10), false, nil)
	srv.SetShield(NewShield("enforce"))

	handler := srv.FunnelMux()

	// Without X-Linkari-Client → 403
	req := httptest.NewRequest(http.MethodGet, "/actions", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("FunnelMux without client header: got %d, want 403", w.Code)
	}

	// With valid X-Linkari-Client → passes through to route
	req = httptest.NewRequest(http.MethodGet, "/actions", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("X-Linkari-Client", "android/1.0.0")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code == http.StatusForbidden {
		t.Errorf("FunnelMux with valid client header: got 403, want non-403")
	}
}

func TestMuxDoesNotUseShield(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, nil, NewRingLog(10), false, nil)
	srv.SetShield(NewShield("enforce"))

	handler := srv.Mux()

	// Mux should NOT use shield — missing header should pass
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code == http.StatusForbidden {
		t.Errorf("Mux should not use shield: got 403")
	}
}

func TestFunnelMux404ForHealthz(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, nil, NewRingLog(10), false, nil)
	// Use log mode so shield doesn't interfere with the 404 test.
	srv.SetShield(NewShield("log"))

	handler := srv.FunnelMux()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("FunnelMux /healthz: got %d, want 404", w.Code)
	}
}

func TestShieldConfigDefaults(t *testing.T) {
	// Zero-valued ServerConfig → ShieldConfig returns "log"
	sc := &ServerConfig{}
	if got := sc.ShieldConfig(); got != "log" {
		t.Errorf("default ShieldConfig: got %q, want \"log\"", got)
	}

	// Explicit mode → returned as-is
	sc.Shield.Mode = "enforce"
	if got := sc.ShieldConfig(); got != "enforce" {
		t.Errorf("explicit ShieldConfig: got %q, want \"enforce\"", got)
	}
}

// --- M4: funnelAuthGuardMiddleware tests ---

func TestFunnelAuthGuard(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("operator-token", router, nil, NewRingLog(10), false, nil)
	srv.jiraToken = "jira-token-xyz"

	guard := srv.funnelAuthGuardMiddleware(okHandler)

	tests := []struct {
		name       string
		path       string
		auth       string // Authorization header; empty = no header
		wantStatus int
	}{
		{
			name:       "static operator token rejected",
			path:       "/share",
			auth:       "Bearer operator-token",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "session token passes",
			path:       "/share",
			auth:       "Bearer some-session-token",
			wantStatus: http.StatusOK,
		},
		{
			name:       "jira token passes",
			path:       "/share",
			auth:       "Bearer jira-token-xyz",
			wantStatus: http.StatusOK,
		},
		{
			name:       "no auth header passes",
			path:       "/share",
			auth:       "",
			wantStatus: http.StatusOK,
		},
		{
			name:       "unauthenticated /auth/google passes",
			path:       "/auth/google",
			auth:       "Bearer operator-token",
			wantStatus: http.StatusOK,
		},
		{
			name:       "unauthenticated /auth/invite passes",
			path:       "/auth/invite",
			auth:       "Bearer operator-token",
			wantStatus: http.StatusOK,
		},
		{
			name:       "/register with operator token passes (G-09)",
			path:       "/register",
			auth:       "Bearer operator-token",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			w := httptest.NewRecorder()
			guard.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestClientHeaderRegex(t *testing.T) {
	valid := []string{
		"android/1.2.3",
		"android/1.2.3/cloud",
		"chrome-extension/0.5.1",
		"ios/2.0.0-beta",
		"my_client/1.0/local",
	}
	invalid := []string{
		"",
		"garbage",
		"/",
		"a/b/c/d",
		"android/",
		"/1.2.3",
		"android /1.2.3",
		"android/1.2.3/ cloud",
	}

	for _, h := range valid {
		if !clientHeaderRegex.MatchString(h) {
			t.Errorf("expected valid: %q", h)
		}
	}
	for _, h := range invalid {
		if clientHeaderRegex.MatchString(h) {
			t.Errorf("expected invalid: %q", h)
		}
	}
}
