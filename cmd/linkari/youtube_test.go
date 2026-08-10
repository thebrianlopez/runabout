// EPIC-009 M3: unit tests for YouTube extraction helpers.
// EPIC-003 M3: unit tests for audio fallback pipeline.
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

// TestStripSRT verifies that SRT timing markers, sequence numbers, and HTML
// tags are stripped from subtitle content.
func TestStripSRT(t *testing.T) {
	input := `1
00:00:01,000 --> 00:00:04,000
Hello, <c>world</c>!

2
00:00:04,500 --> 00:00:08,000
This is a test.

3
00:00:08,001 --> 00:00:10,000
<00:00:08.500><c>Auto-generated</c> caption.
`
	got := stripSRT(input)
	want := "Hello, world!\nThis is a test.\nAuto-generated caption."
	if got != want {
		t.Errorf("stripSRT:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestStripSRT_Empty verifies empty input yields empty output.
func TestStripSRT_Empty(t *testing.T) {
	if stripSRT("") != "" {
		t.Error("expected empty output for empty input")
	}
}

// TestIsYouTubeURL verifies the URL detection regex.
func TestIsYouTubeURL(t *testing.T) {
	yes := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtube.com/watch?v=abc",
		"https://youtu.be/dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/abc123",
		"https://www.youtube.com/live/abc123",
		"https://www.youtube.com/embed/abc123",
		"http://youtube.com/watch?v=xyz",
	}
	no := []string{
		"https://vimeo.com/123456",
		"https://github.com/golang/go",
		"https://www.nytimes.com/article",
		"https://spotify.com/track/abc",
		"https://youtube.com/channel/abc", // channel page, not a video
	}

	for _, u := range yes {
		if !isYouTubeURL(u) {
			t.Errorf("expected isYouTubeURL(%q) = true", u)
		}
	}
	for _, u := range no {
		if isYouTubeURL(u) {
			t.Errorf("expected isYouTubeURL(%q) = false", u)
		}
	}
}

// TestIsYouTubeURL_RG2_GoogleRedirect is a regression guard (RG-2) ensuring
// that isYouTubeURL matches Google redirect URLs that wrap a YouTube link in a
// query parameter. If this test starts failing, a regex tightening that broke
// the redirect case must be fixed before the change merges. EPIC-006 M1.
func TestIsYouTubeURL_RG2_GoogleRedirect(t *testing.T) {
	u := "https://www.google.com/url?sa=t&url=https://www.youtube.com/watch?v=X"
	if !isYouTubeURL(u) {
		t.Errorf("RG-2: isYouTubeURL(%q) = false, want true — Google redirect must match", u)
	}
}

// TestRunYtdlpExtract_MockExec verifies the extraction path using a mock seam.
// EPIC-090 M4: seam now returns ytVideoMeta instead of a bare title string.
func TestRunYtdlpExtract_MockExec(t *testing.T) {
	// Save and restore the real execYtdlp seam.

	wantTranscript := "Hello world"
	wantMeta := ytVideoMeta{
		Title:        "Test Video",
		ID:           "dQw4w9WgXcQ",
		Duration:     212,
		SubtitleType: "manual",
	}

	ytdlpStub := func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return wantTranscript, wantMeta, nil
	}
	deps := &ytDeps{Ytdlp: ytdlpStub}

	got, meta, err := deps.Ytdlp(context.Background(), "yt-dlp", "https://www.youtube.com/watch?v=test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantTranscript {
		t.Errorf("transcript: got %q, want %q", got, wantTranscript)
	}
	if meta.Title != wantMeta.Title {
		t.Errorf("title: got %q, want %q", meta.Title, wantMeta.Title)
	}
	if meta.ID != wantMeta.ID {
		t.Errorf("video_id: got %q, want %q", meta.ID, wantMeta.ID)
	}
	if meta.Duration != wantMeta.Duration {
		t.Errorf("duration: got %d, want %d", meta.Duration, wantMeta.Duration)
	}
	if meta.SubtitleType != wantMeta.SubtitleType {
		t.Errorf("subtitle_type: got %q, want %q", meta.SubtitleType, wantMeta.SubtitleType)
	}
}

// TestRunYtdlpExtract_ErrorPath verifies that errors from the seam propagate.
func TestRunYtdlpExtract_ErrorPath(t *testing.T) {
	ytdlpStub := func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return "", ytVideoMeta{}, fmt.Errorf("yt-dlp: no subtitles found")
	}
	deps := &ytDeps{Ytdlp: ytdlpStub}

	_, _, err := deps.Ytdlp(context.Background(), "yt-dlp", "https://www.youtube.com/watch?v=nosubs")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestDetectSubtitleType verifies manual vs auto detection from yt-dlp JSON.
// EPIC-090 M4.
func TestDetectSubtitleType(t *testing.T) {
	manualMeta := ytRawMeta{
		Subtitles: map[string]json.RawMessage{
			"en": json.RawMessage(`[]`),
		},
		AutomaticCaptions: map[string]json.RawMessage{
			"en": json.RawMessage(`[]`),
		},
	}
	if got := detectSubtitleType(manualMeta); got != "manual" {
		t.Errorf("expected manual, got %q", got)
	}

	autoOnlyMeta := ytRawMeta{
		Subtitles: map[string]json.RawMessage{},
		AutomaticCaptions: map[string]json.RawMessage{
			"en": json.RawMessage(`[]`),
		},
	}
	if got := detectSubtitleType(autoOnlyMeta); got != "auto" {
		t.Errorf("expected auto, got %q", got)
	}
}

// ─── EPIC-003 M3: audio fallback unit tests ────────────────────────────────

// installYtdlpNoSubtitlesStub makes execYtdlp return a "no subtitles" error.
func installYtdlpNoSubtitlesStub(t *testing.T) *ytDeps {
	t.Helper()
	ytdlpStub := func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return "", ytVideoMeta{}, fmt.Errorf("yt-dlp: no subtitles found for test-url")
	}
	deps := &ytDeps{Ytdlp: ytdlpStub}
	return deps
}

// installYtdlpAudioStub makes execYtdlpAudio write a fake audio file and return its path.
func installYtdlpAudioStub(t *testing.T) {
	t.Helper()
	prev := execYtdlpAudio
	execYtdlpAudio = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		dir := t.TempDir()
		p := filepath.Join(dir, "audio.m4a")
		if err := os.WriteFile(p, []byte("FAKE-M4A"), 0o644); err != nil {
			return "", ytVideoMeta{}, err
		}
		return p, ytVideoMeta{Title: "Test Video", ID: "test123", Duration: 60}, nil
	}
	t.Cleanup(func() { execYtdlpAudio = prev })
}

// installWhisperStubYT wires a whisper stub returning tx (or err if non-nil)
// into deps. EPIC-258 M2: injected via ytDeps instead of a package-var swap.
func installWhisperStubYT(t *testing.T, deps *ytDeps, tx string, err error) {
	t.Helper()
	deps.Whisper = func(_ context.Context, _, _ string) (string, error) {
		return tx, err
	}
}

// TestScoreYouTubeAsync_NoSubtitlesFallback verifies that when yt-dlp finds no
// subtitles and ytFallbackToAudio=true, scoreYouTubeAsync produces a scored row.
// EPIC-003 M3.
func TestScoreYouTubeAsync_NoSubtitlesFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // prevent resolvePushConfigOnce from loading real config.toml
	// Install a hermetic eng profile so this test runs in clean checkouts.
	installTestProfileDir(t, "eng")

	// Enable fallback and restore on cleanup.
	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = true
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	// Stub yt-dlp subtitle extraction → no subtitles.
	deps := installYtdlpNoSubtitlesStub(t)
	// Stub audio download → fake file.
	installYtdlpAudioStub(t)

	// Stub ffmpeg → write fake wav (EPIC-258 M2: injected via ytDeps).
	deps.FfmpegConvert = func(_ context.Context, _, outputPath string) error {
		return os.WriteFile(outputPath, []byte("RIFF-fake-wav"), 0o644)
	}

	// Stub whisper → return a transcript.
	installWhisperStubYT(t, deps, "This is the audio transcript for testing.", nil)

	// Stub evaluator → return a valid scorecard (RubricScores required for bare-verdict shortcut).
	deps.Backend = &funcScoringBackend{completeJSON: func(_ context.Context, _, _, _ string) ([]byte, error) {
		v := TriageVerdict{Score: 75, Verdict: "interesting", Tags: "test", RubricScores: map[string]int{"overall": 75}}
		return json.Marshal(v)
	}}

	q := newTestQueue(t)
	req := ShareRequest{
		Type:    "url",
		URL:     "https://www.youtube.com/watch?v=test123",
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
		scoreYouTubeAsync(req, q, "yt-dlp", nil, "", nil, deps)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("scoreYouTubeAsync timed out")
	}

	var status string
	if err := q.db.QueryRow("SELECT status FROM queue WHERE id=?", id).Scan(&status); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "scored" && status != "archived" {
		t.Errorf("status = %q, want scored or archived after audio fallback", status)
	}
}

// TestScoreYouTubeAsync_FallbackStepFailures verifies that individual failures
// in the audio fallback steps result in a failed queue row. EPIC-003 M3.
// ytAudioMaxRetries is set to 0 so the dead-letter path terminates immediately
// rather than putting the row in "pending" retry state.
func TestScoreYouTubeAsync_FallbackStepFailures(t *testing.T) {
	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = true
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	prevRetries := ytAudioMaxRetries
	ytAudioMaxRetries = 0
	t.Cleanup(func() { ytAudioMaxRetries = prevRetries })

	cases := []struct {
		name       string
		audioErr   error
		ffmpegErr  error
		whisperTx  string
		whisperErr error
	}{
		{
			name:     "audio_download_fails",
			audioErr: fmt.Errorf("yt-dlp: network error"),
		},
		{
			name:      "ffmpeg_fails",
			ffmpegErr: fmt.Errorf("ffmpeg: no such file"),
		},
		{
			name:       "whisper_fails",
			whisperErr: fmt.Errorf("whisper: model not found"),
		},
		{
			name:      "whisper_empty_transcript",
			whisperTx: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Stub execYtdlp → no subtitles.
			deps := installYtdlpNoSubtitlesStub(t)
			// Stub audio download.
			prevAudio := execYtdlpAudio
			if tc.audioErr != nil {
				execYtdlpAudio = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
					return "", ytVideoMeta{}, tc.audioErr
				}
			} else {
				execYtdlpAudio = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
					dir := t.TempDir()
					p := filepath.Join(dir, "audio.m4a")
					_ = os.WriteFile(p, []byte("FAKE-M4A"), 0o644)
					return p, ytVideoMeta{Title: "Test", Duration: 30}, nil
				}
			}
			t.Cleanup(func() { execYtdlpAudio = prevAudio })

			// Stub ffmpeg (EPIC-258 M2: injected via ytDeps).
			if tc.audioErr == nil {
				if tc.ffmpegErr != nil {
					deps.FfmpegConvert = func(_ context.Context, _, _ string) error { return tc.ffmpegErr }
				} else {
					deps.FfmpegConvert = func(_ context.Context, _, outputPath string) error {
						return os.WriteFile(outputPath, []byte("RIFF-fake"), 0o644)
					}
				}
			}

			// Stub whisper.
			if tc.audioErr == nil && tc.ffmpegErr == nil {
				installWhisperStubYT(t, deps, tc.whisperTx, tc.whisperErr)
			}

			q := newTestQueue(t)
			req := ShareRequest{
				Type:    "url",
				URL:     "https://www.youtube.com/watch?v=failtest",
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
				scoreYouTubeAsync(req, q, "yt-dlp", nil, "", nil, deps)
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("case %q: scoreYouTubeAsync timed out", tc.name)
			}

			var status string
			if err := q.db.QueryRow("SELECT status FROM queue WHERE id=?", id).Scan(&status); err != nil {
				t.Fatalf("case %q: query: %v", tc.name, err)
			}
			if status != "failed" {
				t.Errorf("case %q: status = %q, want failed", tc.name, status)
			}
		})
	}
}

// TestYtAudioFallback_TempDirCleanup verifies that ytAudioFallback removes the
// audio temp dir when the context is already cancelled before the download step.
// EPIC-003 M3.
func TestYtAudioFallback_TempDirCleanup(t *testing.T) {
	var capturedDir string

	prev := execYtdlpAudio
	execYtdlpAudio = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		// Create a real temp dir to verify cleanup.
		dir, err := os.MkdirTemp("", "linkari-ytaudio-test-*")
		if err != nil {
			return "", ytVideoMeta{}, err
		}
		capturedDir = dir
		p := filepath.Join(dir, "audio.m4a")
		_ = os.WriteFile(p, []byte("FAKE"), 0o644)
		return p, ytVideoMeta{}, nil
	}
	t.Cleanup(func() { execYtdlpAudio = prev })

	// Stub ffmpeg to fail so cleanup logic in ytAudioFallback triggers.
	// EPIC-258 M2: injected via ytDeps.
	ytFallbackDeps := &ytDeps{FfmpegConvert: func(_ context.Context, _, _ string) error {
		return fmt.Errorf("ffmpeg: injected failure")
	}}

	evtPath := filepath.Join(t.TempDir(), "events.jsonl")
	evtLogger, evtErr := NewEventLogger(evtPath)
	if evtErr != nil {
		t.Fatalf("NewEventLogger: %v", evtErr)
	}

	ctx := context.Background()
	_, _, err := ytAudioFallback(ctx, "yt-dlp", "https://www.youtube.com/watch?v=cleanup", 7, nil, evtLogger, "", ytFallbackDeps)
	if err == nil {
		t.Fatal("expected error from ffmpeg failure")
	}

	// After failure, ytAudioFallback defers os.RemoveAll(filepath.Dir(audioPath)).
	if capturedDir != "" {
		if _, statErr := os.Stat(capturedDir); !os.IsNotExist(statErr) {
			t.Errorf("temp dir %q not cleaned up after ytAudioFallback failure", capturedDir)
		}
	}

	// EPIC-005 M2: yt_audio_fallback_failed event must be emitted with step=ffmpeg.
	evtLogger.Close()
	rawEvents, readErr := os.ReadFile(evtPath)
	if readErr != nil {
		t.Fatalf("read events file: %v", readErr)
	}
	if !strings.Contains(string(rawEvents), `"yt_audio_fallback_failed"`) {
		t.Errorf("expected yt_audio_fallback_failed event, got: %s", rawEvents)
	}
	if !strings.Contains(string(rawEvents), `"ffmpeg"`) {
		t.Errorf("expected step=ffmpeg in yt_audio_fallback_failed event, got: %s", rawEvents)
	}
}

// TestYtAudioFallback_TimeoutExpiry verifies that when the outer context
// expires during the whisper step, ytAudioFallback returns a context deadline
// error and cleans up the audio temp dir. EPIC-004 M4.
func TestYtAudioFallback_TimeoutExpiry(t *testing.T) {
	var capturedDir string

	// Stub audio download — create a real temp dir so we can verify cleanup.
	prev := execYtdlpAudio
	execYtdlpAudio = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		dir, err := os.MkdirTemp("", "linkari-ytaudio-timeout-*")
		if err != nil {
			return "", ytVideoMeta{}, err
		}
		capturedDir = dir
		p := filepath.Join(dir, "audio.m4a")
		if err := os.WriteFile(p, []byte("FAKE-M4A"), 0o644); err != nil {
			return "", ytVideoMeta{}, err
		}
		return p, ytVideoMeta{Title: "Timeout Test", ID: "tout1", Duration: 30}, nil
	}
	t.Cleanup(func() { execYtdlpAudio = prev })

	// EPIC-258 M2: ffmpeg no-op + blocking whisper injected via ytDeps so the
	// whisper step is reached and blocks until the context is cancelled.
	ytFallbackDeps := &ytDeps{
		FfmpegConvert: func(_ context.Context, _, outputPath string) error {
			return os.WriteFile(outputPath, []byte("RIFF-fake"), 0o644)
		},
		Whisper: func(ctx context.Context, _, _ string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}

	// Short outer context to trigger timeout during whisper step.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	evtPath := filepath.Join(t.TempDir(), "events.jsonl")
	evtLogger, err := NewEventLogger(evtPath)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	defer evtLogger.Close()

	_, _, fallbackErr := ytAudioFallback(ctx, "yt-dlp", "https://www.youtube.com/watch?v=timeout1", 42, nil, evtLogger, "", ytFallbackDeps)
	if fallbackErr == nil {
		t.Fatal("expected error from context expiry, got nil")
	}
	// EPIC-005 M1: error must carry yt_audio_timeout prefix specifically.
	if !strings.HasPrefix(fallbackErr.Error(), "yt_audio_timeout") {
		t.Errorf("error = %q; want prefix 'yt_audio_timeout'", fallbackErr.Error())
	}

	// F2 TDD CT-2: timeout emits yt_audio_timeout; yt_audio_fallback_failed is NOT
	// emitted on timeout (the two events are distinct error classes per FDD §5).
	// EPIC-108 updated this contract; yt_audio_fallback_failed is reserved for
	// non-timeout whisper-cli exits.
	evtLogger.Close()
	rawEvents, readErr := os.ReadFile(evtPath)
	if readErr != nil {
		t.Fatalf("read events file: %v", readErr)
	}
	if !strings.Contains(string(rawEvents), `"yt_audio_timeout"`) {
		t.Errorf("expected yt_audio_timeout event in %s, got: %s", evtPath, rawEvents)
	}
	if strings.Contains(string(rawEvents), `"yt_audio_fallback_failed"`) {
		t.Errorf("yt_audio_fallback_failed must NOT be emitted on timeout (use yt_audio_timeout): %s", rawEvents)
	}

	// Temp dir must be cleaned up even on timeout.
	if capturedDir != "" {
		if _, statErr := os.Stat(capturedDir); !os.IsNotExist(statErr) {
			t.Errorf("temp dir %q not cleaned up after timeout", capturedDir)
		}
	}
}

// TestScoreYouTubeAsync_AudioFallbackSubtitleType verifies that when yt-dlp
// finds no subtitles and the audio fallback succeeds, the scored row's events
// contain subtitle_type="audio". EPIC-006 M3.
func TestScoreYouTubeAsync_AudioFallbackSubtitleType(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // prevent resolvePushConfigOnce from loading real config.toml
	installTestProfileDir(t, "eng")

	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = true
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	deps := installYtdlpNoSubtitlesStub(t)
	installYtdlpAudioStub(t)

	deps.FfmpegConvert = func(_ context.Context, _, outputPath string) error {
		return os.WriteFile(outputPath, []byte("RIFF-fake-wav"), 0o644)
	}

	installWhisperStubYT(t, deps, "Audio fallback transcript.", nil)

	deps.Backend = &funcScoringBackend{completeJSON: func(_ context.Context, _, _, _ string) ([]byte, error) {
		v := TriageVerdict{Score: 70, Verdict: "interesting", Tags: "test", RubricScores: map[string]int{"overall": 70}}
		return json.Marshal(v)
	}}

	evtPath := filepath.Join(t.TempDir(), "events.jsonl")
	evtLogger, err := NewEventLogger(evtPath)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	defer evtLogger.Close()

	q := newTestQueue(t)
	req := ShareRequest{
		Type:    "url",
		URL:     "https://www.youtube.com/watch?v=subtitletype",
		Profile: "eng",
	}
	id, qErr := q.Enqueue(&req)
	if qErr != nil {
		t.Fatalf("Enqueue: %v", qErr)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}
	req.QueueRowID = id

	done := make(chan struct{})
	go func() {
		defer close(done)
		scoreYouTubeAsync(req, q, "yt-dlp", evtLogger, "", nil, deps)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("scoreYouTubeAsync timed out")
	}

	evtLogger.Close()
	rawEvents, readErr := os.ReadFile(evtPath)
	if readErr != nil {
		t.Fatalf("read events file: %v", readErr)
	}
	if !strings.Contains(string(rawEvents), `"subtitle_type":"audio"`) &&
		!strings.Contains(string(rawEvents), `"subtitle_type": "audio"`) {
		t.Errorf("expected subtitle_type=audio in events, got: %s", rawEvents)
	}
}

// TestTranscribeYouTubeAsync_AudioFallbackSubtitleType verifies that when
// yt-dlp finds no subtitles and the audio fallback succeeds,
// transcribeYouTubeAsync emits subtitle_type="audio" in events. EPIC-006 M3.
func TestTranscribeYouTubeAsync_AudioFallbackSubtitleType(t *testing.T) {
	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = true
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	deps := installYtdlpNoSubtitlesStub(t)
	installYtdlpAudioStub(t)

	deps.FfmpegConvert = func(_ context.Context, _, outputPath string) error {
		return os.WriteFile(outputPath, []byte("RIFF-fake-wav"), 0o644)
	}

	installWhisperStubYT(t, deps, "Audio transcript via whisper.", nil)

	evtPath := filepath.Join(t.TempDir(), "events.jsonl")
	evtLogger, err := NewEventLogger(evtPath)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	defer evtLogger.Close()

	q := newTestQueue(t)
	req := ShareRequest{
		Type:    "url",
		URL:     "https://www.youtube.com/watch?v=txsubtitletype",
		Profile: "eng",
	}
	id, qErr := q.Enqueue(&req)
	if qErr != nil {
		t.Fatalf("Enqueue: %v", qErr)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}
	req.QueueRowID = id

	done := make(chan struct{})
	go func() {
		defer close(done)
		transcribeYouTubeAsync(req, q, "yt-dlp", evtLogger, "", nil, deps)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("transcribeYouTubeAsync timed out")
	}

	evtLogger.Close()
	rawEvents, readErr := os.ReadFile(evtPath)
	if readErr != nil {
		t.Fatalf("read events file: %v", readErr)
	}
	if !strings.Contains(string(rawEvents), `"subtitle_type":"audio"`) &&
		!strings.Contains(string(rawEvents), `"subtitle_type": "audio"`) {
		t.Errorf("expected subtitle_type=audio in events, got: %s", rawEvents)
	}
}

// TestRouteYouTubeURL_MissingType verifies that a YouTube URL with req.Type=""
// routes to scoreYouTubeAsync rather than scoreAsync. EPIC-003 M3.
func TestRouteYouTubeURL_MissingType(t *testing.T) {
	var scoreYTCalled bool

	// Capture calls via execYtdlp seam — scoreYouTubeAsync calls execYtdlp first.
	ytdlpStub := func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		scoreYTCalled = true
		return "", ytVideoMeta{}, fmt.Errorf("stub: no subtitles")
	}
	deps := &ytDeps{Ytdlp: ytdlpStub}

	// Also stub ytAudioFallback path (ytFallbackToAudio is false, so won't call it).
	q := newTestQueue(t)
	req := ShareRequest{
		Type:    "", // Missing type — the key invariant under test.
		URL:     "https://www.youtube.com/watch?v=missingtype",
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
		scoreYouTubeAsync(req, q, "yt-dlp", nil, "", nil, deps)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scoreYouTubeAsync timed out")
	}

	if !scoreYTCalled {
		t.Error("execYtdlp not called — req.Type=\"\" did not route to scoreYouTubeAsync")
	}
}

// ─── EPIC-006 M3: BT-1 / BT-2 — normalization wired into async pipelines ────

// TestScoreYouTubeAsync_BT1_NormalizationWired verifies that scoreYouTubeAsync
// passes the canonical URL (not the original redirect wrapper) to execYtdlp.
// EPIC-006 M3.
func TestScoreYouTubeAsync_BT1_NormalizationWired(t *testing.T) {
	canonical := "https://www.youtube.com/watch?v=bt1test"
	redirectURL := "https://redirect.example.com/url?url=" + canonical

	// Stub normalizer: maps redirectURL → canonical.
	prevNorm := execNormalizeURL
	execNormalizeURL = func(_ context.Context, rawURL string) (string, error) {
		if rawURL == redirectURL {
			return canonical, nil
		}
		return rawURL, nil
	}
	t.Cleanup(func() { execNormalizeURL = prevNorm })

	// Capture the URL that execYtdlp actually receives.
	var capturedURL string
	ytdlpStub := func(_ context.Context, _, videoURL string) (string, ytVideoMeta, error) {
		capturedURL = videoURL
		return "", ytVideoMeta{}, fmt.Errorf("stub: no subtitles")
	}
	deps := &ytDeps{Ytdlp: ytdlpStub}

	q := newTestQueue(t)
	req := ShareRequest{
		Type:    "url",
		URL:     redirectURL,
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
		scoreYouTubeAsync(req, q, "yt-dlp", nil, "", nil, deps)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scoreYouTubeAsync timed out")
	}

	if capturedURL != canonical {
		t.Errorf("BT-1: execYtdlp received %q, want canonical %q", capturedURL, canonical)
	}
}

// TestTranscribeYouTubeAsync_BT2_NormalizationWired verifies that
// transcribeYouTubeAsync passes the canonical URL to execYtdlp. EPIC-006 M3.
func TestTranscribeYouTubeAsync_BT2_NormalizationWired(t *testing.T) {
	canonical := "https://www.youtube.com/watch?v=bt2test"
	redirectURL := "https://redirect.example.com/url?url=" + canonical

	prevNorm := execNormalizeURL
	execNormalizeURL = func(_ context.Context, rawURL string) (string, error) {
		if rawURL == redirectURL {
			return canonical, nil
		}
		return rawURL, nil
	}
	t.Cleanup(func() { execNormalizeURL = prevNorm })

	var capturedURL string
	ytdlpStub := func(_ context.Context, _, videoURL string) (string, ytVideoMeta, error) {
		capturedURL = videoURL
		return "", ytVideoMeta{}, fmt.Errorf("stub: no subtitles")
	}
	deps := &ytDeps{Ytdlp: ytdlpStub}

	q := newTestQueue(t)
	req := ShareRequest{
		Type:    "url",
		URL:     redirectURL,
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
		transcribeYouTubeAsync(req, q, "yt-dlp", nil, "", nil, deps)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("transcribeYouTubeAsync timed out")
	}

	if capturedURL != canonical {
		t.Errorf("BT-2: execYtdlp received %q, want canonical %q", capturedURL, canonical)
	}
}

// RG-1 (POMO_whisper-audio-fallback-oom-kills): Only one whisper-cli process
// runs at a time across concurrent audio fallback jobs.
// When the semaphore is held, a second job emits yt_audio_queued_pending_semaphore
// and waits rather than spawning a second whisper-cli instance.
func TestAudioFallback_SemaphoreCap1(t *testing.T) {
	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = true
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	// Reset semaphore to a clean state and set max_retries=0 so failures are terminal.
	ytAudioSem = make(chan struct{}, 1)
	prevRetries := ytAudioMaxRetries
	ytAudioMaxRetries = 0
	t.Cleanup(func() { ytAudioMaxRetries = prevRetries })

	deps := installYtdlpNoSubtitlesStub(t)
	// hold gates the download stub: the first job holds the channel open until
	// the test releases it. This keeps the semaphore occupied while we launch a
	// second job and verify it blocks.
	hold := make(chan struct{})
	prevAudio := execYtdlpAudio
	var audioCallCount int32
	execYtdlpAudio = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		atomic.AddInt32(&audioCallCount, 1)
		// Block until released (only the first call enters; second waits on semaphore).
		<-hold
		return "", ytVideoMeta{}, fmt.Errorf("stub: released")
	}
	t.Cleanup(func() { execYtdlpAudio = prevAudio })

	evtPath := filepath.Join(t.TempDir(), "events.jsonl")
	evtLogger, err := NewEventLogger(evtPath)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	defer evtLogger.Close()

	q := newTestQueue(t)

	makeReq := func(id string) ShareRequest {
		req := ShareRequest{
			Type:    "url",
			URL:     "https://www.youtube.com/watch?v=" + id,
			Profile: "eng",
		}
		rowID, enqErr := q.Enqueue(&req)
		if enqErr != nil {
			t.Fatalf("Enqueue %s: %v", id, enqErr)
		}
		if err := q.MarkRelayed(rowID); err != nil {
			t.Fatalf("MarkRelayed %s: %v", id, err)
		}
		req.QueueRowID = rowID
		return req
	}

	req1 := makeReq("job1")
	req2 := makeReq("job2")

	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		scoreYouTubeAsync(req1, q, "yt-dlp", evtLogger, "", nil, deps)
	}()

	// Wait until job1 has entered the download stub and is holding the semaphore.
	deadline := time.After(3 * time.Second)
	for atomic.LoadInt32(&audioCallCount) == 0 {
		select {
		case <-deadline:
			t.Fatal("job1 never entered audio stub")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// Launch job2 while job1 holds the semaphore.
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		scoreYouTubeAsync(req2, q, "yt-dlp", evtLogger, "", nil, deps)
	}()

	// Give job2 time to attempt semaphore acquire and emit the queued event.
	time.Sleep(50 * time.Millisecond)

	// Verify job2 has NOT entered the download stub (semaphore is still held by job1).
	if atomic.LoadInt32(&audioCallCount) != 1 {
		t.Errorf("RG-1: audioCallCount = %d after 50ms, want 1 (semaphore must block job2)", atomic.LoadInt32(&audioCallCount))
	}

	// Release job1.
	close(hold)
	select {
	case <-done1:
	case <-time.After(5 * time.Second):
		t.Fatal("job1 timed out after release")
	}
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("job2 timed out after job1 released semaphore")
	}

	// Verify yt_audio_queued_pending_semaphore was emitted by job2.
	evtLogger.Close()
	rawEvents, readErr := os.ReadFile(evtPath)
	if readErr != nil {
		t.Fatalf("read events file: %v", readErr)
	}
	if !strings.Contains(string(rawEvents), `"yt_audio_queued_pending_semaphore"`) {
		t.Errorf("RG-1: expected yt_audio_queued_pending_semaphore event, got:\n%s", rawEvents)
	}
}
