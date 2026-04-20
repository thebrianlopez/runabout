// EPIC-009 M3: unit tests for YouTube extraction helpers.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
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

// TestRunYtdlpExtract_MockExec verifies the extraction path using a mock seam.
// EPIC-090 M4: seam now returns ytVideoMeta instead of a bare title string.
func TestRunYtdlpExtract_MockExec(t *testing.T) {
	// Save and restore the real execYtdlp seam.
	orig := execYtdlp
	defer func() { execYtdlp = orig }()

	wantTranscript := "Hello world"
	wantMeta := ytVideoMeta{
		Title:        "Test Video",
		ID:           "dQw4w9WgXcQ",
		Duration:     212,
		SubtitleType: "manual",
	}

	execYtdlp = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return wantTranscript, wantMeta, nil
	}

	got, meta, err := execYtdlp(context.Background(), "yt-dlp", "https://www.youtube.com/watch?v=test")
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
	orig := execYtdlp
	defer func() { execYtdlp = orig }()

	execYtdlp = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return "", ytVideoMeta{}, fmt.Errorf("yt-dlp: no subtitles found")
	}

	_, _, err := execYtdlp(context.Background(), "yt-dlp", "https://www.youtube.com/watch?v=nosubs")
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
