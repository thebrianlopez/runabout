package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serverConfigWithAccounts(accounts map[string]YouTubeAccountConfig) *ServerConfig {
	cfg := &ServerConfig{}
	cfg.YouTube.Accounts = accounts
	return cfg
}

// CT-5: [server.youtube.accounts.personal] with slot="personal" and
// sources=["watch_later"] parsed; resolveSourceSlot returns "personal".
func TestYouTubeSlotConfig_CT5_ResolvePersonalSlot(t *testing.T) {
	cfg := serverConfigWithAccounts(map[string]YouTubeAccountConfig{
		"personal": {Slot: "personal", Sources: []string{"watch_later"}},
	})
	slot := resolveSourceSlot(cfg, "watch_later")
	assert.Equal(t, "personal", slot)
}

// CT-5b: No [server.youtube.accounts] stanza: resolveSourceSlot returns "default".
func TestYouTubeSlotConfig_CT5b_NoAccountsReturnsDefault(t *testing.T) {
	cfg := &ServerConfig{} // empty accounts map
	slot := resolveSourceSlot(cfg, "watch_later")
	assert.Equal(t, "default", slot)
}

// CT-5c: Source not in any account block: resolveSourceSlot returns "default"
// even when other sources are mapped.
func TestYouTubeSlotConfig_CT5c_UnmappedSourceReturnsDefault(t *testing.T) {
	cfg := serverConfigWithAccounts(map[string]YouTubeAccountConfig{
		"personal": {Slot: "personal", Sources: []string{"watch_later"}},
	})
	slot := resolveSourceSlot(cfg, "liked")
	assert.Equal(t, "default", slot, "unmapped source must fall back to default")
}

// CT-6: Same source "watch_later" in two account blocks: validateSlotConfig returns error.
func TestYouTubeSlotConfig_CT6_ConflictDetected(t *testing.T) {
	cfg := serverConfigWithAccounts(map[string]YouTubeAccountConfig{
		"personal": {Slot: "personal", Sources: []string{"watch_later"}},
		"service":  {Slot: "default", Sources: []string{"watch_later"}},
	})
	err := validateSlotConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "youtube_slot_conflict")
	assert.Contains(t, err.Error(), "watch_later")
}

// CT-6b: Two account blocks with distinct sources: validateSlotConfig returns nil.
func TestYouTubeSlotConfig_CT6b_NoConflictDistinctSources(t *testing.T) {
	cfg := serverConfigWithAccounts(map[string]YouTubeAccountConfig{
		"personal": {Slot: "personal", Sources: []string{"watch_later"}},
		"service":  {Slot: "default", Sources: []string{"liked"}},
	})
	err := validateSlotConfig(cfg)
	require.NoError(t, err)
}

// RG-7: Existing config without [server.youtube.accounts] parses cleanly;
// resolveSourceSlot returns "default" without panic.
func TestYouTubeSlotConfig_RG7_NilAccountsNoPanic(t *testing.T) {
	cfg := &ServerConfig{} // YouTube.Accounts is nil map
	assert.NotPanics(t, func() {
		slot := resolveSourceSlot(cfg, "watch_later")
		assert.Equal(t, "default", slot)
	})
	assert.NotPanics(t, func() {
		err := validateSlotConfig(cfg)
		require.NoError(t, err)
	})
}

// RG-8: validateSlotConfig is idempotent - calling twice returns same result.
func TestYouTubeSlotConfig_RG8_ValidateIdempotent(t *testing.T) {
	cfg := serverConfigWithAccounts(map[string]YouTubeAccountConfig{
		"personal": {Slot: "personal", Sources: []string{"watch_later"}},
		"service":  {Slot: "default", Sources: []string{"watch_later"}}, // conflict
	})
	err1 := validateSlotConfig(cfg)
	err2 := validateSlotConfig(cfg)
	assert.Equal(t, err1 != nil, err2 != nil, "validateSlotConfig must be deterministic")
}

// Additional: slot="" in account block treated as "default".
func TestYouTubeSlotConfig_EmptySlotFallsBackToDefault(t *testing.T) {
	cfg := serverConfigWithAccounts(map[string]YouTubeAccountConfig{
		"service": {Slot: "", Sources: []string{"watch_later"}},
	})
	slot := resolveSourceSlot(cfg, "watch_later")
	assert.Equal(t, "default", slot, "empty slot in account block should resolve to default")
}
