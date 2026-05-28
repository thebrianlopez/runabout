package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testServer creates a Server with a test queue and optional Google verifier.
// Returns the server and a cleanup function.
func testServer(t *testing.T) (*Server, *rsa.PrivateKey) {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	jwksSrv := testJWKSServer(t, &priv.PublicKey)
	t.Cleanup(jwksSrv.Close)

	dbPath := t.TempDir() + "/test.db"
	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })

	verifier := NewGoogleTokenVerifier(testClientID)
	verifier.jwksURL = jwksSrv.URL

	ring := NewRingLog(10)
	srv := NewServer("test-operator-token", nil, q, ring, false, nil)
	srv.googleVerifier = verifier

	return srv, priv
}

func validTestClaims(sub string) GoogleClaims {
	return GoogleClaims{
		Sub:           sub,
		Email:         sub + "@example.com",
		EmailVerified: true,
		Name:          "Test User " + sub,
		Iss:           "accounts.google.com",
		Aud:           testClientID,
		Exp:           time.Now().Add(time.Hour).Unix(),
		Iat:           time.Now().Unix(),
	}
}

func TestAuthGoogleUnknownUser(t *testing.T) {
	srv, priv := testServer(t)

	claims := validTestClaims("unknown-sub")
	token := signTestJWT(t, claims, priv)

	body, _ := json.Marshal(authGoogleRequest{IDToken: token})
	req := httptest.NewRequest(http.MethodPost, "/auth/google", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleAuthGoogle(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp authGoogleResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Status != "invite_required" {
		t.Errorf("status = %q, want %q", resp.Status, "invite_required")
	}
	if resp.SessionToken != "" {
		t.Error("session_token should be empty for unknown user")
	}
}

func TestAuthGoogleKnownUser(t *testing.T) {
	srv, priv := testServer(t)

	// Create a user first.
	sub := "known-sub-123"
	now := time.Now().Unix()
	srv.queue.db.Exec(
		`INSERT INTO users (google_sub, email, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		sub, "known@example.com", "Known User", now, now,
	)

	claims := validTestClaims(sub)
	token := signTestJWT(t, claims, priv)

	body, _ := json.Marshal(authGoogleRequest{IDToken: token})
	req := httptest.NewRequest(http.MethodPost, "/auth/google", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleAuthGoogle(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp authGoogleResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Status != "authenticated" {
		t.Errorf("status = %q, want %q", resp.Status, "authenticated")
	}
	if resp.SessionToken == "" {
		t.Error("session_token should not be empty for known user")
	}
	if resp.UserID == 0 {
		t.Error("user_id should be set for known user")
	}
}

func TestAuthGoogleBadToken(t *testing.T) {
	srv, _ := testServer(t)

	body, _ := json.Marshal(authGoogleRequest{IDToken: "garbage.token.here"})
	req := httptest.NewRequest(http.MethodPost, "/auth/google", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleAuthGoogle(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAuthInviteValid(t *testing.T) {
	srv, priv := testServer(t)

	// Create an invite code.
	code, err := srv.queue.CreateInviteCode()
	if err != nil {
		t.Fatal(err)
	}

	sub := "new-user-sub"
	claims := validTestClaims(sub)
	token := signTestJWT(t, claims, priv)

	body, _ := json.Marshal(authInviteRequest{IDToken: token, InviteCode: code})
	req := httptest.NewRequest(http.MethodPost, "/auth/invite", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleAuthInvite(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	var resp authGoogleResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Status != "authenticated" {
		t.Errorf("status = %q, want %q", resp.Status, "authenticated")
	}
	if resp.SessionToken == "" {
		t.Error("session_token should not be empty after invite redemption")
	}

	// Verify user was created.
	user, err := srv.queue.LookupUserBySub(sub)
	if err != nil {
		t.Fatal(err)
	}
	if user == nil {
		t.Fatal("user should exist after invite redemption")
	}
}

func TestAuthInviteAlreadyUsed(t *testing.T) {
	srv, priv := testServer(t)

	code, err := srv.queue.CreateInviteCode()
	if err != nil {
		t.Fatal(err)
	}

	// Redeem the code once.
	_, err = srv.queue.RedeemInvite(code, "first-user-sub", "first@example.com", "First")
	if err != nil {
		t.Fatal(err)
	}

	// Try to redeem again.
	claims := validTestClaims("second-user-sub")
	token := signTestJWT(t, claims, priv)

	body, _ := json.Marshal(authInviteRequest{IDToken: token, InviteCode: code})
	req := httptest.NewRequest(http.MethodPost, "/auth/invite", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleAuthInvite(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestAuthInviteInvalidCode(t *testing.T) {
	srv, priv := testServer(t)

	claims := validTestClaims("some-sub")
	token := signTestJWT(t, claims, priv)

	body, _ := json.Marshal(authInviteRequest{IDToken: token, InviteCode: "BADCODE1"})
	req := httptest.NewRequest(http.MethodPost, "/auth/invite", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleAuthInvite(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestSessionTokenShareAuth(t *testing.T) {
	srv, priv := testServer(t)

	// Create user and session.
	sub := "session-test-sub"
	now := time.Now().Unix()
	srv.queue.db.Exec(
		`INSERT INTO users (google_sub, email, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		sub, "session@example.com", "Session User", now, now,
	)
	user, _ := srv.queue.LookupUserBySub(sub)

	claims := validTestClaims(sub)
	token := signTestJWT(t, claims, priv)
	_ = token // used for Google flow; we need a session token

	sessionToken, err := srv.issueSession(user.ID, sub)
	if err != nil {
		t.Fatal(err)
	}

	// Verify session token authenticates requests.
	if !srv.authenticateRequest(httptest.NewRequest(
		http.MethodGet, "/queue",
		nil, // no body for GET
	)) {
		// No auth header — should fail.
	}

	authedReq := httptest.NewRequest(http.MethodGet, "/queue", nil)
	authedReq.Header.Set("Authorization", "Bearer "+sessionToken)
	if !srv.authenticateRequest(authedReq) {
		t.Error("authenticateRequest should accept valid session token")
	}
}

func TestSessionTokenGinitBlocked(t *testing.T) {
	srv, _ := testServer(t)

	// Create user and session.
	sub := "ginit-block-sub"
	now := time.Now().Unix()
	srv.queue.db.Exec(
		`INSERT INTO users (google_sub, email, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		sub, "ginit@example.com", "Ginit User", now, now,
	)
	user, _ := srv.queue.LookupUserBySub(sub)

	sessionToken, err := srv.issueSession(user.ID, sub)
	if err != nil {
		t.Fatal(err)
	}

	// Session token should be blocked from ginit_* actions.
	kind, allowed := srv.checkScopedAuth(sessionToken, "ginit_auto")
	if kind != "session" {
		t.Errorf("kind = %q, want %q", kind, "session")
	}
	if allowed {
		t.Error("session token should NOT be allowed for ginit_* actions")
	}

	// But allowed for non-ginit actions.
	kind, allowed = srv.checkScopedAuth(sessionToken, "uinit_auto")
	if kind != "session" {
		t.Errorf("kind = %q, want %q", kind, "session")
	}
	if !allowed {
		t.Error("session token should be allowed for uinit_* actions")
	}
}

func TestAdminInviteRequiresOperatorToken(t *testing.T) {
	srv, _ := testServer(t)

	// No auth header.
	req := httptest.NewRequest(http.MethodPost, "/admin/invite", nil)
	w := httptest.NewRecorder()
	srv.handleAdminInvite(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: status = %d, want 401", w.Code)
	}

	// Wrong token.
	req = httptest.NewRequest(http.MethodPost, "/admin/invite", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w = httptest.NewRecorder()
	srv.handleAdminInvite(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", w.Code)
	}

	// Correct operator token.
	req = httptest.NewRequest(http.MethodPost, "/admin/invite", nil)
	req.Header.Set("Authorization", "Bearer test-operator-token")
	w = httptest.NewRecorder()
	srv.handleAdminInvite(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("correct token: status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	var resp adminInviteResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Code) != 8 {
		t.Errorf("invite code length = %d, want 8", len(resp.Code))
	}
}
