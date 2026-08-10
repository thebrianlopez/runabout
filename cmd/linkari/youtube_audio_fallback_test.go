// EPIC-108 M1: contract tests for F2 — audio fallback concurrency semaphore,
// wall-clock timeout, and dead-letter requeue.
// Behavioral code was added prior to this file; all tests pass in M1.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func newAudioFallbackEventLogger(t *testing.T) (*EventLogger, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "events.jsonl")
	el, err := NewEventLogger(p)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	t.Cleanup(func() { el.Close() })
	return el, p
}

func readAudioFallbackLog(t *testing.T, el *EventLogger, path string) string {
	t.Helper()
	el.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	return string(raw)
}

// installNoSubtitlesStub makes execYtdlp return a "no subtitles" error to
// force the audio fallback path in transcribeYouTubeAsync.
func installNoSubtitlesStub(t *testing.T) *ytDeps {
	t.Helper()
	ytdlpStub := func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return "", ytVideoMeta{}, fmt.Errorf("yt-dlp: no subtitles found for test-url")
	}
	deps := &ytDeps{Ytdlp: ytdlpStub}
	return deps
}

// installAudioDownloadStub makes execYtdlpAudio return a real temp file so
// os.RemoveAll(filepath.Dir(audioPath)) in ytAudioFallback cleans up safely.
func installAudioDownloadStub(t *testing.T) {
	t.Helper()
	prev := execYtdlpAudio
	execYtdlpAudio = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		dir := t.TempDir()
		p := filepath.Join(dir, "audio.m4a")
		if err := os.WriteFile(p, []byte("fake audio"), 0o644); err != nil {
			return "", ytVideoMeta{}, fmt.Errorf("stub: write fake audio: %w", err)
		}
		return p, ytVideoMeta{Title: "Test Video", ID: "testv1", Duration: 60}, nil
	}
	t.Cleanup(func() { execYtdlpAudio = prev })
}

// installFfmpegNoopStub makes execFfmpegConvert a no-op.
func installFfmpegNoopStub(t *testing.T) {
	t.Helper()
	prev := execFfmpegConvert
	execFfmpegConvert = func(_ context.Context, _, _ string) error { return nil }
	t.Cleanup(func() { execFfmpegConvert = prev })
}

// installNormalizeURLNoopStub makes execNormalizeURL pass through the URL unchanged.
func installNormalizeURLNoopStub(t *testing.T) {
	t.Helper()
	prev := execNormalizeURL
	execNormalizeURL = func(_ context.Context, u string) (string, error) { return u, nil }
	t.Cleanup(func() { execNormalizeURL = prev })
}

// enqueueAudioFallbackReq enqueues a minimal URL share row and returns the req with QueueRowID set.
func enqueueAudioFallbackReq(t *testing.T, q *Queue, url string) ShareRequest {
	t.Helper()
	req := ShareRequest{Type: "url", Action: "vnote_auto", URL: url, Profile: "test"}
	rowID, err := q.Enqueue(&req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	req.QueueRowID = rowID
	return req
}

// ─── CT-1: Semaphore cap=1 — only one whisper-cli active at a time ────────────

// TestAudioFallback_CT1_SemaphoreCap1 verifies that concurrent YouTube shares
// that fall back to audio transcription run at most one whisper-cli at a time.
// Contract (FDD §5): "max_concurrency = 1 — only one whisper-cli process may
// run at a time; enforced by semaphore before exec."
//
// RG-F2-1: guards against regression where concurrent audio jobs exhaust RAM.
func TestAudioFallback_CT1_SemaphoreCap1(t *testing.T) {
	prevSem := ytAudioSem
	ytAudioSem = make(chan struct{}, 1)
	t.Cleanup(func() { ytAudioSem = prevSem })

	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = true
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	deps := installNoSubtitlesStub(t)
	installAudioDownloadStub(t)
	installFfmpegNoopStub(t)
	installNormalizeURLNoopStub(t)
	installPushStub(t, nil)

	var activeCount, peakActive int32
	prevWhisper := execWhisper
	execWhisper = func(ctx context.Context, _, _ string) (string, error) {
		cur := atomic.AddInt32(&activeCount, 1)
		defer atomic.AddInt32(&activeCount, -1)
		for {
			old := atomic.LoadInt32(&peakActive)
			if cur <= old || atomic.CompareAndSwapInt32(&peakActive, old, cur) {
				break
			}
		}
		select {
		case <-time.After(200 * time.Millisecond):
			return "whisper transcript text", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	t.Cleanup(func() { execWhisper = prevWhisper })

	q := newTestQueue(t)
	el, evtPath := newAudioFallbackEventLogger(t)

	req1 := enqueueAudioFallbackReq(t, q, "https://www.youtube.com/watch?v=audio1")
	req2 := enqueueAudioFallbackReq(t, q, "https://www.youtube.com/watch?v=audio2")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		transcribeYouTubeAsync(req1, q, "yt-dlp", el, "", &ServerConfig{TranscriptsDir: transcriptDir}, deps)
	}()
	go func() {
		defer wg.Done()
		transcribeYouTubeAsync(req2, q, "yt-dlp", el, "", &ServerConfig{TranscriptsDir: transcriptDir}, deps)
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("CT-1: goroutines timed out")
	}

	raw := readAudioFallbackLog(t, el, evtPath)

	peak := atomic.LoadInt32(&peakActive)
	if peak > 1 {
		t.Errorf("CT-1: peak concurrent whisper-cli invocations = %d, want ≤ 1 (semaphore cap=1)", peak)
	}
	if !strings.Contains(raw, `"yt_audio_queued_pending_semaphore"`) {
		t.Errorf("CT-1: yt_audio_queued_pending_semaphore not in events (second job must emit it):\n%s", raw)
	}
}

// ─── CT-2: Timeout emits yt_audio_timeout; yt_audio_fallback_failed NOT emitted ─

// TestAudioFallback_CT2_TimeoutEmitsEvent verifies that a whisper-cli run
// exceeding the deadline emits yt_audio_timeout and does NOT emit
// yt_audio_fallback_failed (which is reserved for non-timeout errors).
// Contract (FDD §5 error taxonomy): yt_audio_timeout and yt_audio_fallback_failed
// are distinct error classes; a timeout must not double-emit as a generic failure.
//
// RG-F2-2: guards against regression where timeout is silently swallowed or
// misclassified as yt_audio_fallback_failed.
func TestAudioFallback_CT2_TimeoutEmitsEvent(t *testing.T) {
	prevTimeout := ytWhisperTimeoutSecs
	ytWhisperTimeoutSecs = 1 // 1-second deadline
	t.Cleanup(func() { ytWhisperTimeoutSecs = prevTimeout })

	// With default retries, a timeout → EnqueueAudioRetry → row = pending.
	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = true
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	deps := installNoSubtitlesStub(t)
	installAudioDownloadStub(t)
	installFfmpegNoopStub(t)
	installNormalizeURLNoopStub(t)

	prevWhisper := execWhisper
	execWhisper = func(ctx context.Context, _, _ string) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "", fmt.Errorf("stub: should not reach here")
		}
	}
	t.Cleanup(func() { execWhisper = prevWhisper })

	q := newTestQueue(t)
	el, evtPath := newAudioFallbackEventLogger(t)

	req := enqueueAudioFallbackReq(t, q, "https://www.youtube.com/watch?v=timeout1")
	start := time.Now()
	transcribeYouTubeAsync(req, q, "yt-dlp", el, "", &ServerConfig{TranscriptsDir: transcriptDir}, deps)
	elapsed := time.Since(start)

	raw := readAudioFallbackLog(t, el, evtPath)

	if !strings.Contains(raw, `"yt_audio_timeout"`) {
		t.Errorf("CT-2: yt_audio_timeout not in events:\n%s", raw)
	}
	if strings.Contains(raw, `"yt_audio_fallback_failed"`) {
		t.Errorf("CT-2: yt_audio_fallback_failed must NOT be emitted on timeout (use yt_audio_timeout):\n%s", raw)
	}
	if elapsed > 3*time.Second {
		t.Errorf("CT-2: elapsed = %v, want < 3s (timeout must fire within deadline)", elapsed)
	}

	row, err := q.GetByID(req.QueueRowID)
	if err != nil {
		t.Fatalf("CT-2: GetByID: %v", err)
	}
	if row == nil {
		t.Fatal("CT-2: row is nil")
	}
	// Timeout with retries remaining → row enters dead-letter (status=pending, retry_after set).
	if row.Status == "failed" {
		t.Errorf("CT-2: row.Status = failed; timeout with remaining retries must enter dead-letter (pending), not fail the row")
	}
}

// ─── CT-3: Dead-letter on failure — yt_audio_fallback_failed emitted, row = pending ─

// TestAudioFallback_CT3_DeadLetterOnFailure verifies that a whisper-cli non-zero
// exit emits yt_audio_fallback_failed and places the row in dead-letter (pending)
// for retry rather than marking it permanently failed.
// Contract (FDD §5): "retry_policy = dead-letter requeue with exponential backoff
// on yt_audio_fallback_failed; max 3 attempts before terminal failure."
//
// RG-F2-3: guards against regression where a first whisper failure permanently
// fails the row instead of queuing a retry.
func TestAudioFallback_CT3_DeadLetterOnFailure(t *testing.T) {
	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = true
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	deps := installNoSubtitlesStub(t)
	installAudioDownloadStub(t)
	installFfmpegNoopStub(t)
	installNormalizeURLNoopStub(t)

	prevWhisper := execWhisper
	execWhisper = func(_ context.Context, _, _ string) (string, error) {
		return "", fmt.Errorf("whisper-cli: exit status 1: decode error")
	}
	t.Cleanup(func() { execWhisper = prevWhisper })

	q := newTestQueue(t)
	el, evtPath := newAudioFallbackEventLogger(t)

	req := enqueueAudioFallbackReq(t, q, "https://www.youtube.com/watch?v=fail1")
	transcribeYouTubeAsync(req, q, "yt-dlp", el, "", &ServerConfig{TranscriptsDir: transcriptDir}, deps)

	raw := readAudioFallbackLog(t, el, evtPath)

	if !strings.Contains(raw, `"yt_audio_fallback_failed"`) {
		t.Errorf("CT-3: yt_audio_fallback_failed not in events:\n%s", raw)
	}

	row, err := q.GetByID(req.QueueRowID)
	if err != nil {
		t.Fatalf("CT-3: GetByID: %v", err)
	}
	if row == nil {
		t.Fatal("CT-3: row is nil")
	}
	// First failure with retries remaining → dead-letter (status=pending, not failed).
	if row.Status == "failed" {
		t.Errorf("CT-3: row.Status = failed; first failure must enter dead-letter (status=pending) not permanently fail")
	}
}

// ─── CT-4: Terminal failure after max_retries ─────────────────────────────────

// TestAudioFallback_CT4_TerminalFailure verifies that when max_retries is
// exhausted, yt_audio_terminal_failed is emitted and the row is permanently failed.
// Contract (FDD §5): "max 3 attempts before terminal failure."
//
// RG-F2-4: guards against regression where a row retries indefinitely.
func TestAudioFallback_CT4_TerminalFailure(t *testing.T) {
	prevMaxRetries := ytAudioMaxRetries
	ytAudioMaxRetries = 0 // any failure → immediate terminal
	t.Cleanup(func() { ytAudioMaxRetries = prevMaxRetries })

	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = true
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	deps := installNoSubtitlesStub(t)
	installAudioDownloadStub(t)
	installFfmpegNoopStub(t)
	installNormalizeURLNoopStub(t)

	prevWhisper := execWhisper
	execWhisper = func(_ context.Context, _, _ string) (string, error) {
		return "", fmt.Errorf("whisper-cli: exit status 1: persistent failure")
	}
	t.Cleanup(func() { execWhisper = prevWhisper })

	q := newTestQueue(t)
	el, evtPath := newAudioFallbackEventLogger(t)

	req := enqueueAudioFallbackReq(t, q, "https://www.youtube.com/watch?v=terminal1")
	transcribeYouTubeAsync(req, q, "yt-dlp", el, "", &ServerConfig{TranscriptsDir: transcriptDir}, deps)

	raw := readAudioFallbackLog(t, el, evtPath)

	if !strings.Contains(raw, `"yt_audio_terminal_failed"`) {
		t.Errorf("CT-4: yt_audio_terminal_failed not in events:\n%s", raw)
	}

	row, err := q.GetByID(req.QueueRowID)
	if err != nil {
		t.Fatalf("CT-4: GetByID: %v", err)
	}
	if row == nil {
		t.Fatal("CT-4: row is nil")
	}
	if row.Status != "failed" {
		t.Errorf("CT-4: row.Status = %q, want failed after terminal failure", row.Status)
	}
}

// ─── CT-5: Semaphore released on error — next job not blocked ─────────────────

// TestAudioFallback_CT5_SemaphoreReleasedOnError verifies that a failed audio
// fallback job releases the semaphore so the next job can proceed without blocking.
// Contract (FDD §5): semaphore must be released on both success and failure paths.
//
// RG-F2-5: guards against regression where a failed job leaks the semaphore,
// causing all subsequent audio fallback jobs to deadlock.
func TestAudioFallback_CT5_SemaphoreReleasedOnError(t *testing.T) {
	prevSem := ytAudioSem
	ytAudioSem = make(chan struct{}, 1)
	t.Cleanup(func() { ytAudioSem = prevSem })

	prevMaxRetries := ytAudioMaxRetries
	ytAudioMaxRetries = 0 // fail fast to terminal; no retry loop
	t.Cleanup(func() { ytAudioMaxRetries = prevMaxRetries })

	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = true
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	deps := installNoSubtitlesStub(t)
	installAudioDownloadStub(t)
	installFfmpegNoopStub(t)
	installNormalizeURLNoopStub(t)
	installPushStub(t, nil)

	var callCount int32
	prevWhisper := execWhisper
	execWhisper = func(_ context.Context, _, _ string) (string, error) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return "", fmt.Errorf("whisper-cli: exit status 1: first job fails")
		}
		return "transcript from second job", nil
	}
	t.Cleanup(func() { execWhisper = prevWhisper })

	q := newTestQueue(t)
	el, evtPath := newAudioFallbackEventLogger(t)

	// First job: fails; must release semaphore.
	req1 := enqueueAudioFallbackReq(t, q, "https://www.youtube.com/watch?v=sem1")
	transcribeYouTubeAsync(req1, q, "yt-dlp", el, "", &ServerConfig{TranscriptsDir: transcriptDir}, deps)

	// Second job: should run immediately — not block on semaphore.
	prevMaxRetries2 := ytAudioMaxRetries
	ytAudioMaxRetries = 3 // allow second job to succeed normally
	t.Cleanup(func() { ytAudioMaxRetries = prevMaxRetries2 })

	req2 := enqueueAudioFallbackReq(t, q, "https://www.youtube.com/watch?v=sem2")
	secondStart := time.Now()
	transcribeYouTubeAsync(req2, q, "yt-dlp", el, "", &ServerConfig{TranscriptsDir: transcriptDir}, deps)
	secondElapsed := time.Since(secondStart)

	_ = readAudioFallbackLog(t, el, evtPath)

	// Second job should not have been stuck waiting for a leaked semaphore.
	// Generous threshold: 3s covers the audio download + whisper stubs.
	if secondElapsed > 3*time.Second {
		t.Errorf("CT-5: second job took %v, want < 3s (semaphore should have been released by first job)", secondElapsed)
	}
	if n := atomic.LoadInt32(&callCount); n < 2 {
		t.Errorf("CT-5: whisper called %d time(s), want ≥ 2 (both jobs must run)", n)
	}
}
