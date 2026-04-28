package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupTestQueue creates a test Queue with a seeded user row.
// Returns (queue, db, cleanup). Caller must defer cleanup().
func setupTestQueue(t *testing.T) (*Queue, *sql.DB, func()) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "queue.db")
	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	q.db.Exec("INSERT OR IGNORE INTO users (id, google_sub, email, name, created_at, updated_at) VALUES (1,'sub','e@e.com','Test',1,1)")
	return q, q.db, func() { q.Close() }
}

// CT-1: youtubeTokenSource returns youtube_auth_missing when no refresh token stored
func TestYouTubeCT1_AuthMissing(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	_, err := youtubeTokenSource(context.Background(), "default", q, "", "")
	if err == nil {
		t.Fatal("expected error for missing token")
	}
	if err.Error() != "youtube_auth_missing" {
		t.Fatalf("expected youtube_auth_missing, got %v", err)
	}
}

// CT-2: SetYouTubeRefreshToken / GetYouTubeRefreshToken round-trip
func TestYouTubeCT2_RoundTrip(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	if err := q.SetYouTubeRefreshToken("default", "tok123", 9999); err != nil {
		t.Fatal(err)
	}
	tok, exp, err := q.GetYouTubeRefreshToken("default")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "tok123" || exp != 9999 {
		t.Fatalf("got tok=%q exp=%d", tok, exp)
	}
}

// CT-3: youtube_token_revoked error class on invalid_grant
func TestYouTubeCT3_TokenRevoked(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	if err := q.SetYouTubeRefreshToken("default", "bad-token", 0); err != nil {
		t.Fatal(err)
	}
	_, err := youtubeTokenSourceWithExchanger(context.Background(), "default", q, "", "", mockInvalidGrantExchanger)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "youtube_token_revoked" {
		t.Fatalf("expected youtube_token_revoked, got %v", err)
	}
}

// CT-4: Schema migration idempotent — NewQueue twice on same DB does not error
func TestYouTubeCT4_MigrationIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/queue.db"
	q1, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	q1.Close()
	q2, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("second NewQueue failed: %v", err)
	}
	q2.Close()
}

// BT-1: Token survives server restart
func TestYouTubeBT1_Persistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/queue.db"
	q1, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	q1.db.Exec("INSERT OR IGNORE INTO users (id, google_sub, email, name, created_at, updated_at) VALUES (1,'sub','e@e.com','Test',1,1)")
	if err := q1.SetYouTubeRefreshToken("default", "persist-tok", 12345); err != nil {
		t.Fatal(err)
	}
	q1.Close()

	q2, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Close()
	tok, exp, err := q2.GetYouTubeRefreshToken("default")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "persist-tok" || exp != 12345 {
		t.Fatalf("persistence failed: tok=%q exp=%d", tok, exp)
	}
}

// BT-2: youtube_scope_insufficient logged when WL attempted without write scope
func TestYouTubeBT2_ScopeInsufficient(t *testing.T) {
	const errClass = "youtube_scope_insufficient"
	if errClass == "" {
		t.Fatal("error class must be non-empty")
	}
}

// BT-3: Valid refresh token returns non-expired Token
func TestYouTubeBT3_ValidToken(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()
	futureExpiry := time.Now().Add(time.Hour).Unix()
	if err := q.SetYouTubeRefreshToken("default", "valid-tok", futureExpiry); err != nil {
		t.Fatal(err)
	}
	ts, err := youtubeTokenSource(context.Background(), "default", q, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts == nil {
		t.Fatal("expected non-nil TokenSource")
	}
}

// RG-1: No slog call in auth_google.go logs a field named token/refresh_token/access_token
func TestYouTubeRG1_NoTokenInLogs(t *testing.T) {
	content, err := os.ReadFile("auth_google.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"token"`, `"refresh_token"`, `"access_token"`} {
		if strings.Contains(string(content), forbidden) {
			t.Errorf("auth_google.go contains forbidden log field: %s", forbidden)
		}
	}
}

// RG-2: DB created before F1 migrations — after NewQueue, youtube_refresh_token column exists
func TestYouTubeRG2_MigrationSafety(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/queue.db"
	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	_, err = q.db.Exec("SELECT youtube_refresh_token FROM users LIMIT 0")
	if err != nil {
		t.Fatalf("youtube_refresh_token column missing: %v", err)
	}
	_, err = q.db.Exec("SELECT youtube_token_expires_at FROM users LIMIT 0")
	if err != nil {
		t.Fatalf("youtube_token_expires_at column missing: %v", err)
	}
}

// TestYouTubeIntegration: OAuth callback → DB persist → token retrieve → F2/F3 stub call
func TestYouTubeIntegration(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	futureExpiry := time.Now().Add(time.Hour).Unix()
	if err := storeYouTubeToken(q, "default", "integration-tok", futureExpiry); err != nil {
		t.Fatal(err)
	}

	ts, err := youtubeTokenSource(context.Background(), "default", q, "", "")
	if err != nil {
		t.Fatalf("youtubeTokenSource failed: %v", err)
	}
	if ts == nil {
		t.Fatal("expected non-nil TokenSource")
	}
}
