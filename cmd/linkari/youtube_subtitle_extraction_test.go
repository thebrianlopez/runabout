// EPIC-109 M1: contract tests for F1 — yt-dlp subtitle extraction.
// All tests reference extractYTSubtitles (stubbed; implemented in M2) and the
// dead-letter retry path in scoreYouTubeAsync (wired in M3).
// These tests compile and fail in M1; they pass once M2/M3 are implemented.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func newSubtitleEventLogger(t *testing.T) (*EventLogger, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "events.jsonl")
	el, err := NewEventLogger(p)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	t.Cleanup(func() { el.Close() })
	return el, p
}

func readEventLog(t *testing.T, el *EventLogger, path string) string {
	t.Helper()
	el.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	return string(raw)
}

// ─── CT-1: yt_subtitles_ok emitted when subtitle file present ────────────────

func TestSubtitleExtraction_CT1_SubtitlesFound(t *testing.T) {
	el, evtPath := newSubtitleEventLogger(t)

	orig := execYtdlp
	defer func() { execYtdlp = orig }()
	execYtdlp = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return "This is the subtitle text.", ytVideoMeta{Title: "Test", ID: "abc123", Duration: 120, SubtitleType: "auto"}, nil
	}

	subtitleEvent, transcript, meta, err := extractYTSubtitles(context.Background(), "yt-dlp", "https://www.youtube.com/watch?v=abc123", 1, el)

	if err != nil {
		t.Fatalf("CT-1: unexpected error: %v", err)
	}
	if subtitleEvent != "yt_subtitles_ok" {
		t.Errorf("CT-1: subtitleEvent = %q, want yt_subtitles_ok", subtitleEvent)
	}
	if transcript == "" {
		t.Error("CT-1: transcript is empty, want non-empty")
	}
	if meta.ID != "abc123" {
		t.Errorf("CT-1: meta.ID = %q, want abc123", meta.ID)
	}

	raw := readEventLog(t, el, evtPath)
	if !strings.Contains(raw, `"yt_subtitles_ok"`) {
		t.Errorf("CT-1: yt_subtitles_ok not in events:\n%s", raw)
	}
	if strings.Contains(raw, `"yt_dlp_failed"`) {
		t.Errorf("CT-1: yt_dlp_failed must NOT be emitted on success:\n%s", raw)
	}
}

// ─── CT-2: yt_no_subtitles is a normal signal (not an error) ─────────────────

func TestSubtitleExtraction_CT2_NoSubtitles(t *testing.T) {
	el, evtPath := newSubtitleEventLogger(t)

	orig := execYtdlp
	defer func() { execYtdlp = orig }()
	execYtdlp = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return "", ytVideoMeta{}, fmt.Errorf("yt-dlp: no subtitles found for test-url")
	}

	subtitleEvent, transcript, _, err := extractYTSubtitles(context.Background(), "yt-dlp", "https://www.youtube.com/watch?v=nosubs", 2, el)

	// yt_no_subtitles is a normal signal — caller triggers F2, no dead-letter.
	if err != nil {
		t.Errorf("CT-2: err = %v, want nil (yt_no_subtitles must not be an error)", err)
	}
	if subtitleEvent != "yt_no_subtitles" {
		t.Errorf("CT-2: subtitleEvent = %q, want yt_no_subtitles", subtitleEvent)
	}
	if transcript != "" {
		t.Errorf("CT-2: transcript = %q, want empty for no-subtitles", transcript)
	}

	raw := readEventLog(t, el, evtPath)
	if !strings.Contains(raw, `"yt_no_subtitles"`) {
		t.Errorf("CT-2: yt_no_subtitles not in events:\n%s", raw)
	}
	if strings.Contains(raw, `"yt_dlp_failed"`) {
		t.Errorf("CT-2: yt_dlp_failed must NOT be emitted for yt_no_subtitles:\n%s", raw)
	}
}

// ─── CT-3: yt_dlp_failed emitted on non-zero exit ────────────────────────────

func TestSubtitleExtraction_CT3_YtDlpFailure(t *testing.T) {
	el, evtPath := newSubtitleEventLogger(t)

	orig := execYtdlp
	defer func() { execYtdlp = orig }()
	execYtdlp = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return "", ytVideoMeta{}, fmt.Errorf("yt-dlp: exit status 1: ERROR: Unable to extract video data")
	}

	subtitleEvent, _, _, err := extractYTSubtitles(context.Background(), "yt-dlp", "https://www.youtube.com/watch?v=fail", 3, el)

	if err == nil {
		t.Error("CT-3: expected non-nil error for yt_dlp_failed")
	}
	if subtitleEvent != "yt_dlp_failed" {
		t.Errorf("CT-3: subtitleEvent = %q, want yt_dlp_failed", subtitleEvent)
	}

	raw := readEventLog(t, el, evtPath)
	if !strings.Contains(raw, `"yt_dlp_failed"`) {
		t.Errorf("CT-3: yt_dlp_failed not in events:\n%s", raw)
	}
	if !strings.Contains(raw, `"subtitle"`) {
		t.Errorf("CT-3: expected step=subtitle in yt_dlp_failed event:\n%s", raw)
	}
}

// ─── CT-4: timeout → yt_dlp_failed with error_reason=timeout ─────────────────

func TestSubtitleExtraction_CT4_Timeout(t *testing.T) {
	el, evtPath := newSubtitleEventLogger(t)

	orig := execYtdlp
	defer func() { execYtdlp = orig }()
	execYtdlp = func(ctx context.Context, _, _ string) (string, ytVideoMeta, error) {
		select {
		case <-ctx.Done():
			return "", ytVideoMeta{}, ctx.Err()
		case <-time.After(5 * time.Second):
			return "", ytVideoMeta{}, fmt.Errorf("stub: should not reach here")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	subtitleEvent, _, _, err := extractYTSubtitles(ctx, "yt-dlp", "https://www.youtube.com/watch?v=timeout", 4, el)

	if err == nil {
		t.Error("CT-4: expected error on timeout")
	}
	if subtitleEvent != "yt_dlp_failed" {
		t.Errorf("CT-4: subtitleEvent = %q, want yt_dlp_failed", subtitleEvent)
	}

	raw := readEventLog(t, el, evtPath)
	if !strings.Contains(raw, `"yt_dlp_failed"`) {
		t.Errorf("CT-4: yt_dlp_failed not in events:\n%s", raw)
	}
	if !strings.Contains(raw, "timeout") {
		t.Errorf("CT-4: expected error_reason=timeout in event:\n%s", raw)
	}
}

// ─── CT-5: dead-letter retry — yt_dlp_failed row retried; succeeds on 2nd ───
// Tests via scoreYouTubeAsync once M2/M3 wire extractYTSubtitles + retry.

func TestSubtitleExtraction_CT5_DeadLetterRetrySucceeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installTestProfileDir(t, "eng")

	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = false
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	prevSubRetries := ytSubtitleMaxRetries
	ytSubtitleMaxRetries = 2
	t.Cleanup(func() { ytSubtitleMaxRetries = prevSubRetries })

	var callCount int32
	orig := execYtdlp
	defer func() { execYtdlp = orig }()
	execYtdlp = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return "", ytVideoMeta{}, fmt.Errorf("yt-dlp: exit status 1: transient network error")
		}
		return "Subtitle text on retry.", ytVideoMeta{Title: "Retry Video", ID: "retry1", Duration: 60, SubtitleType: "auto"}, nil
	}

	prevHaikuJSON := execHaikuJSON
	execHaikuJSON = func(_ context.Context, _, _, _ string) ([]byte, error) {
		v := TriageVerdict{Score: 70, Verdict: "interesting", Tags: "test", RubricScores: map[string]int{"overall": 70}}
		return json.Marshal(v)
	}
	t.Cleanup(func() { execHaikuJSON = prevHaikuJSON })

	q := newTestQueue(t)
	req := ShareRequest{
		Type:    "url",
		URL:     "https://www.youtube.com/watch?v=retryme",
		Profile: "eng",
	}
	id, err := q.Enqueue(&req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}
	req.QueueRowID = id

	done := make(chan struct{})
	go func() {
		defer close(done)
		scoreYouTubeAsync(req, q, "yt-dlp", nil, "")
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("CT-5: scoreYouTubeAsync timed out")
	}

	var status string
	if err := q.db.QueryRow("SELECT status FROM queue WHERE id=?", id).Scan(&status); err != nil {
		t.Fatalf("CT-5: query status: %v", err)
	}
	// M3: row should be scored/archived after retry succeeds.
	// M1: row will be "failed" (no retry yet) — this test fails until M3.
	if status != "scored" && status != "archived" {
		t.Errorf("CT-5: status = %q, want scored or archived after subtitle retry", status)
	}
}

// ─── CT-6: terminal failure after max_retries ─────────────────────────────────
// Tests via scoreYouTubeAsync once M3 wires subtitle dead-letter retry.

func TestSubtitleExtraction_CT6_TerminalFailure(t *testing.T) {
	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = false
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	prevSubRetries := ytSubtitleMaxRetries
	ytSubtitleMaxRetries = 2
	t.Cleanup(func() { ytSubtitleMaxRetries = prevSubRetries })

	orig := execYtdlp
	defer func() { execYtdlp = orig }()
	execYtdlp = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return "", ytVideoMeta{}, fmt.Errorf("yt-dlp: exit status 1: persistent failure")
	}

	evtPath := filepath.Join(t.TempDir(), "events.jsonl")
	el, err := NewEventLogger(evtPath)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	defer el.Close()

	q := newTestQueue(t)
	req := ShareRequest{
		Type:    "url",
		URL:     "https://www.youtube.com/watch?v=alwaysfail",
		Profile: "eng",
	}
	id, enqErr := q.Enqueue(&req)
	if enqErr != nil {
		t.Fatalf("Enqueue: %v", enqErr)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}
	req.QueueRowID = id

	done := make(chan struct{})
	go func() {
		defer close(done)
		scoreYouTubeAsync(req, q, "yt-dlp", el, "")
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("CT-6: scoreYouTubeAsync timed out")
	}

	// M3: yt_dlp_terminal_failed emitted after max_retries exhausted.
	// M1: event not present — this assertion fails until M3.
	raw := readEventLog(t, el, evtPath)
	if !strings.Contains(raw, `"yt_dlp_terminal_failed"`) {
		t.Errorf("CT-6: yt_dlp_terminal_failed not in events (expected after max_retries):\n%s", raw)
	}
}

// ─── CT-7: subtitle timeout config is wired (ytSubtitleTimeoutSecs respected) ─

func TestSubtitleExtraction_CT7_TimeoutConfigWired(t *testing.T) {
	prevSecs := ytSubtitleTimeoutSecs
	ytSubtitleTimeoutSecs = 5 // generous — stub completes in 10ms
	t.Cleanup(func() { ytSubtitleTimeoutSecs = prevSecs })

	el, _ := newSubtitleEventLogger(t)

	orig := execYtdlp
	defer func() { execYtdlp = orig }()
	execYtdlp = func(ctx context.Context, _, _ string) (string, ytVideoMeta, error) {
		// Simulate a 10ms extraction — well under 5s configured limit.
		select {
		case <-time.After(10 * time.Millisecond):
			return "Subtitle content.", ytVideoMeta{Title: "Config Test", ID: "cfg1"}, nil
		case <-ctx.Done():
			return "", ytVideoMeta{}, ctx.Err()
		}
	}

	subtitleEvent, transcript, _, err := extractYTSubtitles(context.Background(), "yt-dlp", "https://www.youtube.com/watch?v=cfg1", 7, el)

	// Must succeed — 10ms is well within the 5s config limit.
	if err != nil {
		t.Errorf("CT-7: unexpected error with generous timeout config: %v", err)
	}
	if subtitleEvent != "yt_subtitles_ok" {
		t.Errorf("CT-7: subtitleEvent = %q, want yt_subtitles_ok", subtitleEvent)
	}
	if transcript == "" {
		t.Error("CT-7: transcript empty, want non-empty")
	}
}

// ─── CT-8: concurrency — at most max_concurrency=3 subtitle jobs simultaneously ─

func TestSubtitleExtraction_CT8_ConcurrencyCap3(t *testing.T) {
	prevCap := cap(ytSubtitleSem)
	ytSubtitleSem = make(chan struct{}, 3)
	t.Cleanup(func() { ytSubtitleSem = make(chan struct{}, prevCap) })

	var active int32
	var peakActive int32

	orig := execYtdlp
	defer func() { execYtdlp = orig }()
	execYtdlp = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		cur := atomic.AddInt32(&active, 1)
		defer atomic.AddInt32(&active, -1)
		// Record peak.
		for {
			old := atomic.LoadInt32(&peakActive)
			if cur <= old || atomic.CompareAndSwapInt32(&peakActive, old, cur) {
				break
			}
		}
		time.Sleep(80 * time.Millisecond) // hold slot long enough to measure peak
		return "Subtitle text.", ytVideoMeta{ID: fmt.Sprintf("vid-%d", cur)}, nil
	}

	const numJobs = 5
	var wg [numJobs]chan struct{}
	for i := range wg {
		wg[i] = make(chan struct{})
	}

	evtPath := filepath.Join(t.TempDir(), "events.jsonl")
	el, err := NewEventLogger(evtPath)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	defer el.Close()

	for i := 0; i < numJobs; i++ {
		ch := wg[i]
		url := fmt.Sprintf("https://www.youtube.com/watch?v=job%d", i)
		go func() {
			defer close(ch)
			extractYTSubtitles(context.Background(), "yt-dlp", url, int64(i), el) //nolint:errcheck
		}()
	}

	for _, ch := range wg {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatal("CT-8: a job timed out")
		}
	}

	peak := atomic.LoadInt32(&peakActive)
	// M2: peak must be ≤ 3 (semaphore cap).
	// M1: no semaphore — peak will be 5 — this assertion fails until M2.
	if peak > 3 {
		t.Errorf("CT-8: peak concurrent subtitle jobs = %d, want ≤ 3 (semaphore cap)", peak)
	}
}
