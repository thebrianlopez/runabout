// Package auth provides credential management for the Jira poller.
// It wraps the shared secrets.Resolver with a TTL-based rotation cache
// so Atlassian API tokens can be refreshed without restarting the service.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thebrianlopez/runabout/internal/secrets"
)

// Sentinel errors for the auth package. Callers use errors.Is to check them.
var (
	// ErrSecretsInit is returned when the SM client cannot be initialised at
	// startup (e.g. no AWS config, bad region). Fatal — service should exit.
	ErrSecretsInit = errors.New("auth: secrets manager init failed")

	// ErrSecretsMissing is returned when the secret ARN is not found or access
	// is denied. Fatal at startup; stale creds returned on rotation.
	ErrSecretsMissing = errors.New("auth: secret not found or access denied")

	// ErrSecretsFormat is returned when the resolved secret is not valid JSON
	// or the required keys are missing / empty.
	ErrSecretsFormat = errors.New("auth: secret format invalid")

	// ErrRefreshFailed is returned by Get when a TTL re-fetch fails.
	// The caller receives the last-known-good Credentials alongside this error.
	ErrRefreshFailed = errors.New("auth: credential refresh failed")
)

// Credentials holds a resolved Atlassian Basic Auth pair.
type Credentials struct {
	Email    string
	APIToken string
}

// atlassianSecret is the expected JSON shape in Secrets Manager.
type atlassianSecret struct {
	Email    string `json:"email"`
	APIToken string `json:"api_token"`
}

// CredentialCache wraps a secrets.Resolver and re-fetches Atlassian
// credentials from Secrets Manager after a configurable TTL.
// Safe for concurrent use.
type CredentialCache struct {
	resolver  *secrets.Resolver
	secretARN string
	ttl       time.Duration
	nowFn     func() time.Time

	mu        sync.RWMutex
	cached    Credentials
	fetchedAt time.Time // zero = never fetched
}

// NewCredentialCache constructs a CredentialCache.
//   - resolver:  shared Resolver (constructed once per process in main).
//   - secretARN: base SM ARN; the resolver will read the full JSON value and
//     parse "email" and "api_token" keys directly.
//   - ttl:       0 disables caching (always re-fetches on Get).
//   - nowFn:     clock source; pass time.Now in production, fake in tests.
func NewCredentialCache(
	resolver *secrets.Resolver,
	secretARN string,
	ttl time.Duration,
	nowFn func() time.Time,
) *CredentialCache {
	return &CredentialCache{
		resolver:  resolver,
		secretARN: secretARN,
		ttl:       ttl,
		nowFn:     nowFn,
	}
}

// Get returns current credentials. If the TTL has elapsed (or no fetch has
// happened yet) it re-fetches from SM. On refresh failure it returns the
// last-known-good credentials and a wrapped ErrRefreshFailed. The returned
// Credentials are never zero-valued when err is ErrRefreshFailed.
func (c *CredentialCache) Get(ctx context.Context) (Credentials, error) {
	c.mu.RLock()
	if !c.isStaleUnsafe() {
		creds := c.cached
		c.mu.RUnlock()
		return creds, nil
	}
	c.mu.RUnlock()

	// Re-check under write lock — another goroutine may have refreshed.
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isStaleUnsafe() {
		return c.cached, nil
	}

	fresh, err := c.fetch(ctx)
	if err != nil {
		if c.fetchedAt.IsZero() {
			// No stale value to fall back to — propagate the error directly.
			return Credentials{}, err
		}
		return c.cached, fmt.Errorf("%w: %w", ErrRefreshFailed, err)
	}

	c.cached = fresh
	c.fetchedAt = c.nowFn()
	return c.cached, nil
}

// ForceRefresh discards the cached value and fetches immediately.
// Used at startup to eagerly validate credentials before the first poll.
// Returns a typed error (ErrSecretsMissing, ErrSecretsFormat, ErrSecretsInit)
// — never ErrRefreshFailed.
func (c *CredentialCache) ForceRefresh(ctx context.Context) error {
	fresh, err := c.fetch(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cached = fresh
	c.fetchedAt = c.nowFn()
	c.mu.Unlock()
	return nil
}

// fetch resolves and parses credentials. Returns typed sentinel errors.
func (c *CredentialCache) fetch(ctx context.Context) (Credentials, error) {
	uri := c.smURI()

	payload, _, err := c.resolver.Resolve(ctx, uri)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Credentials{}, fmt.Errorf("%w: %w", ErrSecretsMissing, err)
		}
		msg := err.Error()
		if contains(msg, "sm client init") || contains(msg, "load aws config") {
			return Credentials{}, fmt.Errorf("%w: %w", ErrSecretsInit, err)
		}
		if contains(msg, "not found") || contains(msg, "access denied") || contains(msg, "AccessDeniedException") {
			return Credentials{}, fmt.Errorf("%w: %w", ErrSecretsMissing, err)
		}
		return Credentials{}, fmt.Errorf("%w: %w", ErrSecretsMissing, err)
	}

	var s atlassianSecret
	if err := json.Unmarshal([]byte(payload), &s); err != nil {
		return Credentials{}, fmt.Errorf("%w: %w", ErrSecretsFormat, err)
	}
	if s.Email == "" || s.APIToken == "" {
		return Credentials{}, fmt.Errorf("%w: email and api_token must both be non-empty", ErrSecretsFormat)
	}
	return Credentials{Email: s.Email, APIToken: s.APIToken}, nil
}

// smURI returns the URI to resolve. LOCAL_DEV=true substitutes a local file.
func (c *CredentialCache) smURI() string {
	if os.Getenv("LOCAL_DEV") == "true" {
		home := os.Getenv("HOME")
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		return "file://" + filepath.Join(home, ".atlassian", "credentials")
	}
	return "secretsmanager://" + c.secretARN
}

// isStale reports whether a re-fetch is needed without holding a lock.
// Callers must acquire the appropriate lock before calling.
func (c *CredentialCache) isStaleUnsafe() bool {
	if c.fetchedAt.IsZero() {
		return true
	}
	if c.ttl == 0 {
		return true
	}
	return c.nowFn().After(c.fetchedAt.Add(c.ttl))
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
