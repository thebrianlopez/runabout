package cache

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetOrFetchNilStore(t *testing.T) {
	called := false
	result, err := GetOrFetch[string](nil, "key", "src", time.Hour, false, func() (string, error) {
		called = true
		return "hello", nil
	})
	require.NoError(t, err)
	assert.True(t, called, "fetchFn was not called with nil store")
	assert.Equal(t, "hello", result)
}

func TestGetOrFetchMiss(t *testing.T) {
	s := tempDB(t)
	callCount := 0

	result, err := GetOrFetch[string](s, "key1", "test", time.Hour, false, func() (string, error) {
		callCount++
		return "fetched", nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "fetchFn call count")
	assert.Equal(t, "fetched", result)
}

func TestGetOrFetchHit(t *testing.T) {
	s := tempDB(t)
	key := JiraUserKey("hit@test.com")

	// Populate cache via first call.
	GetOrFetch[string](s, key, "test", time.Hour, false, func() (string, error) {
		return "cached_value", nil
	})

	// Second call should not invoke fetchFn.
	callCount := 0
	result, err := GetOrFetch[string](s, key, "test", time.Hour, false, func() (string, error) {
		callCount++
		return "should_not_see_this", nil
	})
	require.NoError(t, err)
	assert.Equal(t, 0, callCount, "fetchFn was called on cache hit")
	assert.Equal(t, "cached_value", result)
}

func TestGetOrFetchRefresh(t *testing.T) {
	s := tempDB(t)
	key := JiraUserKey("refresh@test.com")

	// Populate cache.
	GetOrFetch[string](s, key, "test", time.Hour, false, func() (string, error) {
		return "old", nil
	})

	// Refresh should bypass cache.
	result, err := GetOrFetch[string](s, key, "test", time.Hour, true, func() (string, error) {
		return "new", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "new", result)

	// Verify the refreshed value is now cached.
	result2, _ := GetOrFetch[string](s, key, "test", time.Hour, false, func() (string, error) {
		return "should_not_see", nil
	})
	assert.Equal(t, "new", result2, "cached value after refresh")
}

func TestGetOrFetchExpired(t *testing.T) {
	s := tempDB(t)
	key := JiraUserKey("expired@test.com")

	// Populate with 0 TTL.
	GetOrFetch[string](s, key, "test", 0, false, func() (string, error) {
		return "old", nil
	})

	time.Sleep(time.Millisecond)

	// Should call fetchFn because entry is expired.
	callCount := 0
	result, err := GetOrFetch[string](s, key, "test", time.Hour, false, func() (string, error) {
		callCount++
		return "fresh", nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "fetchFn call count after expiry")
	assert.Equal(t, "fresh", result)
}

func TestGetOrFetchAPIError(t *testing.T) {
	s := tempDB(t)
	apiErr := errors.New("api failure")

	_, err := GetOrFetch[string](s, "key", "test", time.Hour, false, func() (string, error) {
		return "", apiErr
	})
	assert.ErrorIs(t, err, apiErr)
}

func TestGetOrFetchSlice(t *testing.T) {
	s := tempDB(t)
	key := "slice-key"

	items := []string{"a", "b", "c"}
	result, err := GetOrFetch[[]string](s, key, "test", time.Hour, false, func() ([]string, error) {
		return items, nil
	})
	require.NoError(t, err)
	assert.Len(t, result, 3)

	// Verify cache hit returns the same data.
	result2, _ := GetOrFetch[[]string](s, key, "test", time.Hour, false, func() ([]string, error) {
		return nil, errors.New("should not be called")
	})
	require.Len(t, result2, 3)
	assert.Equal(t, "a", result2[0])
}

func TestGetOrFetchWithCorruptDB(t *testing.T) {
	// Use a store that we can close to simulate corruption.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s := Open(dbPath)
	require.NotNil(t, s, "Open returned nil")
	s.Close() // Close the DB to simulate problems.

	// GetOrFetch should fall through to fetchFn, not panic.
	result, err := GetOrFetch[string](s, "key", "test", time.Hour, false, func() (string, error) {
		return "fallback", nil
	})
	// Should succeed via fetchFn even if cache operations fail.
	require.NoError(t, err, "GetOrFetch on closed store")
	assert.Equal(t, "fallback", result)
}
