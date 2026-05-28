//go:build integration

package main

// EPIC-038 M9: Integration tests for the container execution pipeline.
//
// These tests require a running Lima or OrbStack VM with:
//   - containerd with gVisor (io.containerd.runsc.v1) registered
//   - Container images built and pushed to the local registry
//   - LINKARI_RUNTIME_SOCKET pointing to the containerd socket inside the VM
//
// Run with:
//
//	LINKARI_RUNTIME_SOCKET=/var/run/lima/lima-gvisor/containerd.sock \
//	  go test -v -tags=integration -run TestContainer ./cmd/linkari/...
//
// In CI: skipped automatically when LINKARI_RUNTIME_SOCKET is unset or the
// socket file does not exist on disk. Use make integration-test (which calls
// lima-test first) for a fully orchestrated run.

import (
	"context"
	"os"
	"testing"
	"time"
)

// skipIfNoSocket skips the test when the CRI socket is unavailable.
// This keeps the integration suite from failing in environments without Lima.
func skipIfNoSocket(t *testing.T) SandboxConfig {
	t.Helper()
	sock := os.Getenv("LINKARI_RUNTIME_SOCKET")
	if sock == "" {
		t.Skip("LINKARI_RUNTIME_SOCKET not set — skipping integration test")
	}
	if _, err := os.Stat(sock); err != nil {
		t.Skipf("CRI socket %s not reachable (%v) — skipping integration test", sock, err)
	}
	return SandboxConfig{
		Enabled:       true,
		RuntimeSocket: sock,
		ImageRegistry: "ghcr.io/blo-grindr/linkari",
		MemoryLimitMB: 512,
		CPULimitCores: 1.0,
	}
}

// TestContainerRuntimePing verifies that ContainerRuntime.Ping succeeds when
// the CRI socket is present and containerd is running inside the Lima VM.
func TestContainerRuntimePing(t *testing.T) {
	cfg := skipIfNoSocket(t)
	cr := &ContainerRuntime{cfg: cfg}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cr.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
}

// TestContainerInvokeFFmpeg runs a real ffmpeg container via gVisor and
// verifies that a minimal WAV file is produced from a synthetic input.
func TestContainerInvokeFFmpeg(t *testing.T) {
	cfg := skipIfNoSocket(t)
	cr := &ContainerRuntime{cfg: cfg}

	// Create a minimal 1-second silent WAV as input.
	inputPath := t.TempDir() + "/test_input.wav"
	outputPath := t.TempDir() + "/test_output.wav"
	if err := writeSilentWAV(inputPath, 1); err != nil {
		t.Fatalf("create input wav: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := cr.InvokeFFmpeg(ctx, inputPath, outputPath); err != nil {
		t.Fatalf("InvokeFFmpeg: %v", err)
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("output not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("output WAV is empty")
	}
	t.Logf("ffmpeg output: %d bytes", info.Size())
}

// TestContainerRuntimeFallback verifies that NewExecutionRuntimeWithPing
// returns a LocalRuntime when the socket path does not exist.
func TestContainerRuntimeFallback(t *testing.T) {
	cfg := SandboxConfig{
		Enabled:       true,
		RuntimeSocket: "/tmp/linkari-nonexistent-socket-test.sock",
		ImageRegistry: "ghcr.io/blo-grindr/linkari",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rt := NewExecutionRuntimeWithPing(ctx, cfg)
	if _, ok := rt.(*LocalRuntime); !ok {
		t.Fatalf("expected LocalRuntime fallback, got %T", rt)
	}
}

// writeSilentWAV writes a minimal valid WAV file of durationSec seconds at
// 16kHz mono 16-bit PCM. Used to produce synthetic test input for ffmpeg.
func writeSilentWAV(path string, durationSec int) error {
	const sampleRate = 16000
	const bitDepth = 16
	const channels = 1

	numSamples := sampleRate * durationSec
	dataSize := numSamples * channels * (bitDepth / 8)
	fileSize := 36 + dataSize

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	writeLE := func(v uint32, n int) {
		for i := 0; i < n; i++ {
			f.Write([]byte{byte(v >> (8 * i))}) //nolint:errcheck
		}
	}
	writeStr := func(s string) { f.Write([]byte(s)) } //nolint:errcheck

	// RIFF header
	writeStr("RIFF")
	writeLE(uint32(fileSize), 4)
	writeStr("WAVE")
	// fmt chunk
	writeStr("fmt ")
	writeLE(16, 4)                                     // chunk size
	writeLE(1, 2)                                      // PCM
	writeLE(channels, 2)                               // channels
	writeLE(sampleRate, 4)                             // sample rate
	writeLE(uint32(sampleRate*channels*bitDepth/8), 4) // byte rate
	writeLE(uint32(channels*bitDepth/8), 2)            // block align
	writeLE(bitDepth, 2)                               // bits per sample
	// data chunk
	writeStr("data")
	writeLE(uint32(dataSize), 4)
	// Silent PCM samples (all zeros)
	silence := make([]byte, dataSize)
	_, err = f.Write(silence)
	return err
}
