package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// insertBskyTestUser inserts a test user and returns their auto-assigned ID.
func insertBskyTestUser(t *testing.T, q *Queue) int64 {
	t.Helper()
	res, err := q.db.Exec(
		`INSERT INTO users (google_sub, email, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"bsky-test-sub", "bsky@example.com", "Bsky User", 0, 0,
	)
	if err != nil {
		t.Fatalf("insert bsky test user: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// testBskyServerWithSession creates a Server with a test user and issues a session token.
func testBskyServerWithSession(t *testing.T) (*Server, string, int64) {
	t.Helper()
	q := newTestQueue(t)
	ring := NewRingLog(10)
	srv := NewServer("operator-token", nil, q, ring, false, nil)
	userID := insertBskyTestUser(t, q)
	sessionToken, err := srv.issueSession(userID, "bsky-test-sub")
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}
	return srv, sessionToken, userID
}

// fakePDS returns an httptest.Server that responds to createSession with the given
// status code and response body fields.
func fakePDS(t *testing.T, statusCode int, fields map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(fields)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --- CT-1: PersistBlueskySession → LoadBlueskySession round-trip ---

func TestBlueskyAuthCT1Roundtrip(t *testing.T) {
	q := newTestQueue(t)
	userID := insertBskyTestUser(t, q)

	want := BlueskySessionData{
		DID:        "did:plc:ct1test",
		Handle:     "ct1.bsky.social",
		AccessJWT:  "access-jwt-ct1",
		RefreshJWT: "refresh-jwt-ct1",
		Host:       "https://bsky.social",
	}
	if err := q.PersistBlueskySession(userID, want); err != nil {
		t.Fatalf("PersistBlueskySession: %v", err)
	}
	got, err := q.LoadBlueskySession(userID)
	if err != nil {
		t.Fatalf("LoadBlueskySession: %v", err)
	}
	if got == nil {
		t.Fatal("LoadBlueskySession returned nil, want non-nil")
	}
	if *got != want {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", *got, want)
	}
}

// --- CT-2: LoadBlueskySession returns nil, nil when no session stored ---

func TestBlueskyAuthCT2NoSession(t *testing.T) {
	q := newTestQueue(t)
	userID := insertBskyTestUser(t, q)

	got, err := q.LoadBlueskySession(userID)
	if err != nil {
		t.Fatalf("LoadBlueskySession: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil session, got %+v", *got)
	}
}

// --- CT-3: RefreshCallback persists new tokens ---

func TestBlueskyAuthCT3RefreshCallback(t *testing.T) {
	q := newTestQueue(t)
	userID := insertBskyTestUser(t, q)

	orig := BlueskySessionData{
		DID: "did:plc:ct3test", Handle: "ct3.bsky.social",
		AccessJWT: "original-access", RefreshJWT: "original-refresh",
		Host: "https://bsky.social",
	}
	if err := q.PersistBlueskySession(userID, orig); err != nil {
		t.Fatalf("PersistBlueskySession: %v", err)
	}

	refreshCb := func(updated BlueskySessionData) error {
		return q.UpdateBlueskySession(userID, updated)
	}
	updated := orig
	updated.AccessJWT = "refreshed-access"
	if err := refreshCb(updated); err != nil {
		t.Fatalf("RefreshCallback: %v", err)
	}

	got, err := q.LoadBlueskySession(userID)
	if err != nil {
		t.Fatalf("LoadBlueskySession after refresh: %v", err)
	}
	if got == nil {
		t.Fatal("LoadBlueskySession returned nil after refresh")
	}
	if got.AccessJWT != "refreshed-access" {
		t.Errorf("AccessJWT = %q, want %q", got.AccessJWT, "refreshed-access")
	}
}

// --- CT-4: ResumeBlueskySession constructs client without PDS contact ---

func TestBlueskyAuthCT4ResumeNoPDSContact(t *testing.T) {
	data := BlueskySessionData{
		DID:        "did:plc:ct4test",
		Handle:     "ct4.bsky.social",
		AccessJWT:  "access-ct4",
		RefreshJWT: "refresh-ct4",
		Host:       "https://bsky.social",
	}
	var refreshCalled bool
	cb := func(BlueskySessionData) error { refreshCalled = true; return nil }

	client := ResumeBlueskySession(data, cb)
	if client == nil {
		t.Fatal("ResumeBlueskySession returned nil")
	}
	if client.AccountDID() != data.DID {
		t.Errorf("AccountDID = %q, want %q", client.AccountDID(), data.DID)
	}
	if refreshCalled {
		t.Error("refresh callback must not fire on resume — no PDS contact expected")
	}
}

// --- CT-5: bskyClient == nil guard logs bluesky_session_missing, no panic ---

func TestBlueskyAuthCT5NilGuard(t *testing.T) {
	ring := NewRingLog(10)
	srv := NewServer("tok", nil, nil, ring, false, nil)
	// bskyClient is nil by default

	client, ok := srv.requireBskyClient("test-caller")
	if ok {
		t.Error("requireBskyClient should return false when bskyClient is nil")
	}
	if client != nil {
		t.Error("requireBskyClient should return nil client when bskyClient is nil")
	}
	// no panic == guard works
}

// --- BT-1: Valid credentials → session stored → account_did returned ---

func TestBlueskyAuthBT1ValidCredentials(t *testing.T) {
	srv, sessionToken, _ := testBskyServerWithSession(t)
	pds := fakePDS(t, http.StatusOK, map[string]string{
		"did": "did:plc:bt1test", "handle": "bt1.bsky.social",
		"accessJwt": "access-bt1", "refreshJwt": "refresh-bt1",
	})

	body, _ := json.Marshal(authBlueskyRequest{Handle: "bt1.bsky.social", Password: "pw", Host: pds.URL})
	req := httptest.NewRequest(http.MethodPost, "/auth/bluesky", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	w := httptest.NewRecorder()

	srv.handleAuthBluesky(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp authBlueskyResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "connected" {
		t.Errorf("status = %q, want %q", resp.Status, "connected")
	}
	if resp.AccountDID != "did:plc:bt1test" {
		t.Errorf("account_did = %q, want %q", resp.AccountDID, "did:plc:bt1test")
	}
	if srv.bskyClient == nil {
		t.Error("bskyClient should be set after successful auth")
	}

	got, err := srv.queue.LoadBlueskySession(1)
	if err != nil {
		t.Fatalf("LoadBlueskySession: %v", err)
	}
	if got == nil || got.DID != "did:plc:bt1test" {
		t.Errorf("persisted session DID = %v, want did:plc:bt1test", got)
	}
}

// --- BT-2: Bad credentials → 401 ---

func TestBlueskyAuthBT2BadCredentials(t *testing.T) {
	srv, sessionToken, _ := testBskyServerWithSession(t)
	pds := fakePDS(t, http.StatusUnauthorized, map[string]string{
		"error": "AuthenticationRequired", "message": "Invalid identifier or password",
	})

	body, _ := json.Marshal(authBlueskyRequest{Handle: "bad.user", Password: "wrong", Host: pds.URL})
	req := httptest.NewRequest(http.MethodPost, "/auth/bluesky", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	w := httptest.NewRecorder()

	srv.handleAuthBluesky(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if srv.bskyClient != nil {
		t.Error("bskyClient should remain nil after failed auth")
	}
}

// --- BT-3: Existing bluesky_session_json → bskyClient non-nil on startup ---

func TestBlueskyAuthBT3ResumeOnStartup(t *testing.T) {
	q := newTestQueue(t)
	userID := insertBskyTestUser(t, q)

	data := BlueskySessionData{
		DID: "did:plc:bt3test", Handle: "bt3.bsky.social",
		AccessJWT: "access-bt3", RefreshJWT: "refresh-bt3",
		Host: "https://bsky.social",
	}
	if err := q.PersistBlueskySession(userID, data); err != nil {
		t.Fatalf("PersistBlueskySession: %v", err)
	}

	ring := NewRingLog(10)
	srv := NewServer("tok", nil, q, ring, false, nil)
	resumeBlueskySessionsOnStartup(context.Background(), q, srv)

	if srv.bskyClient == nil {
		t.Fatal("bskyClient should be non-nil after startup resume")
	}
	if srv.bskyClient.AccountDID() != data.DID {
		t.Errorf("AccountDID = %q, want %q", srv.bskyClient.AccountDID(), data.DID)
	}
}

// --- BT-4: Corrupt JSON → WARN log, bskyClient stays nil ---

func TestBlueskyAuthBT4CorruptSession(t *testing.T) {
	q := newTestQueue(t)
	insertBskyTestUser(t, q)

	q.db.Exec("UPDATE users SET bluesky_session_json=? WHERE id=?", "not-valid-json{{{", 1)

	ring := NewRingLog(10)
	srv := NewServer("tok", nil, q, ring, false, nil)
	resumeBlueskySessionsOnStartup(context.Background(), q, srv)

	if srv.bskyClient != nil {
		t.Error("bskyClient should remain nil when session JSON is corrupt")
	}
}

// --- RG-1: UpdateBlueskySession persists new AccessJWT; LoadBlueskySession returns it ---

func TestBlueskyAuthRG1RefreshPersists(t *testing.T) {
	q := newTestQueue(t)
	userID := insertBskyTestUser(t, q)

	orig := BlueskySessionData{
		DID: "did:plc:rg1test", Handle: "rg1.bsky.social",
		AccessJWT: "original-access", RefreshJWT: "original-refresh",
		Host: "https://bsky.social",
	}
	if err := q.PersistBlueskySession(userID, orig); err != nil {
		t.Fatalf("PersistBlueskySession: %v", err)
	}

	updated := orig
	updated.AccessJWT = "new-access-after-refresh"
	if err := q.UpdateBlueskySession(userID, updated); err != nil {
		t.Fatalf("UpdateBlueskySession: %v", err)
	}

	got, err := q.LoadBlueskySession(userID)
	if err != nil {
		t.Fatalf("LoadBlueskySession: %v", err)
	}
	if got == nil {
		t.Fatal("LoadBlueskySession returned nil after update")
	}
	if got.AccessJWT != "new-access-after-refresh" {
		t.Errorf("AccessJWT = %q, want updated value", got.AccessJWT)
	}
}

// --- RG-2: NewQueue called twice on same DB does not error; column exists ---

func TestBlueskyAuthRG2IdempotentMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "idempotent.db")

	q1, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("first NewQueue: %v", err)
	}
	q1.Close()

	q2, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("second NewQueue: %v", err)
	}
	defer q2.Close()

	// Column presence: sql.ErrNoRows means column exists but no rows match — OK.
	var v sql.NullString
	if err := q2.db.QueryRow("SELECT bluesky_session_json FROM users WHERE id=-1").Scan(&v); err != nil && err != sql.ErrNoRows {
		t.Errorf("bluesky_session_json column missing or unexpected error: %v", err)
	}
}

// --- M8: Full F1 round-trip — login → persist → restart → resume ---

func TestBlueskyAuth(t *testing.T) {
	const testDID = "did:plc:m8test"
	pds := fakePDS(t, http.StatusOK, map[string]string{
		"did": testDID, "handle": "m8user.bsky.social",
		"accessJwt": "access-m8", "refreshJwt": "refresh-m8",
	})

	dbPath := filepath.Join(t.TempDir(), "m8.db")

	// Phase 1: login and persist.
	q1, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	ring := NewRingLog(10)
	srv1 := NewServer("tok", nil, q1, ring, false, nil)
	userID := insertBskyTestUser(t, q1)
	sessionToken, err := srv1.issueSession(userID, "bsky-test-sub")
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}

	body, _ := json.Marshal(authBlueskyRequest{Handle: "m8user.bsky.social", Password: "pw", Host: pds.URL})
	req := httptest.NewRequest(http.MethodPost, "/auth/bluesky", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	w := httptest.NewRecorder()
	srv1.handleAuthBluesky(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login failed: status=%d body=%s", w.Code, w.Body.String())
	}
	q1.Close()

	// Phase 2: simulate restart — new Queue and Server on same DB.
	q2, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("NewQueue restart: %v", err)
	}
	defer q2.Close()
	ring2 := NewRingLog(10)
	srv2 := NewServer("tok", nil, q2, ring2, false, nil)

	resumeBlueskySessionsOnStartup(context.Background(), q2, srv2)

	if srv2.bskyClient == nil {
		t.Fatal("bskyClient should be non-nil after restart resume")
	}
	if srv2.bskyClient.AccountDID() != testDID {
		t.Errorf("AccountDID = %q, want %q", srv2.bskyClient.AccountDID(), testDID)
	}
}
