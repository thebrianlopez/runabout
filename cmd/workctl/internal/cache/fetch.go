package cache

import (
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/config"
)

// fetchGroup deduplicates concurrent API calls for the same cache key.
// When cache warm runs multiple periods and two goroutines miss the cache for
// the same key simultaneously, only one API call fires; the other waits and
// shares the result.
var fetchGroup singleflight.Group

// GetOrFetch tries the cache first, falling back to fetchFn on miss.
//
// Behavior:
//   - store == nil → calls fetchFn directly (cache disabled)
//   - refresh == true → skips cache lookup, calls fetchFn, stores result
//   - cache hit → unmarshal and return
//   - cache miss/expired → deduplicated fetchFn call via singleflight, store result
//   - any cache error → log warning, call fetchFn (never fail due to cache)
func GetOrFetch[T any](store *Store, key, source string, ttl time.Duration, refresh bool, fetchFn func() (T, error)) (T, error) {
	shortKey := key
	if len(shortKey) > 12 {
		shortKey = shortKey[:12]
	}

	// Cache disabled — pass through.
	if store == nil {
		config.LogDebug("cache: disabled, calling API directly")
		return fetchFn()
	}

	// Refresh flag — skip lookup, force API call.
	if !refresh {
		data, err := store.Get(key)
		if err != nil {
			config.LogDebug("cache: GET error (falling through to API): %v", err)
		} else if data != nil {
			var result T
			if err := json.Unmarshal(data, &result); err != nil {
				config.LogDebug("cache: unmarshal error (falling through to API): %v", err)
			} else {
				config.LogDebug("cache: HIT [%s] key=%s…", source, shortKey)
				return result, nil
			}
		}
	} else {
		config.LogDebug("cache: refresh requested, skipping lookup")
	}

	// Cache miss or error — deduplicate concurrent fetches for the same key.
	config.LogDebug("cache: MISS [%s] key=%s…", source, shortKey)

	type fetchResult struct {
		val T
		err error
	}
	v, _, _ := fetchGroup.Do(key, func() (any, error) {
		val, err := fetchFn()
		if err != nil {
			return fetchResult{val: val, err: err}, nil
		}
		encoded, encErr := json.Marshal(val)
		if encErr != nil {
			config.LogDebug("cache: marshal error (result not cached): %v", encErr)
		} else if putErr := store.Put(key, source, encoded, ttl); putErr != nil {
			config.LogDebug("cache: PUT error (result not cached): %v", putErr)
		} else {
			config.LogDebug("cache: stored [%s] key=%s… (%s, ttl=%s)",
				source, shortKey, humanBytes(len(encoded)), ttl)
		}
		return fetchResult{val: val, err: nil}, nil
	})
	r := v.(fetchResult)
	return r.val, r.err
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
