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

// TestNormalizeYouTubeURL_RG1_Integration is the regression guard for
// google.com/url wrapper URLs (RG-1). It exercises the full pipeline:
//  1. normalizeYouTubeURL resolves the redirect to canonical form
//  2. ytAudioFallback produces a transcript (verifies yt-dlp + whisper path)
//
// This test catches the production failure from 2026-04-21 server.log row_id=482
// where Google redirect URLs passed verbatim to yt-dlp caused both subtitle
// extraction and audio fallback to fail. EPIC-006 M5.
//
// # Environment Setup
//
// Required env vars:
//
//	LINKARI_INTEGRATION_REDIRECT_URL — Google redirect URL wrapping a
//	  caption-free YouTube video.
//	  Example: LINKARI_INTEGRATION_REDIRECT_URL="https://www.google.com/url?sa=t&url=https://www.youtube.com/watch?v=jNQXAC9IVRw"
//
// Optional env vars:
//
//	YTDLP_PATH  — path to yt-dlp binary (default: "yt-dlp" on PATH)
//	WHISPER_CLI — path to whisper-cli binary (default: "whisper-cli" on PATH)
//
// Run with: go test -tags=integration ./cmd/linkari/... -run TestNormalizeYouTubeURL_RG1_Integration
func TestNormalizeYouTubeURL_RG1_Integration(t *testing.T) {
	redirectURL := os.Getenv("LINKARI_INTEGRATION_REDIRECT_URL")
	if redirectURL == "" {
		t.Skip("LINKARI_INTEGRATION_REDIRECT_URL not set — skipping integration test")
	}

	ytPath := os.Getenv("YTDLP_PATH")
	if ytPath == "" {
		ytPath = "yt-dlp"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Step 1: verify normalizeYouTubeURL resolves to canonical form.
	canonical, err := normalizeYouTubeURL(ctx, redirectURL)
	if err != nil {
		t.Fatalf("normalizeYouTubeURL error: %v", err)
	}
	if !isCanonicalYouTubeURL(canonical) {
		t.Fatalf("normalizeYouTubeURL returned non-canonical URL %q (expected youtube.com or youtu.be host)", canonical)
	}
	if canonical == redirectURL {
		t.Fatalf("normalizeYouTubeURL returned original redirect URL unchanged — redirect walk did not resolve")
	}
	t.Logf("RG-1: resolved %q → %q", redirectURL, canonical)

	// Step 2: verify full pipeline produces a transcript (not yt_no_subtitles).
	// Uses ytFallbackToAudio=true to exercise the audio fallback path for
	// caption-free videos.
	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = true
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	transcript, meta, fallbackErr := ytAudioFallback(ctx, ytPath, canonical, 0, nil, nil, "")
	if fallbackErr != nil {
		t.Fatalf("ytAudioFallback: %v (RG-1: canonical URL %q must produce a transcript, not yt_no_subtitles)", fallbackErr, canonical)
	}
	if strings.TrimSpace(transcript) == "" {
		t.Fatal("ytAudioFallback returned empty transcript")
	}
	t.Logf("RG-1 OK: title=%q duration=%ds canonical=%q transcript_len=%d",
		meta.Title, meta.Duration, canonical, len(transcript))
}

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
