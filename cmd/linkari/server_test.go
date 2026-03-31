package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	tmux := &TmuxRunner{DefaultSession: "test"}
	router := NewRouter(tmux, false, "", 0)
	srv := NewServer("test-token", router, nil, NewRingLog(10), false, nil)
	mux := srv.Mux()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp ShareResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %q", resp.Status)
	}
}

func TestShareUnauthorized(t *testing.T) {
	srv := NewServer("secret", nil, nil, NewRingLog(10), false, nil)
	mux := srv.Mux()

	body := `{"type":"text","text":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/share", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestShareNoAuth(t *testing.T) {
	srv := NewServer("secret", nil, nil, NewRingLog(10), false, nil)
	mux := srv.Mux()

	body := `{"type":"text","text":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/share", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestShareMethodNotAllowed(t *testing.T) {
	srv := NewServer("secret", nil, nil, NewRingLog(10), false, nil)
	mux := srv.Mux()

	req := httptest.NewRequest(http.MethodGet, "/share", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestValidateRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     ShareRequest
		wantErr bool
	}{
		{"valid text", ShareRequest{Type: "text", Text: "hello"}, false},
		{"valid url", ShareRequest{Type: "url", URL: "https://example.com"}, false},
		{"empty text", ShareRequest{Type: "text", Text: ""}, true},
		{"empty url", ShareRequest{Type: "url", URL: ""}, true},
		{"bad url scheme", ShareRequest{Type: "url", URL: "ftp://foo"}, true},
		{"unknown type", ShareRequest{Type: "file", Text: "x"}, true},
		{"text too long", ShareRequest{Type: "text", Text: string(make([]byte, 4097))}, true},
		{"url too long", ShareRequest{Type: "url", URL: "https://" + string(make([]byte, 2048))}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRequest(&tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRequest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(3, time.Hour) // large window so nothing expires during test

	for i := 0; i < 3; i++ {
		if !rl.allow("client1") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if rl.allow("client1") {
		t.Fatal("4th request should be rate limited")
	}
	// Different client should still be allowed
	if !rl.allow("client2") {
		t.Fatal("different client should be allowed")
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://example.com", "'https://example.com'"},
		{"it's a test", "'it'\\''s a test'"},
		{"$(rm -rf /)", "'$(rm -rf /)'"},
		{"`id`", "'`id`'"},
		{"foo;bar", "'foo;bar'"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestActionsReturnsProfileTagged(t *testing.T) {
	tmux := &TmuxRunner{DefaultSession: "test"}
	router := NewRouter(tmux, false, "", 0)
	srv := NewServer("test-token", router, nil, NewRingLog(10), false, nil)
	mux := srv.Mux()

	req := httptest.NewRequest(http.MethodGet, "/actions", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var actions []Action
	if err := json.NewDecoder(w.Body).Decode(&actions); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Expect uinit_eng, uinit_life, uinit_travel, uinit_fashion, uinit_finance plus note and ginit.
	wantIDs := map[string]string{
		"uinit_eng":     "eng",
		"uinit_life":    "life",
		"uinit_travel":  "travel",
		"uinit_fashion": "fashion",
		"uinit_finance": "finance",
	}
	found := 0
	for _, a := range actions {
		if wantIcon, ok := wantIDs[a.ID]; ok {
			found++
			if a.Icon != wantIcon {
				t.Errorf("action %q: icon = %q, want %q", a.ID, a.Icon, wantIcon)
			}
			if a.Type != "url" {
				t.Errorf("action %q: type = %q, want %q", a.ID, a.Type, "url")
			}
		}
	}
	if found != len(wantIDs) {
		t.Errorf("found %d profile actions, want %d (actions: %+v)", found, len(wantIDs), actions)
	}
}

func TestProfileExtractionFromAction(t *testing.T) {
	tmux := &TmuxRunner{DefaultSession: "test"}
	router := NewRouter(tmux, false, "", 0)

	// Route a request with action "uinit_life" — should extract profile "life".
	req := &ShareRequest{
		Type:   "url",
		Action: "uinit_life",
		URL:    "https://example.com",
	}

	// Route will call URLHandler.Handle which calls tmux.NewWindow.
	// We don't have a real tmux, so we expect an error from the tmux call,
	// but we can verify the profile was extracted by checking req.Profile after routing.
	router.Route(req)

	if req.Profile != "life" {
		t.Errorf("profile = %q, want %q", req.Profile, "life")
	}
}

func TestProfileFieldInPayload(t *testing.T) {
	tmux := &TmuxRunner{DefaultSession: "test"}
	router := NewRouter(tmux, false, "", 0)

	// Explicit profile in payload takes precedence — action extraction
	// does not overwrite it.
	req := &ShareRequest{
		Type:    "url",
		Action:  "uinit_eng",
		URL:     "https://example.com",
		Profile: "finance",
	}

	router.Route(req)

	if req.Profile != "finance" {
		t.Errorf("profile = %q, want %q (explicit payload should take precedence)", req.Profile, "finance")
	}
}
