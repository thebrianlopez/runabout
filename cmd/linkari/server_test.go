package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
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
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
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

	// Expect uinit_eng, uinit_life, uinit_travel, uinit_fashion, uinit_music, uinit_finance, uinit_dining.
	// ginit is only present when ATLASSIAN_DOMAIN=grindr.atlassian.net.
	wantIDs := map[string]string{
		"uinit_eng":     "eng",
		"uinit_life":    "life",
		"uinit_travel":  "travel",
		"uinit_fashion": "fashion",
		"uinit_music":   "music",
		"uinit_finance": "finance",
		"uinit_dining":  "dining",
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
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)

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

func TestTLSCertMissing(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	// Neither file exists — os.Stat should fail.
	if _, err := os.Stat(certFile); err == nil {
		t.Fatal("expected cert file to be absent")
	}
	if _, err := os.Stat(keyFile); err == nil {
		t.Fatal("expected key file to be absent")
	}
}

func TestTLSCertPresent(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	// Write placeholder files to simulate mkcert output being present.
	if err := os.WriteFile(certFile, []byte("cert"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, []byte("key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(certFile); err != nil {
		t.Fatalf("cert file should be present: %v", err)
	}
	if _, err := os.Stat(keyFile); err != nil {
		t.Fatalf("key file should be present: %v", err)
	}
}

func TestNotifyWithVerdict(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, newTestQueue(t), NewRingLog(10), false, nil)
	mux := srv.Mux()

	payload := notifyRequest{
		Score:   85,
		URL:     "https://example.com/paper",
		Slug:    "cool-paper",
		Profile: "eng",
		Verdict: "Actionable research on fine-tuning with self-distillation",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/notify", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ShareResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %q", resp.Status)
	}
}

func TestNotifyBelowThreshold(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, nil, NewRingLog(10), false, nil)
	mux := srv.Mux()

	payload := notifyRequest{
		Score:   40,
		URL:     "https://example.com/low",
		Slug:    "low-score-item",
		Profile: "eng",
		Verdict: "Not relevant",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/notify", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp ShareResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %q", resp.Status)
	}
	// Below threshold → message should indicate "logged only"
	if resp.Message == "" {
		t.Error("expected non-empty message")
	}
}

func TestNotifyDeviceFCMTokenOverride(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, newTestQueue(t), NewRingLog(10), false, nil)
	mux := srv.Mux()

	payload := notifyRequest{
		Score:   85,
		URL:     "https://example.com/paper",
		Slug:    "cool-paper",
		Profile: "eng",
		Verdict: "Great paper on transformers",
	}
	body, _ := json.Marshal(payload)

	// Pass device_fcm_token as query param — should be preferred over global.
	req := httptest.NewRequest(http.MethodPost, "/notify?device_fcm_token=device-token-123", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ShareResponse
	json.NewDecoder(w.Body).Decode(&resp)
	// Without a real fcmTokenSource the push will fail, but the device token
	// path is exercised (firebase not configured → logged only).
	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %q: %s", resp.Status, resp.Message)
	}
}

func TestNotifyFallbackToGlobalToken(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, newTestQueue(t), NewRingLog(10), false, nil)
	mux := srv.Mux()

	payload := notifyRequest{
		Score:   85,
		URL:     "https://example.com/paper",
		Slug:    "cool-paper",
		Profile: "eng",
	}
	body, _ := json.Marshal(payload)

	// No device_fcm_token query param — should fall back to global token.
	req := httptest.NewRequest(http.MethodPost, "/notify", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ShareResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %q", resp.Status)
	}
	// Push is now durably enqueued into push_outbox; worker handles delivery.
	if !strings.Contains(resp.Message, "enqueued") {
		t.Errorf("expected enqueued message, got %q", resp.Message)
	}
}

func TestProfileFieldInPayload(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)

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
