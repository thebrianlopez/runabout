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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ytSubtitleLangs is the yt-dlp --sub-langs value. Defaults to "en.*,en".
// Overridden by ServerConfig.YouTube.SubtitleLangs via initClaudeConfig().
var ytSubtitleLangs = "en.*,en"

// ytTimeoutSeconds is the yt-dlp extraction timeout in seconds. Defaults to 30.
// Overridden by ServerConfig.YouTube.TimeoutSeconds via initClaudeConfig().
var ytTimeoutSeconds = 30

// ytFallbackToAudio enables the audio-download fallback when yt-dlp finds no
// subtitles. When true, scoreYouTubeAsync and transcribeYouTubeAsync attempt
// yt-dlp audio download → ffmpeg → whisper before marking the row failed.
// Default true (EPIC-003 M5). Overridable via server.yaml youtube.fallback_to_audio: false.
var ytFallbackToAudio = true

// ytAudioSem serializes whisper-cli invocations to prevent concurrent model loads
// from exhausting available memory (EPIC-108 M1). Cap=1 by default; resized by
// initClaudeConfig when [server.whisper].max_concurrency is set.
var ytAudioSem = make(chan struct{}, 1)

// ytWhisperTimeoutSecs overrides the computed whisper deadline when > 0.
// Set by initClaudeConfig from [server.whisper].timeout_secs. EPIC-108 M2.
var ytWhisperTimeoutSecs int

// ytAudioMaxRetries is the dead-letter retry limit for audio fallback jobs.
// Default 3; configurable via [server.whisper].max_retries. EPIC-108 M3.
var ytAudioMaxRetries = 3

// whisperDeadlineSecs returns the wall-clock budget for a single whisper-cli run.
// If configSecs > 0 it wins; otherwise uses max(audioDurationSecs×2, 900).
func whisperDeadlineSecs(audioDurationSecs, configSecs int) int {
	if configSecs > 0 {
		return configSecs
	}
	d := audioDurationSecs * 2
	if d < 900 {
		d = 900
	}
	return d
}

// execYtdlpAudio is the test seam for yt-dlp audio download invocation.
// Replace in tests to inject mock output without spawning a real subprocess.
// EPIC-001 M3.
var execYtdlpAudio = runYtdlpAudioDownload

// ytVideoMeta is the subset of yt-dlp JSON output we care about.
// EPIC-090 M4: added Duration and SubtitleType.
// EPIC-012 M4: added IsShorts and ChannelID.
type ytVideoMeta struct {
	Title        string `json:"title"`
	ID           string `json:"id"`
	Duration     int    `json:"duration"` // seconds
	ChannelID    string `json:"channel_id"`
	SubtitleType string // "manual" | "auto" — detected from yt-dlp JSON
	IsShorts     bool   // set by detectShorts after meta is populated
}

// ytRawMeta is used to parse the subtitle availability fields from yt-dlp JSON
// so we can determine whether the extracted subtitles are manually uploaded
// ("manual") or auto-generated ("auto"). EPIC-090 M4.
type ytRawMeta struct {
	Title             string                     `json:"title"`
	ID                string                     `json:"id"`
	Duration          int                        `json:"duration"`
	ChannelID         string                     `json:"channel_id"`
	Subtitles         map[string]json.RawMessage `json:"subtitles"`
	AutomaticCaptions map[string]json.RawMessage `json:"automatic_captions"`
}

// detectShorts reports whether the given YouTube video is a Short.
// A video is a Short if its URL contains /shorts/ or its duration is 1–60s.
// Duration=0 (unknown) is never classified as a Short to avoid false positives.
func detectShorts(videoURL string, durationSec int) bool {
	if strings.Contains(videoURL, "/shorts/") {
		return true
	}
	return durationSec > 0 && durationSec <= 60
}

// selectShortsRubric returns shortsTemplate when non-empty, otherwise fallback.
func selectShortsRubric(shortsTemplate, fallback string) string {
	if shortsTemplate != "" {
		return shortsTemplate
	}
	return fallback
}

// detectSubtitleType returns "manual" when the yt-dlp JSON reports at least
// one manually uploaded English subtitle track, otherwise returns "auto".
func detectSubtitleType(raw ytRawMeta) string {
	for lang := range raw.Subtitles {
		if strings.HasPrefix(lang, "en") {
			return "manual"
		}
	}
	return "auto"
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
// EPIC-090 M4: returns ytVideoMeta (title + id + duration + subtitle_type) instead of title string.
var execYtdlp = runYtdlpExtract

// runYtdlpExtract invokes yt-dlp to extract subtitles for the given YouTube
// URL. It returns the plain-text transcript and video metadata, or an error
// if extraction fails. Timeout is configurable via ytTimeoutSeconds (default 30s).
//
// Flags used:
//   - --skip-download       : do not download the video file
//   - --write-subs          : download manually-uploaded subtitles
//   - --write-auto-subs     : download auto-generated subtitles as fallback
//   - --sub-langs           : from ytSubtitleLangs (default "en.*,en")
//   - --convert-subs srt    : normalize to SRT format
//   - --no-playlist         : process single video only (never a playlist)
//   - -j                    : dump JSON metadata to stdout
//   - -o <template>         : write subtitle files to a temp dir
func runYtdlpExtract(ctx context.Context, ytdlpPath, videoURL string) (transcript string, meta ytVideoMeta, err error) {
	if ytdlpPath == "" {
		ytdlpPath = "yt-dlp"
	}

	// Verify the binary is reachable before spawning.
	if _, lookErr := exec.LookPath(ytdlpPath); lookErr != nil {
		return "", ytVideoMeta{}, fmt.Errorf("yt-dlp not found at %q: %w", ytdlpPath, lookErr)
	}

	tmpDir, tmpErr := os.MkdirTemp("", "linkari-yt-*")
	if tmpErr != nil {
		return "", ytVideoMeta{}, fmt.Errorf("create temp dir: %w", tmpErr)
	}
	defer os.RemoveAll(tmpDir)

	timeout := time.Duration(ytTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	langs := ytSubtitleLangs
	if langs == "" {
		langs = "en.*,en"
	}

	outTemplate := filepath.Join(tmpDir, "%(id)s.%(ext)s")
	cmd := exec.CommandContext(ctx, ytdlpPath,
		"--skip-download",
		"--write-subs",
		"--write-auto-subs",
		"--sub-langs", langs,
		"--convert-subs", "srt",
		"--no-playlist",
		"-j",
		"-o", outTemplate,
		videoURL,
	)

	jsonOut, runErr := cmd.Output()
	// runErr may be set even when subtitles were downloaded (yt-dlp exits
	// non-zero for partial failures), so we check for subtitle files first.

	// Parse metadata from JSON stdout (best-effort; tolerate empty).
	// EPIC-090 M4: parse ytRawMeta to detect Duration and SubtitleType.
	// EPIC-012 M6: extract ChannelID and detect Shorts after meta population.
	if len(jsonOut) > 0 {
		var raw ytRawMeta
		if jerr := json.Unmarshal(jsonOut, &raw); jerr == nil {
			meta.Title = raw.Title
			meta.ID = raw.ID
			meta.Duration = raw.Duration
			meta.ChannelID = raw.ChannelID
			meta.SubtitleType = detectSubtitleType(raw)
		}
	}
	if meta.SubtitleType == "" {
		meta.SubtitleType = "auto" // safe default
	}
	meta.IsShorts = detectShorts(videoURL, meta.Duration)

	// Find the .srt file written to tmpDir.
	entries, dirErr := os.ReadDir(tmpDir)
	if dirErr != nil {
		if runErr != nil {
			return "", meta, fmt.Errorf("yt-dlp: %w", runErr)
		}
		return "", meta, fmt.Errorf("read temp dir: %w", dirErr)
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
			return "", meta, fmt.Errorf("yt-dlp extraction failed (no subtitles, exit: %w)", runErr)
		}
		return "", meta, fmt.Errorf("yt-dlp: no subtitles found for %s", videoURL)
	}

	rawSRT, readErr := os.ReadFile(srtPath)
	if readErr != nil {
		return "", meta, fmt.Errorf("read subtitle file: %w", readErr)
	}

	transcript = stripSRT(string(rawSRT))
	if transcript == "" {
		return "", meta, fmt.Errorf("yt-dlp: subtitle file was empty after stripping")
	}

	return transcript, meta, nil
}

// runYtdlpAudioDownload downloads the best-available audio track for a YouTube
// URL using yt-dlp and returns a path to the downloaded file along with basic
// video metadata. The caller is responsible for removing the returned file.
// Timeout is 3 minutes (audio downloads are slower than subtitle extraction).
//
// Flags used:
//   - --format bestaudio[ext=m4a]/bestaudio : prefer m4a; fall back to any audio
//   - --no-playlist                          : single video only
//   - -o <template>                          : output to temp dir
//   - -j                                     : dump JSON metadata to stdout
//
// EPIC-001 M3.
func runYtdlpAudioDownload(ctx context.Context, ytdlpPath, videoURL string) (audioPath string, meta ytVideoMeta, err error) {
	if ytdlpPath == "" {
		ytdlpPath = "yt-dlp"
	}
	if _, lookErr := exec.LookPath(ytdlpPath); lookErr != nil {
		return "", ytVideoMeta{}, fmt.Errorf("yt-dlp not found at %q: %w", ytdlpPath, lookErr)
	}

	tmpDir, tmpErr := os.MkdirTemp("", "linkari-ytaudio-*")
	if tmpErr != nil {
		return "", ytVideoMeta{}, fmt.Errorf("create temp dir: %w", tmpErr)
	}
	// NOTE: caller is responsible for removing the audio file, not the entire
	// dir. We remove tmpDir here only if we are returning an error (no file to
	// preserve). On success, tmpDir is left on disk; caller cleans it up via
	// defer os.RemoveAll(filepath.Dir(audioPath)).
	var success bool
	defer func() {
		if !success {
			os.RemoveAll(tmpDir)
		}
	}()

	// 3-minute timeout: audio downloads are slower than subtitle extraction.
	dlCtx, dlCancel := context.WithTimeout(ctx, 3*time.Minute)
	defer dlCancel()

	outTemplate := filepath.Join(tmpDir, "%(id)s.%(ext)s")
	cmd := exec.CommandContext(dlCtx, ytdlpPath,
		"--format", "bestaudio[ext=m4a]/bestaudio",
		"--no-playlist",
		"--print-json",
		"-o", outTemplate,
		videoURL,
	)

	jsonOut, runErr := cmd.Output()

	// Parse metadata from JSON stdout (best-effort).
	if len(jsonOut) > 0 {
		var raw ytRawMeta
		if jerr := json.Unmarshal(jsonOut, &raw); jerr == nil {
			meta.Title = raw.Title
			meta.ID = raw.ID
			meta.Duration = raw.Duration
		}
	}

	// Find the downloaded audio file.
	entries, dirErr := os.ReadDir(tmpDir)
	if dirErr != nil || len(entries) == 0 {
		if runErr != nil {
			return "", meta, fmt.Errorf("yt-dlp audio download failed: %w", runErr)
		}
		return "", meta, fmt.Errorf("yt-dlp audio download: no file written to %s", tmpDir)
	}

	// Take the first (and only) file yt-dlp wrote.
	for _, e := range entries {
		if !e.IsDir() {
			audioPath = filepath.Join(tmpDir, e.Name())
			break
		}
	}
	if audioPath == "" {
		return "", meta, fmt.Errorf("yt-dlp audio download: no audio file in %s", tmpDir)
	}

	success = true
	return audioPath, meta, nil
}

// ytAudioFallback downloads the audio track for a YouTube video URL and
// transcribes it via ffmpeg + whisper. Used when yt-dlp subtitle extraction
// finds no subtitles (yt_no_subtitles). Returns the transcript string and
// video metadata on success, or an error if any step fails.
// EPIC-001 M3. whisperModel is passed through to execWhisper (empty = default).
// EPIC-005 M1: context.DeadlineExceeded from whisper is wrapped as yt_audio_timeout.
// EPIC-005 M2: emits yt_audio_fallback_complete / yt_audio_fallback_failed with step/duration fields.
func ytAudioFallback(ctx context.Context, ytPath, videoURL string, rowID int64, q *Queue, events *EventLogger, whisperModel string) (string, ytVideoMeta, error) {
	start := time.Now()

	slog.Info("yt_audio_fallback: start",
		"event_type", "yt_audio_fallback_start",
		"row_id", rowID,
		"url", videoURL,
	)

	if q != nil {
		q.SetProgress(rowID, "downloading_audio")
	}

	// Step 1: download audio via yt-dlp.
	audioPath, meta, dlErr := execYtdlpAudio(ctx, ytPath, videoURL)
	if dlErr != nil {
		slog.Warn("yt_audio_fallback_failed",
			"event_type", "yt_audio_fallback_failed",
			"row_id", rowID,
			"step", "download",
			"error_reason", dlErr.Error(),
		)
		if events != nil {
			_ = events.Emit("yt_audio_fallback_failed", map[string]interface{}{
				"row_id":       rowID,
				"step":         "download",
				"error_reason": dlErr.Error(),
			})
		}
		return "", ytVideoMeta{}, fmt.Errorf("audio download: %w", dlErr)
	}
	// Remove the downloaded audio dir on exit (both success and failure).
	defer os.RemoveAll(filepath.Dir(audioPath))

	// Step 2: ffmpeg convert to wav (16kHz mono for whisper).
	if q != nil {
		q.SetProgress(rowID, "converting_audio")
	}
	wavPath := audioPath + ".wav"
	defer os.Remove(wavPath)

	ffmpegCtx, ffmpegCancel := context.WithTimeout(ctx, 60*time.Second)
	ffErr := execFfmpegConvert(ffmpegCtx, audioPath, wavPath)
	ffmpegCancel()
	if ffErr != nil {
		slog.Warn("yt_audio_fallback_failed",
			"event_type", "yt_audio_fallback_failed",
			"row_id", rowID,
			"step", "ffmpeg",
			"error_reason", ffErr.Error(),
		)
		if events != nil {
			_ = events.Emit("yt_audio_fallback_failed", map[string]interface{}{
				"row_id":       rowID,
				"step":         "ffmpeg",
				"error_reason": ffErr.Error(),
			})
		}
		return "", meta, fmt.Errorf("ffmpeg: %w", ffErr)
	}

	// Step 3: whisper transcribe.
	if q != nil {
		q.SetProgress(rowID, "transcribing_audio")
	}
	// EPIC-108 M2: dynamic deadline — max(duration×2, 900s); configurable via [server.whisper].timeout_secs.
	deadlineSecs := whisperDeadlineSecs(meta.Duration, ytWhisperTimeoutSecs)
	whisperCtx, whisperCancel := context.WithTimeout(ctx, time.Duration(deadlineSecs)*time.Second)
	transcript, whisperErr := execWhisper(whisperCtx, wavPath, whisperModel)
	whisperCancel()
	if whisperErr != nil {
		if errors.Is(whisperErr, context.DeadlineExceeded) {
			slog.Warn("yt_audio_timeout",
				"event_type", "yt_audio_timeout",
				"row_id", rowID,
				"deadline_secs", deadlineSecs,
			)
			if events != nil {
				_ = events.Emit("yt_audio_timeout", map[string]interface{}{
					"row_id":       rowID,
					"deadline_secs": deadlineSecs,
				})
				_ = events.Emit("yt_audio_fallback_failed", map[string]interface{}{
					"row_id":       rowID,
					"step":         "whisper",
					"error_reason": "yt_audio_timeout",
				})
			}
			return "", meta, fmt.Errorf("yt_audio_timeout: %w", whisperErr)
		}
		slog.Warn("yt_audio_fallback_failed",
			"event_type", "yt_audio_fallback_failed",
			"row_id", rowID,
			"step", "whisper",
			"error_reason", whisperErr.Error(),
		)
		if events != nil {
			_ = events.Emit("yt_audio_fallback_failed", map[string]interface{}{
				"row_id":       rowID,
				"step":         "whisper",
				"error_reason": whisperErr.Error(),
			})
		}
		return "", meta, fmt.Errorf("whisper: %w", whisperErr)
	}
	if strings.TrimSpace(transcript) == "" {
		slog.Warn("yt_audio_fallback_failed",
			"event_type", "yt_audio_fallback_failed",
			"row_id", rowID,
			"step", "whisper",
			"error_reason", "empty_transcript",
		)
		if events != nil {
			_ = events.Emit("yt_audio_fallback_failed", map[string]interface{}{
				"row_id":       rowID,
				"step":         "whisper",
				"error_reason": "empty_transcript",
			})
		}
		return "", meta, fmt.Errorf("whisper: empty transcript")
	}

	elapsed := time.Since(start).Milliseconds()
	slog.Info("yt_audio_fallback_complete",
		"event_type", "yt_audio_fallback_complete",
		"row_id", rowID,
		"duration_ms", elapsed,
		"subtitle_type", "audio",
	)
	if events != nil {
		_ = events.Emit("yt_audio_fallback_complete", map[string]interface{}{
			"row_id":        rowID,
			"duration_ms":   elapsed,
			"subtitle_type": "audio",
		})
	}

	return transcript, meta, nil
}

// scoreYouTubeAsync runs the full YouTube transcription + scoring pipeline in a
// goroutine. It mirrors processVoiceNoteAsync but uses yt-dlp for extraction
// instead of ffmpeg/whisper. EPIC-009 M4. EPIC-090 M3/M4/M5.
//
// Pipeline:
//  1. yt-dlp extract → transcript + ytVideoMeta
//  2. SetText on queue row
//  3. saveTranscriptFile (source="youtube", extended frontmatter)
//  4. Evaluate via HaikuJSONEvaluator
//  5. UpdateScore → status=scored
//  6. EnqueueDigestIfDue → FCM push (content_type="youtube" for M5 title template)
func scoreYouTubeAsync(req ShareRequest, q *Queue, ytPath string, events *EventLogger, whisperModel string) {
	outerTimeout := 120 * time.Second
	if ytFallbackToAudio {
		outerTimeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), outerTimeout)
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

	// Step 1: normalize URL (resolve redirect wrappers to canonical YouTube form).
	// EPIC-006 M3: inserted before execYtdlp so yt-dlp always receives a canonical URL.
	if normalized, normErr := execNormalizeURL(ctx, videoURL); normErr == nil {
		videoURL = normalized
	}

	// Step 2: yt-dlp extraction.
	transcript, meta, err := execYtdlp(ctx, ytPath, videoURL)
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
		// EPIC-001 M3: audio fallback — when subtitles are unavailable and the
		// config gate is enabled, attempt yt-dlp audio download → ffmpeg → whisper
		// before giving up. Only activated for yt_no_subtitles (not binary missing
		// or extraction timeouts which imply a network or binary problem).
		if verdict == "yt_no_subtitles" && ytFallbackToAudio {
			// EPIC-108 M1: acquire semaphore before spawning whisper-cli.
			select {
			case ytAudioSem <- struct{}{}:
				// acquired immediately
			default:
				if events != nil {
					_ = events.Emit("yt_audio_queued_pending_semaphore", map[string]interface{}{"row_id": rowID})
				}
				slog.Info("yt_audio_fallback: waiting for semaphore",
					"event_type", "yt_audio_queued_pending_semaphore",
					"row_id", rowID,
				)
				select {
				case ytAudioSem <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
			transcript, meta, err = ytAudioFallback(ctx, ytPath, videoURL, rowID, q, events, whisperModel)
			<-ytAudioSem
			if err == nil {
				meta.SubtitleType = "audio"
				// Fallback succeeded — continue with transcript below.
				goto subtitleReady
			}
			// EPIC-005 M1: surface yt_audio_timeout specifically so the queue row
			// verdict reflects the actual failure mode rather than yt_no_subtitles.
			if strings.HasPrefix(err.Error(), "yt_audio_timeout") {
				verdict = "yt_audio_timeout"
			}
			slog.Warn("score_youtube: audio fallback also failed",
				"event_type", "score_youtube_fallback_failed",
				"row_id", rowID,
				"url", videoURL,
				"verdict", verdict,
				"error", err,
			)
			// EPIC-108 M3: dead-letter requeue with exponential backoff.
			if q != nil {
				if row, gErr := q.GetByID(rowID); gErr == nil && row != nil && row.RetryCount < ytAudioMaxRetries {
					nextAttempt := row.RetryCount + 1
					if rErr := q.EnqueueAudioRetry(rowID, nextAttempt); rErr == nil {
						slog.Info("yt_audio_retry_enqueued",
							"event_type", "yt_audio_retry_enqueued",
							"row_id", rowID,
							"attempt", nextAttempt,
						)
						if events != nil {
							_ = events.Emit("yt_audio_retry_enqueued", map[string]interface{}{
								"row_id":  rowID,
								"attempt": nextAttempt,
							})
						}
						return
					}
				}
				q.MarkFailedWithReason(rowID, "yt_audio_terminal_failed")
				slog.Warn("yt_audio_terminal_failed",
					"event_type", "yt_audio_terminal_failed",
					"row_id", rowID,
				)
				if events != nil {
					_ = events.Emit("yt_audio_terminal_failed", map[string]interface{}{"row_id": rowID})
				}
				enqueuePrefilterPush(q, &req, "yt_audio_terminal_failed")
			}
			return
		}
		if q != nil {
			q.MarkFailedWithReason(rowID, verdict)
			// Defer push to here so fallback path doesn't trigger a premature notification.
			enqueuePrefilterPush(q, &req, verdict)
		}
		return
	}
subtitleReady:

	slog.Info("score_youtube: extracted",
		"event_type", "yt_transcript_extracted",
		"row_id", rowID,
		"url", videoURL,
		"title", meta.Title,
		"video_id", meta.ID,
		"duration", meta.Duration,
		"subtitle_type", meta.SubtitleType,
		"transcript_len", len(transcript),
	)
	if events != nil {
		_ = events.Emit("yt_transcript_extracted", map[string]interface{}{
			"row_id":        rowID,
			"url":           videoURL,
			"title":         meta.Title,
			"video_id":      meta.ID,
			"duration":      meta.Duration,
			"subtitle_type": meta.SubtitleType,
			"transcript_len": len(transcript),
		})
	}

	// Step 3a: persist Shorts detection. EPIC-012 M8/M11.
	if q != nil && rowID > 0 {
		if err := q.SetIsShorts(rowID, meta.IsShorts); err != nil {
			slog.Warn("score_youtube: SetIsShorts failed", "row_id", rowID, "error", err)
		}
	}
	if meta.IsShorts {
		detectionMethod := "duration"
		if strings.Contains(videoURL, "/shorts/") {
			detectionMethod = "url_pattern"
		}
		slog.Info("score_youtube: shorts detected",
			"event_type", "shorts_detected",
			"row_id", rowID,
			"url", videoURL,
			"detection_method", detectionMethod,
		)
	}

	// Step 3b: backfill queue text.
	if q != nil {
		if err := q.SetText(rowID, transcript); err != nil {
			slog.Warn("score_youtube: SetText failed", "row_id", rowID, "error", err)
		}
	}

	// Step 4: save transcript file. EPIC-090 M4: pass video_id, duration, subtitle_type.
	txPath, err := saveTranscriptFile(rowID, profile, "", transcript, "youtube", videoURL, meta.Title, meta.ID, meta.Duration, meta.SubtitleType)
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

	// Step 5: rubric scoring.
	if q != nil {
		q.SetProgress(rowID, "scoring")
	}
	eval := HaikuJSONEvaluator{}
	_, sysPrompt, tmplErr := loadProfileTemplateForModeJSON(profile, "url")
	var ytScore int
	var ytVerdict, ytTags string
	var rubricSource string
	if tmplErr == nil {
		// EPIC-012 M8: Shorts rubric substitution.
		if meta.IsShorts {
			if req.ShortsRubricTemplate != "" {
				sysPrompt = req.ShortsRubricTemplate
				rubricSource = "shorts_template"
			} else {
				rubricSource = "default"
				slog.Info("score_youtube: shorts rubric missing",
					"event_type", "shorts_rubric_missing",
					"row_id", rowID,
					"profile", profile,
				)
			}
		} else {
			rubricSource = "default"
		}
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

	// Step 6: persist score.
	if q == nil {
		return
	}
	if ytVerdict == "eval_failed" || ytVerdict == "template_missing" {
		_ = q.UpdateFailedVerdict(rowID, ytVerdict)
		_ = q.MarkFailedWithReason(rowID, ytVerdict)
		slog.Warn("score_youtube: eval failed, row marked failed",
			"event_type", "score_youtube_eval_failed",
			"row_id", rowID,
			"verdict", ytVerdict,
		)
		return
	}
	slug := fmt.Sprintf("yt-%d", rowID)
	if _, err := q.ScoreByID(rowID, ytScore, ytTags, ytVerdict, slug, "", ""); err != nil {
		slog.Warn("score_youtube: ScoreByID failed", "row_id", rowID, "error", err)
		return
	}

	// Step 7: FCM push. EPIC-090 M5: tag content_type="youtube" so sendOutboxFCM
	// can render a YouTube-specific notification title.
	// EPIC-012 M8: use "youtube_shorts" for Shorts so FCM title reads "Short: ...".
	resolvePushConfigOnce(q)
	result, pushErr := q.EnqueueDigestIfDue(context.Background(), profile, ytScore, slug, ytVerdict, videoURL)
	if pushErr != nil {
		slog.Warn("score_youtube: EnqueueDigestIfDue failed", "row_id", rowID, "error", pushErr)
	} else if !result.Enqueued {
		slog.Info("score_youtube: push skipped", "event_type", "score_youtube_push_skipped",
			"row_id", rowID, "reason", result.Reason,
			"seconds_until_allowed", result.SecondsUntilAllowed)
	} else if result.ID > 0 {
		contentType := "youtube"
		if meta.IsShorts {
			contentType = "youtube_shorts"
		}
		_ = q.SetPushContentType(result.ID, contentType)
	}

	// EPIC-012 M11: Shorts scoring complete log.
	if meta.IsShorts {
		slog.Info("score_youtube: shorts scoring complete",
			"event_type", "shorts_scoring_complete",
			"row_id", rowID,
			"rubric_source", rubricSource,
			"score", ytScore,
		)
	}

	slog.Info("score_youtube: complete",
		"event_type", "score_youtube_complete",
		"row_id", rowID,
		"score", ytScore,
		"verdict", ytVerdict,
		"profile", profile,
	)
}

// transcribeYouTubeAsync is the transcript-only pipeline for vnote_auto YouTube
// URL shares. Unlike scoreYouTubeAsync, it does NOT score the content — it
// extracts the transcript, saves it to docs/transcripts/, and sends an FCM
// notification immediately via EnqueueTranscriptPush (bypasses min-score floor
// and throttle). EPIC-090 M2.
//
// Pipeline:
//  1. yt-dlp extract → transcript + ytVideoMeta
//  2. saveTranscriptFile (source="youtube", extended frontmatter)
//  3. EnqueueTranscriptPush → FCM push with content_type="youtube_transcript"
func transcribeYouTubeAsync(req ShareRequest, q *Queue, ytPath string, events *EventLogger, whisperModel string) {
	outerTimeout := 120 * time.Second
	if ytFallbackToAudio {
		outerTimeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), outerTimeout)
	defer cancel()

	rowID := req.QueueRowID
	videoURL := req.URL
	profile := req.Profile

	slog.Info("transcribe_youtube: start",
		"event_type", "transcribe_youtube_start",
		"row_id", rowID,
		"url", videoURL,
		"profile", profile,
	)

	if q != nil {
		q.SetProgress(rowID, "extracting")
	}

	// Step 1: normalize URL (resolve redirect wrappers to canonical YouTube form).
	// EPIC-006 M3: inserted before execYtdlp so yt-dlp always receives a canonical URL.
	if normalized, normErr := execNormalizeURL(ctx, videoURL); normErr == nil {
		videoURL = normalized
	}

	// Step 2: yt-dlp extraction.
	transcript, meta, err := execYtdlp(ctx, ytPath, videoURL)
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
		slog.Warn("transcribe_youtube: extraction failed",
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
		// EPIC-001 M3: same audio fallback gate as scoreYouTubeAsync.
		if verdict == "yt_no_subtitles" && ytFallbackToAudio {
			// EPIC-108 M1: acquire semaphore before spawning whisper-cli.
			select {
			case ytAudioSem <- struct{}{}:
				// acquired immediately
			default:
				if events != nil {
					_ = events.Emit("yt_audio_queued_pending_semaphore", map[string]interface{}{"row_id": rowID})
				}
				slog.Info("yt_audio_fallback: waiting for semaphore",
					"event_type", "yt_audio_queued_pending_semaphore",
					"row_id", rowID,
				)
				select {
				case ytAudioSem <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
			transcript, meta, err = ytAudioFallback(ctx, ytPath, videoURL, rowID, q, events, whisperModel)
			<-ytAudioSem
			if err == nil {
				meta.SubtitleType = "audio"
				goto txSubtitleReady
			}
			// EPIC-005 M1: surface yt_audio_timeout specifically.
			if strings.HasPrefix(err.Error(), "yt_audio_timeout") {
				verdict = "yt_audio_timeout"
			}
			slog.Warn("transcribe_youtube: audio fallback also failed",
				"event_type", "transcribe_youtube_fallback_failed",
				"row_id", rowID,
				"url", videoURL,
				"verdict", verdict,
				"error", err,
			)
			// EPIC-108 M3: dead-letter requeue with exponential backoff.
			if q != nil {
				if row, gErr := q.GetByID(rowID); gErr == nil && row != nil && row.RetryCount < ytAudioMaxRetries {
					nextAttempt := row.RetryCount + 1
					if rErr := q.EnqueueAudioRetry(rowID, nextAttempt); rErr == nil {
						slog.Info("yt_audio_retry_enqueued",
							"event_type", "yt_audio_retry_enqueued",
							"row_id", rowID,
							"attempt", nextAttempt,
						)
						if events != nil {
							_ = events.Emit("yt_audio_retry_enqueued", map[string]interface{}{
								"row_id":  rowID,
								"attempt": nextAttempt,
							})
						}
						return
					}
				}
				q.MarkFailedWithReason(rowID, "yt_audio_terminal_failed")
				slog.Warn("yt_audio_terminal_failed",
					"event_type", "yt_audio_terminal_failed",
					"row_id", rowID,
				)
				if events != nil {
					_ = events.Emit("yt_audio_terminal_failed", map[string]interface{}{"row_id": rowID})
				}
				enqueuePrefilterPush(q, &req, "yt_audio_terminal_failed")
			}
			return
		}
		if q != nil {
			q.MarkFailedWithReason(rowID, verdict)
			enqueuePrefilterPush(q, &req, verdict)
		}
		return
	}
txSubtitleReady:

	slog.Info("transcribe_youtube: extracted",
		"event_type", "yt_transcript_extracted",
		"row_id", rowID,
		"url", videoURL,
		"title", meta.Title,
		"video_id", meta.ID,
		"duration", meta.Duration,
		"subtitle_type", meta.SubtitleType,
		"transcript_len", len(transcript),
	)
	if events != nil {
		_ = events.Emit("yt_transcript_extracted", map[string]interface{}{
			"row_id":         rowID,
			"url":            videoURL,
			"title":          meta.Title,
			"video_id":       meta.ID,
			"duration":       meta.Duration,
			"subtitle_type":  meta.SubtitleType,
			"transcript_len": len(transcript),
		})
	}

	// Step 3: save transcript file.
	txPath, err := saveTranscriptFile(rowID, profile, "", transcript, "youtube", videoURL, meta.Title, meta.ID, meta.Duration, meta.SubtitleType)
	if err != nil {
		slog.Warn("transcribe_youtube: save transcript failed", "row_id", rowID, "error", err)
		// Non-fatal — still send FCM so the user knows the transcript is done.
	} else {
		slog.Info("transcribe_youtube: transcript saved",
			"event_type", "transcribe_youtube_transcript_saved",
			"row_id", rowID,
			"path", txPath,
		)
	}

	// Step 4: FCM push via EnqueueTranscriptPush — bypasses min-score floor
	// and throttle. Verdict "transcribed" is the notification body. EPIC-090 M2.
	if q != nil {
		slug := fmt.Sprintf("yt-tx-%d", rowID)
		verdict := "transcribed"
		if meta.Title != "" {
			verdict = meta.Title
		}
		if err := q.EnqueueTranscriptPush(profile, slug, verdict, videoURL); err != nil {
			slog.Warn("transcribe_youtube: EnqueueTranscriptPush failed", "row_id", rowID, "error", err)
		}
	}

	slog.Info("transcribe_youtube: complete",
		"event_type", "transcribe_youtube_complete",
		"row_id", rowID,
		"profile", profile,
	)
}
