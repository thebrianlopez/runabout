// EPIC-048 M3: integration tests for the tsnet default-flip and fallback rule.
//
// These tests start a real linkari serve process (using the cobra RunE path)
// and verify end-to-end behavior. They do NOT run in parallel because they
// set global log output and package-level vars (tsnetStart seam).
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Pure function tests ──────────────────────────────────────────────────────

// TestApplyTsnetFallback is a 5-row unit test of the fallback-to-local rule.
func TestApplyTsnetFallback(t *testing.T) {
	cases := []struct {
		name        string
		enabled     bool
		explicit    bool
		authKey     string
		wantEnabled bool
		wantWarn    bool
	}{
		{"default-on-no-key-fires-fallback", true, false, "", false, true},
		{"already-local-no-op", false, false, "", false, false},
		{"explicit-override-no-fallback", true, true, "", true, false},
		{"has-authkey-no-fallback", true, false, "tskey-abc", true, false},
		{"explicit-with-authkey-no-fallback", true, true, "tskey-abc", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := log.New(&buf, "", 0)
			got := applyTsnetFallback(tc.enabled, tc.explicit, tc.authKey, "", logger)
			if got != tc.wantEnabled {
				t.Errorf("enabled=%v want %v", got, tc.wantEnabled)
			}
			if tc.wantWarn && buf.Len() == 0 {
				t.Error("expected WARN to be emitted, got empty buffer")
			}
			if !tc.wantWarn && buf.Len() != 0 {
				t.Errorf("expected no WARN, got: %s", buf.String())
			}
		})
	}
}

// TestWarnLogGolden pins the exact WARN message text (locked decision #9).
// The format must never change without updating operator runbooks.
func TestWarnLogGolden(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	result := applyTsnetFallback(true, false, "", "", logger)
	if result {
		t.Error("expected false (fallback to local), got true")
	}
	want := tsnetFallbackWarn + "\n"
	if got := buf.String(); got != want {
		t.Errorf("WARN message mismatch\n got: %q\nwant: %q", got, want)
	}
}

// ── Integration test helpers ─────────────────────────────────────────────────

// findFreePort returns a free TCP port on localhost. Small race window is
// acceptable for tests.
func findFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("findFreePort: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// waitHTTP polls url until it returns HTTP 200 or the deadline expires.
func waitHTTP(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server at %s did not respond within %v", url, timeout)
}

// mockTsnetStart returns a tsnetStartFunc that binds a local TCP listener
// instead of starting a real Tailscale node. Suitable for integration tests
// that need the Funnel path to succeed without real credentials.
func mockTsnetStart(t *testing.T) tsnetStartFunc {
	t.Helper()
	return func(_ context.Context, _ TsnetConfig) (net.Listener, func() error, string, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, "", err
		}
		// cleanup is a nop: the HTTP server Serve() closes the listener on
		// Shutdown, so a second Close() would return "use of closed network
		// connection". The real TsnetServer.Close() handles this via the tsnet
		// node close; in tests we skip it.
		cleanup := func() error { return nil }
		return ln, cleanup, "linkari.test.ts.net", nil
	}
}

// swapTsnetStart replaces the package-level tsnetStart seam for the test and
// restores the original on cleanup.
func swapTsnetStart(t *testing.T, fn tsnetStartFunc) {
	t.Helper()
	orig := tsnetStart
	tsnetStart = fn
	t.Cleanup(func() { tsnetStart = orig })
}

// ── Integration tests ────────────────────────────────────────────────────────

// TestBareServeNoYamlFallbackToLocal asserts that bare `linkari serve` on a
// machine with no server.yaml and no tsnet_authkey env:
//  1. emits the pinned WARN fallback message.
//  2. starts a local HTTP listener (does NOT attempt tsnet).
//  3. exits 0 on context cancellation.
//
// syncBuffer is a mutex-synchronised bytes.Buffer for capturing log output
// that concurrent goroutines write while the test body reads. EPIC-258: the
// bare bytes.Buffer form raced (heap bytes.Buffer write vs String read) when a
// serve goroutine logged while the test asserted on the captured output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestBareServeNoYamlFallbackToLocal(t *testing.T) {
	// Capture pre-SetOutput log (where fallback WARN lives) without timestamps.
	var warnBuf syncBuffer
	origWriter := log.Default().Writer()
	origFlags := log.Flags()
	log.SetOutput(&warnBuf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(origWriter)
		log.SetFlags(origFlags)
	})

	// Clean HOME — no server.yaml, no env authkey.
	// Explicitly clear tsnet-related env vars so no real TS_AUTHKEY leaks in.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("LINKARI_TOKEN", "test-token-fallback")
	t.Setenv("LINKARI_QUEUE_DB", filepath.Join(tmpHome, "queue.db"))
	t.Setenv("TS_AUTHKEY", "")
	t.Setenv("LINKARI_TSNET", "")
	t.Setenv("LINKARI_LOCAL", "")

	port := findFreePort(t)
	cmd := serveCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--port", strconv.Itoa(port)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- cmd.ExecuteContext(ctx) }()

	// Wait for local listener to bind.
	waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/healthz", port), 5*time.Second)

	// WARN must be in the pre-SetOutput buffer.
	if got := warnBuf.String(); !strings.Contains(got, "tsnet_authkey") {
		t.Errorf("expected fallback WARN in log output, got:\n%s", got)
	}

	// Shutdown via context cancellation.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serve exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down within 5s after context cancel")
	}
}

// TestBareServeBootsFromYaml asserts that `linkari serve` with a fully-populated
// config.toml (tsnet: true, tsnet_authkey: literal) boots without any CLI flags.
// tsnet bring-up is mocked — no real Tailscale node is started.
func TestBareServeBootsFromYaml(t *testing.T) {
	swapTsnetStart(t, mockTsnetStart(t))

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Write config.toml with all required fields.
	configDir := filepath.Join(tmpHome, ".config", "linkari")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configTOML := `[server]
token = "yaml-boot-token"
tsnet = true
tsnet_authkey = "test-authkey-from-yaml"
tsnet_hostname = "linkari-test"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(configTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LINKARI_QUEUE_DB", filepath.Join(tmpHome, "queue.db"))

	port := findFreePort(t)
	cmd := serveCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--port", strconv.Itoa(port)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- cmd.ExecuteContext(ctx) }()

	waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/healthz", port), 5*time.Second)

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serve exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down within 5s")
	}
}

func TestBareServePropagatesTsnetClientSecret(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("LINKARI_QUEUE_DB", filepath.Join(tmpHome, "queue.db"))

	configDir := filepath.Join(tmpHome, ".config", "linkari")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configTOML := `[server]
token = "yaml-boot-token"
tsnet = true
tsnet_authkey = "test-authkey-from-yaml"
tsnet_client_secret = "test-client-secret-from-yaml"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(configTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	var captured TsnetConfig
	swapTsnetStart(t, func(_ context.Context, cfg TsnetConfig) (net.Listener, func() error, string, error) {
		captured = cfg
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, "", err
		}
		return ln, func() error { return ln.Close() }, "linkari.test.ts.net", nil
	})

	port := findFreePort(t)
	cmd := serveCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--port", strconv.Itoa(port)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- cmd.ExecuteContext(ctx) }()
	waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/healthz", port), 5*time.Second)
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serve exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down within 5s")
	}
	if captured.ClientSecret != "test-client-secret-from-yaml" {
		t.Fatalf("ClientSecret = %q, want %q", captured.ClientSecret, "test-client-secret-from-yaml")
	}
}

// TestCanonicalCommandByteIdentical asserts that the EPIC-047 canonical
// invocation still works after the EPIC-048 default flip:
//
//	linkari serve --tsnet --tsnet-authkey $KEY --token $T --notify-min-score 10 --debug
//
// Assertions (per blockers-to-95 spec):
//  1. Exit 0 within 2s of context cancel.
//  2. Local listener binds on configured port.
//
// tsnet is mocked; no real Tailscale node required.
func TestCanonicalCommandByteIdentical(t *testing.T) {
	swapTsnetStart(t, mockTsnetStart(t))

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("LINKARI_QUEUE_DB", filepath.Join(tmpHome, "queue.db"))

	port := findFreePort(t)
	cmd := serveCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--tsnet",
		"--tsnet-authkey", "canonical-test-authkey",
		"--token", "canonical-test-token",
		"--notify-min-score", "10",
		"--debug",
		"--port", strconv.Itoa(port),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- cmd.ExecuteContext(ctx) }()

	waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/healthz", port), 5*time.Second)

	start := time.Now()
	cancel()
	select {
	case err := <-errCh:
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("serve exited with error: %v", err)
		}
		if elapsed > 2*time.Second {
			t.Errorf("shutdown took %v, want ≤ 2s", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down within 5s after context cancel")
	}
}
