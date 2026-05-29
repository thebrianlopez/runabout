package main

// EPIC-184 M2: Contract tests for POST /auth/youtube serverAuthCode delegation.
// CT-1 through CT-8 as specified in:
// PERSONAL_20260528T101000Z_Runabout_YouTube_AccountDelegation_F1F3F4_TDD.md

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAuthCodeExchanger is the test double for serverAuthCodeExchanger.
type fakeAuthCodeExchanger struct {
	refreshToken string
	expiresAt    int64
	err          error
}

func (f *fakeAuthCodeExchanger) Exchange(_ context.Context, _, _, _ string) (string, int64, error) {
	return f.refreshToken, f.expiresAt, f.err
}

// postYouTubeAuth is a test helper that POSTs to /auth/youtube and returns the response.
func postYouTubeAuth(t *testing.T, srv *Server, sessionToken string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/auth/youtube", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if sessionToken != "" {
		req.Header.Set("Authorization", "Bearer "+sessionToken)
	}
	rr := httptest.NewRecorder()
	srv.handleYouTubeAuth(rr, req)
	return rr
}

// setupDelegationServer creates a test server with a seeded user+session and fake exchanger.
func setupDelegationServer(t *testing.T, exchanger serverAuthCodeExchanger) (*Server, string, int64) {
	t.Helper()
	srv, _ := testServer(t)
	srv.authCodeExchanger = exchanger
	srv.googleClientID = "test-client-id"
	srv.googleClientSecret = "test-client-secret"

	// Seed a user directly (bypass invite flow).
	sub := "delegation-user-001"
	now := time.Now().Unix()
	srv.queue.db.Exec(
		`INSERT INTO users (google_sub, email, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		sub, "delegation@example.com", "Delegation User", now, now,
	)
	user, err := srv.queue.LookupUserBySub(sub)
	require.NoError(t, err)

	sessionToken, err := srv.issueSession(user.ID, sub)
	require.NoError(t, err)

	return srv, sessionToken, user.ID
}

// CT-1: valid serverAuthCode → 200, token stored with source="android".
func TestYouTubeDelegation_CT1_ValidCode_Stored(t *testing.T) {
	exchanger := &fakeAuthCodeExchanger{refreshToken: "rt-ct1", expiresAt: 9999999999}
	srv, session, userID := setupDelegationServer(t, exchanger)

	rr := postYouTubeAuth(t, srv, session, map[string]string{
		"server_auth_code": "code-ct1",
		"slot":             "personal",
	})
	assert.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "personal", resp["slot"])
	assert.Equal(t, "stored", resp["status"])

	rt, exp, err := srv.queue.GetYouTubeSlotToken(userID, "personal")
	require.NoError(t, err)
	assert.Equal(t, "rt-ct1", rt)
	assert.Equal(t, int64(9999999999), exp)

	// Verify source="android" in DB.
	var src string
	row := srv.queue.db.QueryRow(
		`SELECT source FROM youtube_oauth_slots WHERE user_id=? AND slot_name=?`,
		userID, "personal",
	)
	require.NoError(t, row.Scan(&src))
	assert.Equal(t, "android", src)
}

// CT-2: missing server_auth_code → 400 missing_server_auth_code.
func TestYouTubeDelegation_CT2_MissingCode(t *testing.T) {
	srv, session, _ := setupDelegationServer(t, &fakeAuthCodeExchanger{})

	rr := postYouTubeAuth(t, srv, session, map[string]string{"slot": "personal"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "missing_server_auth_code")
}

// CT-3: no session token → 401 unauthenticated.
func TestYouTubeDelegation_CT3_NoAuth(t *testing.T) {
	srv, _, _ := setupDelegationServer(t, &fakeAuthCodeExchanger{})

	rr := postYouTubeAuth(t, srv, "", map[string]string{
		"server_auth_code": "code-ct3",
		"slot":             "personal",
	})
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// CT-4: exchanger returns error → 502 token_exchange_failed.
func TestYouTubeDelegation_CT4_ExchangeFailed(t *testing.T) {
	exchanger := &fakeAuthCodeExchanger{err: errors.New("google 400: invalid_grant")}
	srv, session, _ := setupDelegationServer(t, exchanger)

	rr := postYouTubeAuth(t, srv, session, map[string]string{
		"server_auth_code": "bad-code",
		"slot":             "personal",
	})
	assert.Equal(t, http.StatusBadGateway, rr.Code)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "token_exchange_failed", resp["error"])
	assert.Contains(t, resp["detail"], "invalid_grant")
}

// CT-5: token stored via handler is retrievable via GetYouTubeSlotToken.
func TestYouTubeDelegation_CT5_TokenRoundtrip(t *testing.T) {
	exchanger := &fakeAuthCodeExchanger{refreshToken: "rt-ct5", expiresAt: 1234567890}
	srv, session, userID := setupDelegationServer(t, exchanger)

	rr := postYouTubeAuth(t, srv, session, map[string]string{
		"server_auth_code": "code-ct5",
		"slot":             "personal",
	})
	require.Equal(t, http.StatusOK, rr.Code)

	rt, _, err := srv.queue.GetYouTubeSlotToken(userID, "personal")
	require.NoError(t, err)
	assert.Equal(t, "rt-ct5", rt)
}

// CT-6: re-POST same slot → 200, COUNT(*)=1 (overwrite, not duplicate).
func TestYouTubeDelegation_CT6_Overwrite(t *testing.T) {
	exchanger := &fakeAuthCodeExchanger{refreshToken: "rt-first", expiresAt: 1111}
	srv, session, userID := setupDelegationServer(t, exchanger)

	rr := postYouTubeAuth(t, srv, session, map[string]string{
		"server_auth_code": "code-first",
		"slot":             "personal",
	})
	require.Equal(t, http.StatusOK, rr.Code)

	exchanger.refreshToken = "rt-second"
	rr = postYouTubeAuth(t, srv, session, map[string]string{
		"server_auth_code": "code-second",
		"slot":             "personal",
	})
	require.Equal(t, http.StatusOK, rr.Code)

	var count int
	row := srv.queue.db.QueryRow(
		`SELECT COUNT(*) FROM youtube_oauth_slots WHERE user_id=? AND slot_name=?`,
		userID, "personal",
	)
	require.NoError(t, row.Scan(&count))
	assert.Equal(t, 1, count, "expected exactly 1 row after overwrite")

	rt, _, _ := srv.queue.GetYouTubeSlotToken(userID, "personal")
	assert.Equal(t, "rt-second", rt)
}

// CT-7: resolveSourceSlot returns "personal" and token resolves correctly (F3 routing).
func TestYouTubeDelegation_CT7_SlotRouting(t *testing.T) {
	exchanger := &fakeAuthCodeExchanger{refreshToken: "rt-ct7", expiresAt: 9999}
	srv, session, userID := setupDelegationServer(t, exchanger)

	rr := postYouTubeAuth(t, srv, session, map[string]string{
		"server_auth_code": "code-ct7",
		"slot":             "personal",
	})
	require.Equal(t, http.StatusOK, rr.Code)

	// Configure slot routing as the server would.
	cfg := &ServerConfig{}
	cfg.YouTube.Accounts = map[string]YouTubeAccountConfig{
		"personal": {Slot: "personal", Sources: []string{"watch_later"}},
	}
	slot := resolveSourceSlot(cfg, "watch_later")
	assert.Equal(t, "personal", slot)

	rt, _, err := srv.queue.GetYouTubeSlotToken(userID, slot)
	require.NoError(t, err)
	assert.Equal(t, "rt-ct7", rt)
}

// CT-8: source column = "android" for delegation; "cli" for CLI-written rows (F4).
func TestYouTubeDelegation_CT8_SourceColumn(t *testing.T) {
	exchanger := &fakeAuthCodeExchanger{refreshToken: "rt-android", expiresAt: 9999}
	srv, session, userID := setupDelegationServer(t, exchanger)

	// Write via Android delegation.
	rr := postYouTubeAuth(t, srv, session, map[string]string{
		"server_auth_code": "code-ct8",
		"slot":             "personal",
	})
	require.Equal(t, http.StatusOK, rr.Code)

	// Write via CLI path (SetYouTubeSlotToken defaults source="cli").
	require.NoError(t, srv.queue.SetYouTubeSlotToken(userID, "default", "rt-cli", 9999))

	var androidSrc, cliSrc string
	srv.queue.db.QueryRow(
		`SELECT source FROM youtube_oauth_slots WHERE user_id=? AND slot_name='personal'`, userID,
	).Scan(&androidSrc)
	srv.queue.db.QueryRow(
		`SELECT source FROM youtube_oauth_slots WHERE user_id=? AND slot_name='default'`, userID,
	).Scan(&cliSrc)

	assert.Equal(t, "android", androidSrc)
	assert.Equal(t, "cli", cliSrc)
}
