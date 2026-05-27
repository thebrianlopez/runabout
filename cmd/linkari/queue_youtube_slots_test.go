package main

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CT-1: SetSlotToken + GetSlotToken roundtrip.
func TestYouTubeSlots_CT1_Roundtrip(t *testing.T) {
	q := newTestQueue(t)
	err := q.SetYouTubeSlotToken(1, "personal", "rt_personal", 9999)
	require.NoError(t, err)

	token, expiresAt, err := q.GetYouTubeSlotToken(1, "personal")
	require.NoError(t, err)
	assert.Equal(t, "rt_personal", token)
	assert.Equal(t, int64(9999), expiresAt)
}

// CT-2: Slot isolation - two slots hold independent tokens.
func TestYouTubeSlots_CT2_Isolation(t *testing.T) {
	q := newTestQueue(t)
	require.NoError(t, q.SetYouTubeSlotToken(1, "personal", "tok_A", 1000))
	require.NoError(t, q.SetYouTubeSlotToken(1, "default", "tok_B", 2000))

	tokenPersonal, _, err := q.GetYouTubeSlotToken(1, "personal")
	require.NoError(t, err)
	assert.Equal(t, "tok_A", tokenPersonal, "personal slot should hold tok_A")

	tokenDefault, _, err := q.GetYouTubeSlotToken(1, "default")
	require.NoError(t, err)
	assert.Equal(t, "tok_B", tokenDefault, "default slot should hold tok_B")
}

// CT-3: Migration - existing youtube_refresh_token copied to slot "default".
func TestYouTubeSlots_CT3_Migration(t *testing.T) {
	q := newTestQueue(t)
	now := time.Now().Unix()
	// Seed a user row with a pre-existing refresh token.
	_, err := q.db.Exec(
		`INSERT INTO users (google_sub, email, name, active, created_at, updated_at, youtube_refresh_token, youtube_token_expires_at)
		 VALUES ('sub1', 'test@test.com', 'Test', 1, ?, ?, 'tok123', 12345)`,
		now, now,
	)
	require.NoError(t, err)

	// Re-run the migration SQL (simulates next server startup after a user row is present).
	_, err = q.db.Exec(`INSERT OR IGNORE INTO youtube_oauth_slots
		(user_id, slot_name, refresh_token, token_expires_at, created_at, updated_at)
	SELECT
		id, 'default', youtube_refresh_token, COALESCE(youtube_token_expires_at, 0),
		strftime('%s','now'), strftime('%s','now')
	FROM users
	WHERE youtube_refresh_token IS NOT NULL AND youtube_refresh_token != ''`)
	require.NoError(t, err)

	token, expiresAt, err := q.GetYouTubeSlotToken(1, "default")
	require.NoError(t, err)
	assert.Equal(t, "tok123", token)
	assert.Equal(t, int64(12345), expiresAt)
}

// CT-3b: Migration idempotency - running twice produces exactly 1 row.
func TestYouTubeSlots_CT3b_MigrationIdempotent(t *testing.T) {
	q := newTestQueue(t)
	now := time.Now().Unix()
	_, err := q.db.Exec(
		`INSERT INTO users (google_sub, email, name, active, created_at, updated_at, youtube_refresh_token, youtube_token_expires_at)
		 VALUES ('sub1', 'test@test.com', 'Test', 1, ?, ?, 'tok123', 0)`,
		now, now,
	)
	require.NoError(t, err)

	migSQL := `INSERT OR IGNORE INTO youtube_oauth_slots
		(user_id, slot_name, refresh_token, token_expires_at, created_at, updated_at)
	SELECT
		id, 'default', youtube_refresh_token, COALESCE(youtube_token_expires_at, 0),
		strftime('%s','now'), strftime('%s','now')
	FROM users
	WHERE youtube_refresh_token IS NOT NULL AND youtube_refresh_token != ''`

	_, err = q.db.Exec(migSQL)
	require.NoError(t, err)
	_, err = q.db.Exec(migSQL)
	require.NoError(t, err)

	var count int
	err = q.db.QueryRow(`SELECT COUNT(*) FROM youtube_oauth_slots WHERE slot_name='default'`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "migration should produce exactly 1 row even when run twice")
}

// CT-3c: Migration skipped for empty token - produces 0 rows.
func TestYouTubeSlots_CT3c_MigrationSkippedEmpty(t *testing.T) {
	q := newTestQueue(t)
	now := time.Now().Unix()
	// Insert user with empty refresh token.
	_, err := q.db.Exec(
		`INSERT INTO users (google_sub, email, name, active, created_at, updated_at, youtube_refresh_token)
		 VALUES ('sub1', 'test@test.com', 'Test', 1, ?, ?, '')`,
		now, now,
	)
	require.NoError(t, err)

	_, err = q.db.Exec(`INSERT OR IGNORE INTO youtube_oauth_slots
		(user_id, slot_name, refresh_token, token_expires_at, created_at, updated_at)
	SELECT
		id, 'default', youtube_refresh_token, COALESCE(youtube_token_expires_at, 0),
		strftime('%s','now'), strftime('%s','now')
	FROM users
	WHERE youtube_refresh_token IS NOT NULL AND youtube_refresh_token != ''`)
	require.NoError(t, err)

	var count int
	err = q.db.QueryRow(`SELECT COUNT(*) FROM youtube_oauth_slots`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "migration should produce 0 rows for empty refresh token")
}

// RG-1: GetYouTubeSlotToken returns sql.ErrNoRows for unknown slot.
func TestYouTubeSlots_RG1_UnknownSlotErrNoRows(t *testing.T) {
	q := newTestQueue(t)
	_, _, err := q.GetYouTubeSlotToken(1, "nonexistent")
	assert.True(t, errors.Is(err, sql.ErrNoRows), "expected sql.ErrNoRows, got: %v", err)
}

// RG-2: SetYouTubeSlotToken called twice for same slot updates in place (no duplicate row).
func TestYouTubeSlots_RG2_UpsertNoDuplicate(t *testing.T) {
	q := newTestQueue(t)
	require.NoError(t, q.SetYouTubeSlotToken(1, "personal", "first_tok", 100))
	require.NoError(t, q.SetYouTubeSlotToken(1, "personal", "second_tok", 200))

	var count int
	err := q.db.QueryRow(`SELECT COUNT(*) FROM youtube_oauth_slots WHERE slot_name='personal'`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "upsert should produce exactly 1 row")

	token, expiresAt, err := q.GetYouTubeSlotToken(1, "personal")
	require.NoError(t, err)
	assert.Equal(t, "second_tok", token, "second write should win")
	assert.Equal(t, int64(200), expiresAt)
}

// RG-3: Migration does not overwrite pre-existing slot "default".
func TestYouTubeSlots_RG3_MigrationNoOverwrite(t *testing.T) {
	q := newTestQueue(t)
	now := time.Now().Unix()

	// Pre-seed slot "default" with token X.
	require.NoError(t, q.SetYouTubeSlotToken(1, "default", "pre_existing_X", 5000))

	// Seed user with a different token.
	_, err := q.db.Exec(
		`INSERT INTO users (google_sub, email, name, active, created_at, updated_at, youtube_refresh_token, youtube_token_expires_at)
		 VALUES ('sub1', 'test@test.com', 'Test', 1, ?, ?, 'from_user_table', 1)`,
		now, now,
	)
	require.NoError(t, err)

	// Run migration - INSERT OR IGNORE should not touch the pre-existing row.
	_, err = q.db.Exec(`INSERT OR IGNORE INTO youtube_oauth_slots
		(user_id, slot_name, refresh_token, token_expires_at, created_at, updated_at)
	SELECT
		id, 'default', youtube_refresh_token, COALESCE(youtube_token_expires_at, 0),
		strftime('%s','now'), strftime('%s','now')
	FROM users
	WHERE youtube_refresh_token IS NOT NULL AND youtube_refresh_token != ''`)
	require.NoError(t, err)

	token, _, err := q.GetYouTubeSlotToken(1, "default")
	require.NoError(t, err)
	assert.Equal(t, "pre_existing_X", token, "migration must not overwrite pre-existing slot")
}
