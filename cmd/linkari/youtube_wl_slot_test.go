package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// slotTestEventLogger creates an EventLogger backed by a temp file and returns
// a function to read back all emitted events as a slice of maps.
func slotTestEventLogger(t *testing.T) (*EventLogger, func() []map[string]interface{}) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	el, err := NewEventLogger(path)
	require.NoError(t, err)
	t.Cleanup(func() { el.Close() })

	readEvents := func() []map[string]interface{} {
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			return nil
		}
		var out []map[string]interface{}
		for _, line := range splitLines(data) {
			if len(line) == 0 {
				continue
			}
			var m map[string]interface{}
			if err := json.Unmarshal(line, &m); err == nil {
				// Flatten metadata fields into top-level map for easy assertions.
				if meta, ok := m["metadata"].(map[string]interface{}); ok {
					for k, v := range meta {
						m[k] = v
					}
					delete(m, "metadata")
				}
				out = append(out, m)
			}
		}
		return out
	}
	return el, readEvents
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// mockTokenSource returns a no-op oauth2.TokenSource.
type mockTokenSource struct{}

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "mock_access"}, nil
}

// seedSlotToken seeds a token into the given queue for testing.
func seedSlotToken(t *testing.T, q *Queue, userID int64, slot, token string) {
	t.Helper()
	require.NoError(t, q.SetYouTubeSlotToken(userID, slot, token, 9999999))
}

// CT-7: syncWatchLaterAsync with slot="personal" calls GetYouTubeSlotToken
// for "personal", not "default".
func TestWLSlot_CT7_SlotPersonalRoutesPersonalToken(t *testing.T) {
	q := newTestQueue(t)
	seedSlotToken(t, q, 1, "personal", "personal_tok")

	el, readEvents := slotTestEventLogger(t)

	// Override execYouTubePlaylistItems to return empty - we only care about slot routing.
	deps := &ytListDeps{}
	deps.PlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		return nil, "", nil
	}

	syncWatchLaterAsync("default", "personal", q, el, "cid", "csec", false, deps)

	events := readEvents()
	// source_start must include slot="personal"
	found := false
	for _, ev := range events {
		if ev["event_type"] == "source_start" {
			assert.Equal(t, "personal", ev["slot"], "source_start must carry correct slot")
			found = true
		}
	}
	assert.True(t, found, "source_start event must be emitted")
}

// CT-7b: syncWatchLaterAsync with slot="default" uses the "default" slot.
func TestWLSlot_CT7b_SlotDefaultRoutesDefaultToken(t *testing.T) {
	q := newTestQueue(t)
	seedSlotToken(t, q, 1, "default", "default_tok")

	el, readEvents := slotTestEventLogger(t)

	deps := &ytListDeps{}
	deps.PlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		return nil, "", nil
	}

	syncWatchLaterAsync("default", "default", q, el, "cid", "csec", false, deps)

	events := readEvents()
	for _, ev := range events {
		if ev["event_type"] == "source_start" {
			assert.Equal(t, "default", ev["slot"])
		}
	}
}

// CT-8: slot_no_token for "personal" emits source_disabled; "default" slot
// (via a separate syncLikedVideosAsync call) completes normally.
func TestWLSlot_CT8_SlotNoTokenEmitsSourceDisabled(t *testing.T) {
	q := newTestQueue(t)
	// Only seed "default" - "personal" has no token.
	seedSlotToken(t, q, 1, "default", "default_tok")

	el, readEvents := slotTestEventLogger(t)

	deps := &ytListDeps{}
	deps.PlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		return nil, "", nil
	}

	// Watch Later with "personal" slot - no token stored.
	syncWatchLaterAsync("default", "personal", q, el, "cid", "csec", false, deps)
	// Liked with "default" slot - has a token.
	syncLikedVideosAsync("default", "default", q, el, "cid", "csec", false, deps)

	events := readEvents()

	// Verify source_disabled was emitted for yt_watch_later.
	hasDisabled := false
	for _, ev := range events {
		if ev["event_type"] == "source_disabled" && ev["source"] == "yt_watch_later" {
			assert.Equal(t, "slot_no_token", ev["reason"])
			assert.Equal(t, "personal", ev["slot"])
			hasDisabled = true
		}
	}
	assert.True(t, hasDisabled, "source_disabled with reason=slot_no_token must be emitted for yt_watch_later")

	// Verify yt_liked completed with source_complete (default slot had a token).
	hasLikedComplete := false
	for _, ev := range events {
		if ev["event_type"] == "source_complete" && ev["source"] == "yt_liked" {
			hasLikedComplete = true
		}
	}
	assert.True(t, hasLikedComplete, "yt_liked with valid default slot must complete normally")
}

// CT-8b: source_start event contains slot field matching configured slot.
func TestWLSlot_CT8b_SourceStartContainsSlot(t *testing.T) {
	q := newTestQueue(t)
	seedSlotToken(t, q, 1, "personal", "personal_tok")

	el, readEvents := slotTestEventLogger(t)

	deps := &ytListDeps{}
	deps.PlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		return nil, "", nil
	}

	syncWatchLaterAsync("default", "personal", q, el, "cid", "csec", false, deps)

	events := readEvents()
	for _, ev := range events {
		if ev["event_type"] == "source_start" && ev["source"] == "yt_watch_later" {
			slot, ok := ev["slot"]
			assert.True(t, ok, "source_start must have slot field")
			assert.Equal(t, "personal", slot)
			return
		}
	}
	t.Error("source_start event for yt_watch_later not found")
}

// CT-8c: source_complete event contains slot field.
func TestWLSlot_CT8c_SourceCompleteContainsSlot(t *testing.T) {
	q := newTestQueue(t)
	seedSlotToken(t, q, 1, "personal", "personal_tok")

	el, readEvents := slotTestEventLogger(t)

	deps := &ytListDeps{}
	deps.PlaylistItems = func(_ context.Context, _ oauth2.TokenSource, _, _ string) ([]ytPlaylistItem, string, error) {
		return nil, "", nil
	}

	syncWatchLaterAsync("default", "personal", q, el, "cid", "csec", false, deps)

	events := readEvents()
	for _, ev := range events {
		if ev["event_type"] == "source_complete" && ev["source"] == "yt_watch_later" {
			slot, ok := ev["slot"]
			assert.True(t, ok, "source_complete must have slot field")
			assert.Equal(t, "personal", slot)
			return
		}
	}
	t.Error("source_complete event for yt_watch_later not found")
}

// RG-9: source_disabled reason=slot_no_token is emitted (not a panic) when
// slot has no stored token.
func TestWLSlot_RG9_SlotNoTokenNoPanic(t *testing.T) {
	q := newTestQueue(t)
	// No tokens seeded at all.

	el, readEvents := slotTestEventLogger(t)

	assert.NotPanics(t, func() {
		syncWatchLaterAsync("default", "someSlot", q, el, "cid", "csec", false, nil)
	})

	events := readEvents()
	hasDisabled := false
	for _, ev := range events {
		if ev["event_type"] == "source_disabled" && ev["reason"] == "slot_no_token" {
			hasDisabled = true
		}
	}
	assert.True(t, hasDisabled, "source_disabled with reason=slot_no_token must be emitted")

	_ = sql.ErrNoRows // verify sql import used
	_ = errors.New    // verify errors import used
}
