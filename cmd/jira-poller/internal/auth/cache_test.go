package auth_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/auth"
	"github.com/thebrianlopez/runabout/internal/secrets"
)

// fakeSM is an in-memory SecretsManagerAPI for tests.
type fakeSM struct {
	mu      sync.Mutex
	data    map[string]string
	err     error
	calls   int
}

func (f *fakeSM) GetSecretValue(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.data[id]
	if !ok {
		return "", os.ErrNotExist
	}
	return v, nil
}

func newResolver(sm *fakeSM) *secrets.Resolver {
	return secrets.New(func(_ context.Context) (secrets.SecretsManagerAPI, error) {
		return sm, nil
	})
}

const (
	testARN   = "jira-poller/atlassian"
	validJSON = `{"email":"user@example.com","api_token":"tok-abc123"}`
)

// CT-2: Get returns cached value before TTL expires (no second SM call).
func TestCredentialCache_Get_ReturnsCache_BeforeTTL(t *testing.T) {
	sm := &fakeSM{data: map[string]string{testARN: validJSON}}
	r := newResolver(sm)

	now := time.Now()
	cache := auth.NewCredentialCache(r, testARN, 6*time.Hour, func() time.Time { return now })

	if err := cache.ForceRefresh(context.Background()); err != nil {
		t.Fatalf("ForceRefresh: %v", err)
	}
	smCallsAfterRefresh := sm.calls

	creds, err := cache.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if creds.Email != "user@example.com" || creds.APIToken != "tok-abc123" {
		t.Errorf("creds = %+v, want user@example.com / tok-abc123", creds)
	}
	if sm.calls != smCallsAfterRefresh {
		t.Errorf("SM called again before TTL; calls went from %d to %d", smCallsAfterRefresh, sm.calls)
	}
}

// CT-3: Get re-fetches after TTL elapsed.
func TestCredentialCache_Get_RefetchAfterTTL(t *testing.T) {
	sm := &fakeSM{data: map[string]string{testARN: validJSON}}
	r := newResolver(sm)

	base := time.Now()
	cache := auth.NewCredentialCache(r, testARN, 6*time.Hour, func() time.Time { return base })

	if err := cache.ForceRefresh(context.Background()); err != nil {
		t.Fatalf("ForceRefresh: %v", err)
	}
	smCallsAfterFirst := sm.calls

	// Advance clock past TTL.
	stale := base.Add(7 * time.Hour)
	cache2 := auth.NewCredentialCache(r, testARN, 6*time.Hour, func() time.Time { return stale })
	// Force the stale state by refreshing once at base time, then advancing.
	// Use a single cache object with a mutable clock func.
	var clockMu sync.Mutex
	clk := base
	cacheWithClock := auth.NewCredentialCache(r, testARN, 6*time.Hour, func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clk
	})
	if err := cacheWithClock.ForceRefresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	callsAfterInit := sm.calls

	// Advance clock past TTL.
	clockMu.Lock()
	clk = base.Add(7 * time.Hour)
	clockMu.Unlock()

	if _, err := cacheWithClock.Get(context.Background()); err != nil {
		t.Fatalf("Get after TTL: %v", err)
	}
	if sm.calls <= callsAfterInit {
		t.Errorf("expected SM re-fetch after TTL; calls=%d (init=%d)", sm.calls, callsAfterInit)
	}
	_ = cache2
	_ = smCallsAfterFirst
}

// CT-4: Get returns stale + ErrRefreshFailed when SM errors during rotation.
func TestCredentialCache_Get_StaleOnRefreshError(t *testing.T) {
	sm := &fakeSM{data: map[string]string{testARN: validJSON}}
	r := newResolver(sm)

	var clockMu sync.Mutex
	clk := time.Now()
	cache := auth.NewCredentialCache(r, testARN, 6*time.Hour, func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clk
	})

	if err := cache.ForceRefresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Advance past TTL and make SM fail.
	clockMu.Lock()
	clk = clk.Add(7 * time.Hour)
	clockMu.Unlock()
	sm.mu.Lock()
	sm.err = errors.New("SM unavailable")
	sm.mu.Unlock()

	creds, err := cache.Get(context.Background())
	if err == nil {
		t.Fatal("expected ErrRefreshFailed, got nil")
	}
	if !errors.Is(err, auth.ErrRefreshFailed) {
		t.Errorf("err = %v, want ErrRefreshFailed", err)
	}
	// Stale creds must still be returned, not zero value.
	if creds.Email == "" || creds.APIToken == "" {
		t.Errorf("expected stale creds, got zero value %+v", creds)
	}
}

// CT-5: ForceRefresh fails on missing secret.
func TestCredentialCache_ForceRefresh_MissingSecret(t *testing.T) {
	sm := &fakeSM{data: map[string]string{}} // ARN not found
	r := newResolver(sm)

	cache := auth.NewCredentialCache(r, testARN, 6*time.Hour, time.Now)
	err := cache.ForceRefresh(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, auth.ErrSecretsMissing) {
		t.Errorf("err = %v, want ErrSecretsMissing", err)
	}
}

// CT-6: Empty email/api_token → ErrSecretsFormat.
func TestCredentialCache_ForceRefresh_EmptyEmail(t *testing.T) {
	sm := &fakeSM{data: map[string]string{
		testARN: `{"email":"","api_token":"tok-abc123"}`,
	}}
	r := newResolver(sm)

	cache := auth.NewCredentialCache(r, testARN, 6*time.Hour, time.Now)
	err := cache.ForceRefresh(context.Background())
	if !errors.Is(err, auth.ErrSecretsFormat) {
		t.Errorf("err = %v, want ErrSecretsFormat", err)
	}
}

func TestCredentialCache_ForceRefresh_EmptyToken(t *testing.T) {
	sm := &fakeSM{data: map[string]string{
		testARN: `{"email":"user@example.com","api_token":""}`,
	}}
	r := newResolver(sm)

	cache := auth.NewCredentialCache(r, testARN, 6*time.Hour, time.Now)
	err := cache.ForceRefresh(context.Background())
	if !errors.Is(err, auth.ErrSecretsFormat) {
		t.Errorf("err = %v, want ErrSecretsFormat", err)
	}
}

// CT-7: LOCAL_DEV=true reads from file, not SM.
func TestCredentialCache_LocalDev_ReadsFromFile(t *testing.T) {
	dir := t.TempDir()
	atlassianDir := filepath.Join(dir, ".atlassian")
	if err := os.Mkdir(atlassianDir, 0o700); err != nil {
		t.Fatal(err)
	}
	credFile := filepath.Join(atlassianDir, "credentials")
	if err := os.WriteFile(credFile, []byte(`{"email":"dev@example.com","api_token":"dev-tok"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("LOCAL_DEV", "true")
	t.Setenv("HOME", dir)

	// SM is empty — if LOCAL_DEV uses SM, ForceRefresh would fail.
	sm := &fakeSM{data: map[string]string{}}
	r := newResolver(sm)

	cache := auth.NewCredentialCache(r, testARN, 6*time.Hour, time.Now)
	if err := cache.ForceRefresh(context.Background()); err != nil {
		t.Fatalf("ForceRefresh with LOCAL_DEV: %v", err)
	}
	if sm.calls != 0 {
		t.Errorf("SM was called %d times with LOCAL_DEV=true, want 0", sm.calls)
	}
	creds, err := cache.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if creds.Email != "dev@example.com" {
		t.Errorf("email = %q, want dev@example.com", creds.Email)
	}
}

// CT-8: Concurrent Get calls are race-free.
func TestCredentialCache_Get_ConcurrentSafe(t *testing.T) {
	sm := &fakeSM{data: map[string]string{testARN: validJSON}}
	r := newResolver(sm)

	var clockMu sync.Mutex
	clk := time.Now()
	cache := auth.NewCredentialCache(r, testARN, 6*time.Hour, func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clk
	})
	if err := cache.ForceRefresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Trigger TTL expiry mid-flight.
	clockMu.Lock()
	clk = clk.Add(7 * time.Hour)
	clockMu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cache.Get(context.Background())
		}()
	}
	wg.Wait()
}

// CT-9: Linkari build verified separately via `make linkari`; here we confirm
// the secrets package is importable from jira-poller's module boundary.
func TestSecretsPackage_Importable(t *testing.T) {
	// If this file compiles, the import is valid — the test itself is the assertion.
	r := secrets.New(nil)
	if r == nil {
		t.Fatal("secrets.New returned nil")
	}
}
