//go:build integration

// EPIC-003 M4: integration test for yt-dlp + whisper audio fallback.
//
// # Environment Setup
//
// Required env vars:
//
//	LINKARI_INTEGRATION_AUDIO_URL — URL of a known caption-free YouTube video
//	  Example: LINKARI_INTEGRATION_AUDIO_URL=https://www.youtube.com/watch?v=jNQXAC9IVRw
//	  (Use a short, caption-free video to keep test runtime under 2 minutes.)
//
// Optional env vars:
//
//	YTDLP_PATH    — path to yt-dlp binary (default: "yt-dlp" on PATH)
//	WHISPER_CLI   — path to whisper-cli binary (default: "whisper-cli" on PATH)
//
// Run with: go test -tags=integration ./cmd/linkari/... -run TestYouTubeAudioFallback_Integration
package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestYouTubeAudioFallback_Integration downloads audio from a caption-free
// YouTube video, converts it with ffmpeg, and transcribes it with whisper.
// The test verifies that ytAudioFallback returns a non-empty transcript.
// EPIC-003 M4.
func TestYouTubeAudioFallback_Integration(t *testing.T) {
	videoURL := os.Getenv("LINKARI_INTEGRATION_AUDIO_URL")
	if videoURL == "" {
		t.Skip("LINKARI_INTEGRATION_AUDIO_URL not set — skipping integration test")
	}

	ytPath := os.Getenv("YTDLP_PATH")
	if ytPath == "" {
		ytPath = "yt-dlp"
	}

	// Allow up to 10 minutes for audio download + ffmpeg + whisper.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	transcript, meta, err := ytAudioFallback(ctx, ytPath, videoURL, 0, nil, nil, "")
	if err != nil {
		t.Fatalf("ytAudioFallback: %v", err)
	}
	if strings.TrimSpace(transcript) == "" {
		t.Fatal("ytAudioFallback returned empty transcript")
	}
	t.Logf("audio fallback OK: title=%q duration=%ds transcript_len=%d",
		meta.Title, meta.Duration, len(transcript))
}
