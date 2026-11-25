// EPIC-272: integration tests for the local-only serve path.
//
// These tests start a real linkari serve process (using the cobra RunE path)
// and verify end-to-end boot/shutdown behavior. They do NOT run in parallel
// with each other because they mutate process env (HOME, LINKARI_*).
//
// EPIC-048 history: this file previously covered the tsnet default-flip and
// the fallback-to-local rule; that layer was deleted in EPIC-272 and only
// the bare-serve assertions survived (the k8s deployment depends on them).
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

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

// runServeExecute runs a serve command in a goroutine and returns a channel
// carrying its error. Cancel the context to shut the server down.
func runServeExecute(t *testing.T, ctx context.Context, cmd *cobra.Command) chan error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- cmd.ExecuteContext(ctx) }()
	return errCh
}

// awaitShutdown asserts a clean exit within 5s of context cancellation.
func awaitShutdown(t *testing.T, errCh chan error) {
	t.Helper()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serve exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down within 5s after context cancel")
	}
}

// ── Integration tests ────────────────────────────────────────────────────────

// TestBareServeBootsWithoutYaml asserts that bare `linkari serve` on a
// machine with no server.yaml starts a local HTTP listener and exits 0 on
// context cancellation. The EPIC-048 fallback WARN assertion was deleted
// with the tsnet layer (EPIC-272).
func TestBareServeBootsWithoutYaml(t *testing.T) {
	// Clean HOME  -  no server.yaml, no tsnet-era env vars.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("LINKARI_TOKEN", "test-token-bare-serve")
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

	errCh := runServeExecute(t, ctx, cmd)

	// Wait for local listener to bind.
	waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/healthz", port), 5*time.Second)

	cancel()
	awaitShutdown(t, errCh)
}

// TestBareServeBootsFromYaml asserts that `linkari serve` with a fully
// populated config.toml boots without any CLI flags.
func TestBareServeBootsFromYaml(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Write config.toml with the required fields (token is required).
	configDir := filepath.Join(tmpHome, ".config", "linkari")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configTOML := `[server]
token = "yaml-boot-token"
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

	errCh := runServeExecute(t, ctx, cmd)

	waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/healthz", port), 5*time.Second)

	cancel()
	awaitShutdown(t, errCh)
}

// TestServeLocalFlagLogsOneDeprecationLine pins the EPIC-272 M3 --local
// contract: the flag is accepted (the k8s manifest passes it) and logs
// exactly one deprecation line containing "--local is deprecated".
func TestServeLocalFlagLogsOneDeprecationLine(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("LINKARI_TOKEN", "test-token-local-noop")
	t.Setenv("LINKARI_QUEUE_DB", filepath.Join(tmpHome, "queue.db"))
	t.Setenv("LINKARI_LOCAL", "")

	var logBuf bytes.Buffer
	prevHandler := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prevHandler) })

	port := findFreePort(t)
	cmd := serveCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--local", "--port", strconv.Itoa(port)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := runServeExecute(t, ctx, cmd)
	waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/healthz", port), 5*time.Second)
	cancel()
	awaitShutdown(t, errCh)

	deprecationLines := 0
	for _, line := range bytes.Split(logBuf.Bytes(), []byte("\n")) {
		if bytes.Contains(line, []byte("--local is deprecated")) {
			deprecationLines++
		}
	}
	if deprecationLines != 1 {
		t.Fatalf("expected exactly 1 --local deprecation log line, got %d; log:\n%s",
			deprecationLines, logBuf.String())
	}
	// LINKARI_LOCAL=1 must be honored silently - no deprecation line for env.
	if bytes.Contains(logBuf.Bytes(), []byte("LINKARI_LOCAL is deprecated")) {
		t.Errorf("env-based LINKARI_LOCAL must stay silent; log:\n%s", logBuf.String())
	}
}
