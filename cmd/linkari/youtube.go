// EPIC-009 M3/M4: YouTube transcription via yt-dlp.
//
// runYtdlpExtract downloads subtitles for a YouTube URL using yt-dlp, strips
// SRT timing markers, and returns the plain-text transcript and video title.
// scoreYouTubeAsync is the async pipeline goroutine that wires extraction into
// the scoring, persistence, and FCM push flow. EPIC-009 M4.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ytVideoMeta is the subset of yt-dlp JSON output we care about.
type ytVideoMeta struct {
	Title string `json:"title"`
	ID    string `json:"id"`
}

// srtTimingRE matches SRT timing lines (e.g. "00:00:01,000 --> 00:00:04,000")
// and sequence number lines (digits-only lines). Both are stripped from transcripts.
var srtTimingRE = regexp.MustCompile(`(?m)^\d{2}:\d{2}:\d{2}[,\.]\d{3}\s*-->\s*\d{2}:\d{2}:\d{2}[,\.]\d{3}$`)
var srtSequenceRE = regexp.MustCompile(`(?m)^\d+$`)

// srtTagRE strips HTML-style tags sometimes present in auto-generated subtitles
// (e.g. <c>, <00:00:01.000>, font tags).
var srtTagRE = regexp.MustCompile(`<[^>]+>`)

// stripSRT removes SRT timing markers, sequence numbers, and HTML tags from
// subtitle content, returning clean paragraph text.
func stripSRT(raw string) string {
	raw = srtTimingRE.ReplaceAllString(raw, "")
	raw = srtSequenceRE.ReplaceAllString(raw, "")
	raw = srtTagRE.ReplaceAllString(raw, "")

	// Collapse blank lines and trim.
	lines := strings.Split(raw, "\n")
	var kept []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// execYtdlp is the test seam for yt-dlp invocation. Replace in tests to
// inject mock output without spawning a real subprocess.
var execYtdlp = runYtdlpExtract

// runYtdlpExtract invokes yt-dlp to extract subtitles for the given YouTube
// URL. It returns the plain-text transcript and video title, or an error if
// extraction fails. A 30-second context timeout is enforced.
//
// Flags used:
//   - --skip-download       : do not download the video file
//   - --write-subs          : download manually-uploaded subtitles
//   - --write-auto-subs     : download auto-generated subtitles as fallback
//   - --sub-langs "en.*,en" : prefer English subtitles
//   - --convert-subs srt    : normalize to SRT format
//   - --no-playlist         : process single video only (never a playlist)
//   - -j                    : dump JSON metadata to stdout (title extraction)
//   - -o <template>         : write subtitle files to a temp dir
func runYtdlpExtract(ctx context.Context, ytdlpPath, videoURL string) (transcript, title string, err error) {
	if ytdlpPath == "" {
		ytdlpPath = "yt-dlp"
	}

	// Verify the binary is reachable before spawning.
	if _, lookErr := exec.LookPath(ytdlpPath); lookErr != nil {
		return "", "", fmt.Errorf("yt-dlp not found at %q: %w", ytdlpPath, lookErr)
	}

	tmpDir, err := os.MkdirTemp("", "linkari-yt-*")
	if err != nil {
		return "", "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	outTemplate := filepath.Join(tmpDir, "%(id)s.%(ext)s")
	cmd := exec.CommandContext(ctx, ytdlpPath,
		"--skip-download",
		"--write-subs",
		"--write-auto-subs",
		"--sub-langs", "en.*,en",
		"--convert-subs", "srt",
		"--no-playlist",
		"-j",
		"-o", outTemplate,
		videoURL,
	)

	jsonOut, runErr := cmd.Output()
	// runErr may be set even when subtitles were downloaded (yt-dlp exits
	// non-zero for partial failures), so we check for subtitle files first.

	// Parse title from JSON stdout (best-effort; tolerate empty).
	if len(jsonOut) > 0 {
		var meta ytVideoMeta
		if jerr := json.Unmarshal(jsonOut, &meta); jerr == nil {
			title = meta.Title
		}
	}

	// Find the .srt file written to tmpDir.
	entries, dirErr := os.ReadDir(tmpDir)
	if dirErr != nil {
		if runErr != nil {
			return "", title, fmt.Errorf("yt-dlp: %w", runErr)
		}
		return "", title, fmt.Errorf("read temp dir: %w", dirErr)
	}

	var srtPath string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".srt") {
			srtPath = filepath.Join(tmpDir, e.Name())
			break
		}
	}

	if srtPath == "" {
		if runErr != nil {
			return "", title, fmt.Errorf("yt-dlp extraction failed (no subtitles, exit: %w)", runErr)
		}
		return "", title, fmt.Errorf("yt-dlp: no subtitles found for %s", videoURL)
	}

	raw, readErr := os.ReadFile(srtPath)
	if readErr != nil {
		return "", title, fmt.Errorf("read subtitle file: %w", readErr)
	}

	transcript = stripSRT(string(raw))
	if transcript == "" {
		return "", title, fmt.Errorf("yt-dlp: subtitle file was empty after stripping")
	}

	return transcript, title, nil
}

// scoreYouTubeAsync runs the full YouTube transcription + scoring pipeline in a
// goroutine. It mirrors processVoiceNoteAsync but uses yt-dlp for extraction
// instead of ffmpeg/whisper. EPIC-009 M4.
//
// Pipeline:
//  1. yt-dlp extract → transcript + title
//  2. SetText on queue row
//  3. saveTranscriptFile (source="youtube", YouTube filename pattern)
//  4. Evaluate via HaikuJSONEvaluator
//  5. UpdateScore → status=scored
//  6. EnqueueDigestIfDue → FCM push
func scoreYouTubeAsync(req ShareRequest, q *Queue, ytPath string, events *EventLogger) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	rowID := req.QueueRowID
	videoURL := req.URL
	profile := req.Profile

	slog.Info("score_youtube: start",
		"event_type", "score_youtube_start",
		"row_id", rowID,
		"url", videoURL,
		"profile", profile,
	)

	if q != nil {
		q.SetProgress(rowID, "extracting")
	}

	// Step 1: yt-dlp extraction.
	transcript, videoTitle, err := execYtdlp(ctx, ytPath, videoURL)
	if err != nil {
		errStr := err.Error()
		var verdict string
		switch {
		case strings.Contains(errStr, "not found"):
			verdict = "yt_dlp_unavailable"
		case strings.Contains(errStr, "no subtitles"):
			verdict = "yt_no_subtitles"
		default:
			verdict = "yt_extraction_failed"
		}
		slog.Warn("score_youtube: extraction failed",
			"event_type", "yt_transcript_failed",
			"row_id", rowID,
			"url", videoURL,
			"verdict", verdict,
			"error", err,
		)
		if events != nil {
			_ = events.Emit("yt_transcript_failed", map[string]interface{}{
				"row_id":  rowID,
				"url":     videoURL,
				"verdict": verdict,
				"error":   errStr,
			})
		}
		if q != nil {
			q.MarkFailedWithReason(rowID, verdict)
		}
		return
	}

	slog.Info("score_youtube: extracted",
		"event_type", "yt_transcript_extracted",
		"row_id", rowID,
		"url", videoURL,
		"title", videoTitle,
		"transcript_len", len(transcript),
	)
	if events != nil {
		_ = events.Emit("yt_transcript_extracted", map[string]interface{}{
			"row_id":         rowID,
			"url":            videoURL,
			"title":          videoTitle,
			"transcript_len": len(transcript),
		})
	}

	// Step 2: backfill queue text.
	if q != nil {
		if err := q.SetText(rowID, transcript); err != nil {
			slog.Warn("score_youtube: SetText failed", "row_id", rowID, "error", err)
		}
	}

	// Step 3: save transcript file.
	txPath, err := saveTranscriptFile(rowID, profile, "", transcript, "youtube", videoURL, videoTitle)
	if err != nil {
		slog.Warn("score_youtube: save transcript failed", "row_id", rowID, "error", err)
		// Non-fatal — continue to scoring.
	} else {
		slog.Info("score_youtube: transcript saved",
			"event_type", "score_youtube_transcript_saved",
			"row_id", rowID,
			"path", txPath,
		)
	}

	// Step 4: rubric scoring.
	if q != nil {
		q.SetProgress(rowID, "scoring")
	}
	eval := HaikuJSONEvaluator{}
	_, sysPrompt, tmplErr := loadProfileTemplateForModeJSON(profile, "url")
	var ytScore int
	var ytVerdict, ytTags string
	if tmplErr == nil {
		rubricCtx, rubricCancel := context.WithTimeout(ctx, 60*time.Second)
		sc, evalErr := eval.Evaluate(rubricCtx, transcript, sysPrompt)
		rubricCancel()
		if evalErr != nil {
			slog.Warn("score_youtube: eval failed", "row_id", rowID, "error", evalErr)
			ytVerdict = "eval_failed"
		} else {
			ytScore = sc.Score
			ytVerdict = sc.Verdict
			ytTags = sc.Tags
		}
	} else {
		slog.Warn("score_youtube: template load failed", "row_id", rowID, "profile", profile, "error", tmplErr)
		ytVerdict = "template_missing"
	}

	// Step 5: persist score.
	if q == nil {
		return
	}
	slug := fmt.Sprintf("yt-%d", rowID)
	if err := q.UpdateScore(rowID, ytScore, ytTags, ytVerdict, slug, "", ""); err != nil {
		slog.Warn("score_youtube: UpdateScore failed", "row_id", rowID, "error", err)
		return
	}

	// Step 6: FCM push.
	resolvePushConfigOnce(q)
	_, _ = q.EnqueueDigestIfDue(context.Background(), profile, ytScore, slug, ytVerdict, videoURL)

	slog.Info("score_youtube: complete",
		"event_type", "score_youtube_complete",
		"row_id", rowID,
		"score", ytScore,
		"verdict", ytVerdict,
		"profile", profile,
	)
}
