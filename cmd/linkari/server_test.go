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

	"golang.org/x/oauth2"
)

func TestHealthz(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, nil, NewRingLog(10), false, nil)
	mux := srv.Mux()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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

func TestHealthzWithDB(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	q := newTestQueue(t)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)
	mux := srv.Mux()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want ok", body["status"])
	}
	if body["db"] != "ok" {
		t.Errorf("db = %q, want ok", body["db"])
	}
}

func TestHealthzJiraUnconfigured(t *testing.T) {
	// Server with no Jira credentials should return 200 with jira.configured=false.
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, nil, NewRingLog(10), false, nil)
	mux := srv.Mux()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	jiraRaw, ok := body["jira"]
	if !ok {
		t.Fatal("expected jira field in health response")
	}
	jira, ok := jiraRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("expected jira to be an object, got %T", jiraRaw)
	}
	if jira["configured"] != false {
		t.Errorf("jira.configured = %v, want false", jira["configured"])
	}
	if jira["warning"] != "jira_credentials_unconfigured" {
		t.Errorf("jira.warning = %v, want jira_credentials_unconfigured", jira["warning"])
	}
}

func TestHealthzDegradedDB(t *testing.T) {
	// Simulate mid-session DB failure by closing the connection after init.
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	q := newTestQueue(t)
	q.Close() // close the underlying connection; Ping will now fail

	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)
	mux := srv.Mux()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "degraded" {
		t.Errorf("status = %q, want degraded", body["status"])
	}
	if body["db"] != "error" {
		t.Errorf("db = %q, want error", body["db"])
	}
	if _, ok := body["db_error"]; !ok {
		t.Error("expected db_error field in degraded response")
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

	// EPIC-061: expect exactly 2 auto-profile actions.
	wantIDs := map[string]string{
		"uinit_auto": "auto",
		"ginit_auto": "work",
	}
	found := 0
	for _, a := range actions {
		if wantIcon, ok := wantIDs[a.ID]; ok {
			found++
			if a.Icon != wantIcon {
				t.Errorf("action %q: icon = %q, want %q", a.ID, a.Icon, wantIcon)
			}
		}
	}
	if found != len(wantIDs) {
		t.Errorf("found %d actions, want %d (actions: %+v)", found, len(wantIDs), actions)
	}
}

func TestAutoProfileResolution(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)

	// Route uinit_auto — server-score path returns sentinel without tmux.
	req := &ShareRequest{
		Type:   "url",
		Action: "uinit_auto",
		URL:    "https://example.com",
	}
	msg, err := router.Route(req)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !strings.Contains(msg, "Scoring") {
		t.Errorf("expected server-score sentinel, got %q", msg)
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
	// EPIC-059: /notify no longer early-returns on below-threshold scores,
	// so it needs a queue to call enqueueDigestPush. Use newTestQueue.
	srv := NewServer("test-token", router, newTestQueue(t), NewRingLog(10), false, nil)
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
	// EPIC-059: below-threshold scores now proceed to push enqueue
	// instead of returning "logged only".
	if !strings.Contains(resp.Message, "push enqueued") {
		t.Errorf("expected 'push enqueued' message, got %q", resp.Message)
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

// --- EPIC-056 M3: POST /push/test --------------------------------------

func newTestPushServer(t *testing.T, withDevice bool, withTokenSource bool) *Server {
	t.Helper()
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	q := newTestQueue(t)
	if withDevice {
		if err := q.UpsertDevice("device-token-xyz"); err != nil {
			t.Fatalf("upsert device: %v", err)
		}
	}
	var ts oauth2.TokenSource
	if withTokenSource {
		ts = fakeTokenSource{}
	}
	return NewServer("test-token", router, q, NewRingLog(10), false, ts)
}

func TestPushTest_HappyPath(t *testing.T) {
	isolateEventsDir(t)
	installStubTransport(t, &stubRoundTripper{status: http.StatusOK})

	srv := newTestPushServer(t, true, true)
	req := httptest.NewRequest(http.MethodPost, "/push/test", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp testPushResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status = %q, want ok", resp.Status)
	}
	if resp.Reason != "" || resp.Error != "" {
		t.Errorf("unexpected error fields: %+v", resp)
	}
}

func TestPushTest_NoDeviceRegistered(t *testing.T) {
	isolateEventsDir(t)
	srv := newTestPushServer(t, false, true)
	req := httptest.NewRequest(http.MethodPost, "/push/test", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var resp testPushResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Reason != "no_device_registered" {
		t.Errorf("reason = %q, want no_device_registered", resp.Reason)
	}
}

func TestPushTest_FCMSendFailed(t *testing.T) {
	isolateEventsDir(t)
	installStubTransport(t, &stubRoundTripper{status: http.StatusNotFound})

	srv := newTestPushServer(t, true, true)
	req := httptest.NewRequest(http.MethodPost, "/push/test", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var resp testPushResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Reason != "fcm_send_failed" {
		t.Errorf("reason = %q, want fcm_send_failed", resp.Reason)
	}
	if !strings.Contains(resp.Error, "404") {
		t.Errorf("expected error to mention 404, got %q", resp.Error)
	}
}

func TestPushTest_Unauthorized(t *testing.T) {
	srv := newTestPushServer(t, true, true)
	req := httptest.NewRequest(http.MethodPost, "/push/test", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestPushTest_MethodNotAllowed(t *testing.T) {
	srv := newTestPushServer(t, true, true)
	req := httptest.NewRequest(http.MethodGet, "/push/test", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestProfileFieldInPayload(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)

	// Explicit profile in payload takes precedence — auto-profile
	// does not overwrite it.
	req := &ShareRequest{
		Type:    "url",
		Action:  "uinit_auto",
		URL:     "https://example.com",
		Profile: "finance",
	}

	router.Route(req)

	if req.Profile != "finance" {
		t.Errorf("profile = %q, want %q (explicit payload should take precedence)", req.Profile, "finance")
	}
}
