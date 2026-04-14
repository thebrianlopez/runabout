package main

// EPIC-060 M1: server-side scoring pipeline for uinit_* actions.
//
// Replaces the tmux → fish uinit → linkari triage chain with a single
// goroutine: fetch → truncate → eval → ScoreByURL → archive → FCM push.
// Activated when ActionConfig.ServerScore is true (set on all builtinConfig
// uinit_* actions by this milestone).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// unsupportedPipelineRE matches URL patterns that cannot be scored via the
// Jina Reader text pipeline (video/audio/streaming platforms). These return
// early with no queue update rather than burning a Haiku call on empty content.
var unsupportedPipelineRE = regexp.MustCompile(`(?i)(?:youtube\.com|youtu\.be|spotify\.com|twitch\.tv|soundcloud\.com|tiktok\.com|netflix\.com)`)

// jinaHTTPClient is the HTTP client for Jina Reader requests. Uses a 35s
// timeout to give Jina a margin above the 30s fetch context timeout —
// the context cancels first; this is a backstop for runaway connections.
var jinaHTTPClient = &http.Client{Timeout: 35 * time.Second}

// jinaBaseURL is the Jina Reader endpoint prefix. Overridden in tests to point
// at a local httptest.Server so fetch calls never hit the real network.
var jinaBaseURL = "https://r.jina.ai/"

// fetchJinaContent retrieves page content via Jina Reader (r.jina.ai).
// Uses ctx for cancellation; callers should pass a context with a 30s deadline.
// Returns the response body as UTF-8 text, capped at 1MB before rune truncation.
func fetchJinaContent(ctx context.Context, rawURL string) (string, error) {
	jinaURL := jinaBaseURL + rawURL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jinaURL, nil)
	if err != nil {
		return "", fmt.Errorf("jina request: %w", err)
	}
	// Jina rejects requests with no User-Agent.
	req.Header.Set("User-Agent", "linkari-server/1.0")
	resp, err := jinaHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("jina fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("jina status %d for %s", resp.StatusCode, rawURL)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB cap
	if err != nil {
		return "", fmt.Errorf("jina read: %w", err)
	}
	return string(b), nil
}

// defaultWhisperModel returns the default whisper model path.
func defaultWhisperModel() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "whisper", "ggml-large-v3-turbo.bin")
}

// execFfmpegConvert is the function var for converting audio files via ffmpeg.
// Tests override this to avoid real ffmpeg invocation.
var execFfmpegConvert = runFfmpegConvert

// runFfmpegConvert invokes ffmpeg to convert an audio file to 16kHz mono WAV.
func runFfmpegConvert(ctx context.Context, inputPath, outputPath string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", inputPath,
		"-ar", "16000",
		"-ac", "1",
		"-y",
		outputPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w (stderr: %s)", err, stderr.String())
	}
	return nil
}

// execWhisper is the function var for running whisper-cli. Tests override this
// to avoid real transcription. Same pattern as execHaiku.
var execWhisper = runWhisperCLI

// runWhisperCLI invokes whisper-cli to transcribe a WAV file. Returns the
// transcript text. The model path is resolved from server config or default.
func runWhisperCLI(ctx context.Context, wavPath, modelPath string) (string, error) {
	if modelPath == "" {
		modelPath = defaultWhisperModel()
	}
	cmd := exec.CommandContext(ctx, "whisper-cli",
		"--model", modelPath,
		"--file", wavPath,
		"--no-timestamps",
		"--output-txt",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("whisper-cli: %w (stderr: %s)", err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// execFfmpegSegment is the function var for segmenting a WAV file into chunks.
// Tests override this to avoid real ffmpeg invocation.
var execFfmpegSegment = runFfmpegSegment

// runFfmpegSegment invokes ffmpeg to split a WAV file into fixed-duration
// chunks using the segment muxer. Returns paths of the produced chunk files.
func runFfmpegSegment(ctx context.Context, wavPath string, segmentSecs int) ([]string, error) {
	dir := filepath.Dir(wavPath)
	base := strings.TrimSuffix(filepath.Base(wavPath), filepath.Ext(wavPath))
	pattern := filepath.Join(dir, base+"_chunk_%03d.wav")

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", wavPath,
		"-f", "segment",
		"-segment_time", fmt.Sprintf("%d", segmentSecs),
		"-c", "copy",
		pattern,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg segment: %w (stderr: %s)", err, stderr.String())
	}

	// Glob for produced chunks — ffmpeg names them sequentially.
	chunks, err := filepath.Glob(filepath.Join(dir, base+"_chunk_*.wav"))
	if err != nil {
		return nil, fmt.Errorf("glob chunks: %w", err)
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("ffmpeg segment produced no chunks")
	}
	// filepath.Glob returns sorted results, which matches segment order.
	return chunks, nil
}

// audioChunkSeconds is the segment duration for chunked whisper transcription.
// 10 minutes keeps whisper RAM at ~340MB per chunk instead of ~900MB monolithic.
const audioChunkSeconds = 600

// audioChunkSizeThreshold is the WAV file size above which we chunk. Files
// under this size are transcribed in a single whisper pass. 50MB WAV ≈ 26 min
// at 16kHz mono — a reasonable breakpoint where whisper memory becomes a concern.
const audioChunkSizeThreshold = 50 << 20

// classificationPreamble returns a preamble to prepend to the evaluator
// prompt when the profile was auto-classified from a URL. This tells the
// LLM which profile rubric to apply and why.
func classificationPreamble(profile, rawURL string) string {
	return fmt.Sprintf(
		"[Auto-classified profile: %s (from URL: %s)]\n"+
			"Score this content using the %s profile rubric.\n\n",
		profile, rawURL, profile,
	)
}

// scoreURLAsync runs the full server-side scoring pipeline for a uinit_auto share.
// Must be launched as a goroutine from handleTemplate. The goroutine owns a
// 60s context independent of the HTTP request to prevent client-disconnect
// cancellation from aborting a scoring run that has already started.
//
// Pipeline: auto-classify profile → unsupported-check → Jina fetch (30s) →
// truncate → loadProfileTemplate → classification preamble → eval.Evaluate
// (Haiku) → ScoreByURL → archive → EnqueueDigestIfDue (FCM push).
//
// Takes eval as a parameter so tests can inject a stub Evaluator without
// touching the execHaiku var (test-seam choice from EPIC-060 assessment).
func scoreURLAsync(rawURL, profile string, q *Queue, eval Evaluator) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// EPIC-061 M3: auto-classify profile from URL when empty.
	autoClassified := false
	contentClassify := false // true when domain match failed and content classification is needed
	if profile == "" {
		classified, matched := classifyURLProfile(rawURL)
		profile = classified
		autoClassified = true
		if !matched {
			contentClassify = true
		}
	}

	slog.Info("server_score: start",
		"event_type", "server_score_start",
		"url", rawURL,
		"profile", profile,
		"auto_classified", autoClassified,
	)

	// Early exit for video/audio platforms — Jina returns empty or JS-stripped
	// content that would produce noise-gate 0-score results.
	if unsupportedPipelineRE.MatchString(rawURL) {
		slog.Info("server_score: unsupported pipeline",
			"event_type", "server_score_skip",
			"url", rawURL,
			"profile", profile,
			"reason", "unsupported_pipeline",
		)
		return
	}

	// Fetch page content. 30s sub-context leaves headroom within the 60s budget.
	fetchCtx, fetchCancel := context.WithTimeout(ctx, 30*time.Second)
	defer fetchCancel()
	content, err := fetchJinaContent(fetchCtx, rawURL)
	if err != nil {
		slog.Warn("server_score: fetch failed",
			"event_type", "server_score_fetch_error",
			"url", rawURL,
			"profile", profile,
			"error", err,
		)
		return
	}
	content = truncateRunes(content, contentTruncationRunes)
	if strings.TrimSpace(content) == "" {
		slog.Warn("server_score: empty content after fetch",
			"event_type", "server_score_empty_content",
			"url", rawURL,
			"profile", profile,
		)
		return
	}

	// Content-based classification: when domain matching fell through to the
	// "eng" fallback, ask Haiku to classify the fetched content into the best
	// profile. This is cheap (~100 tokens) and avoids scoring travel/life/dining
	// content under the eng rubric.
	if contentClassify {
		classified := classifyContentProfile(ctx, content)
		if classified != "" {
			slog.Info("server_score: content-classified profile",
				"event_type", "server_score_content_classify",
				"url", rawURL,
				"domain_fallback", profile,
				"content_classified", classified,
			)
			profile = classified
		}
	}

	// Load profile template (same precedence as cmd_triage loadProfileTemplate).
	_, sysPrompt, err := loadProfileTemplate(profile)
	if err != nil {
		slog.Warn("server_score: load template failed",
			"event_type", "server_score_template_error",
			"url", rawURL,
			"profile", profile,
			"error", err,
		)
		return
	}

	// EPIC-061 M3: prepend classification preamble when auto-classified.
	if autoClassified {
		sysPrompt = classificationPreamble(profile, rawURL) + sysPrompt
	}

	// Evaluate via Haiku. eval.Evaluate routes through execHaiku indirection —
	// the test seam is preserved without changes to execHaiku callers.
	sc, err := eval.Evaluate(ctx, content, sysPrompt)
	if err != nil {
		slog.Warn("server_score: evaluate failed",
			"event_type", "server_score_eval_error",
			"url", rawURL,
			"profile", profile,
			"error", err,
		)
		return
	}
	sc.Profile = profile
	sc.SourceType = "server-score"

	slog.Info("server_score: evaluated",
		"event_type", "server_score_evaluated",
		"url", rawURL,
		"profile", profile,
		"score", sc.Score,
		"verdict", sc.Verdict,
		"latency_ms", sc.LatencyMs,
	)

	// Persist — ScoreByURL finds the queued row by URL and updates it to scored.
	// If q is nil (no queue configured), scoring completes but nothing is stored.
	if q == nil {
		return
	}
	slug := deriveSlugFromURL(rawURL)
	item, _, err := q.ScoreByURL(rawURL, sc.Score, sc.Verdict, sc.Tags, profile, slug, sc.RubricScores)
	if err != nil {
		slog.Warn("server_score: ScoreByURL failed",
			"event_type", "server_score_queue_error",
			"url", rawURL,
			"profile", profile,
			"error", err,
		)
		return
	}

	// EPIC-072 M5: persist topic tags from scoring.
	if len(sc.TopicTags) > 0 {
		if tagErr := q.SetTopicTags(item.ID, sc.TopicTags); tagErr != nil {
			slog.Warn("server_score: SetTopicTags failed", "id", item.ID, "error", tagErr)
		}
	}

	// Auto-archive when score meets profile threshold (mirrors cmd_score path).
	threshold := archiveThreshold(profile)
	if threshold >= 0 && item.Score != nil && *item.Score >= threshold {
		if archErr := q.Archive(item.ID); archErr == nil {
			item.Status = "archived"
		}
	}

	// EPIC-072 M6: trigger cluster detection after scoring (background goroutine).
	if len(sc.TopicTags) > 0 && profile != "" {
		go detectClusters(context.Background(), q, profile, 0, 0)
	}

	// EPIC-072 M9: action route dispatch post-scoring.
	if item.Score != nil {
		dispatchActionRoute(context.Background(), sc, profile, rawURL, q, item.ID, 0)
	}

	// FCM push via the dual-writer invariant (EPIC-051). Every scored row
	// produces a push regardless of archive status.
	if item.Score != nil {
		resolvePushConfigOnce(q)
		_, _ = q.EnqueueDigestIfDue(context.Background(),
			item.Profile, *item.Score, item.Slug, item.Verdict, item.URL,
			sc.GapSummary(3))
	}

	slog.Info("server_score: complete",
		"event_type", "server_score_complete",
		"url", rawURL,
		"profile", profile,
		"score", sc.Score,
		"status", item.Status,
	)
}

// validProfiles is the set of profiles that classifyContentProfile accepts.
var validProfiles = map[string]bool{
	"eng": true, "travel": true, "life": true,
	"dining": true, "fashion": true, "finance": true, "music": true,
}

// contentClassifyPrompt is the system prompt for the lightweight Haiku
// classification call. Designed for minimal token usage.
const contentClassifyPrompt = "Given URL content, respond with exactly one profile name from: eng, travel, life, dining, fashion, finance, music. No explanation."

// execContentClassify is the function var used to call Haiku for content
// classification. Tests override this to avoid real API calls.
var execContentClassify = execHaiku

// classifyContentProfile calls Haiku with a minimal prompt to classify page
// content into the best-matching profile. Returns the classified profile, or
// empty string if the response is unparseable (caller falls back to "eng").
func classifyContentProfile(ctx context.Context, content string) string {
	// Truncate content aggressively — classification needs much less than scoring.
	snippet := truncateRunes(content, 2000)
	out, err := execContentClassify(ctx, contentClassifyPrompt, snippet)
	if err != nil {
		slog.Warn("content classify: haiku failed", "error", err)
		return ""
	}
	result := strings.TrimSpace(strings.ToLower(out))
	if validProfiles[result] {
		return result
	}
	// Try to extract a valid profile from a longer response.
	for p := range validProfiles {
		if strings.Contains(result, p) {
			return p
		}
	}
	slog.Warn("content classify: unparseable response", "raw", out)
	return ""
}

// scoreAudioAsync runs the voice note transcription and synopsis pipeline.
// Must be launched as a goroutine from handleTemplate. Owns a 1800s context
// independent of the HTTP request to accommodate 200MB uploads.
//
// EPIC-071 M2: replaced eval.Evaluate (JSON scorecard) with execHaiku
// (plain text synopsis) using the vnote_synopsis prompt template. Audio
// shares no longer produce a numeric score — they produce a 1-2 sentence
// synopsis pushed via FCM with content_type=voice_note.
//
// Pipeline: ffmpeg m4a→wav → [segment if large] → whisper transcribe →
// backfill queue text → save transcript file → loadProfileTemplate("vnote_synopsis") →
// execHaiku (plain text) → UpdateScore → EnqueueDigestIfDue (FCM push).
func scoreAudioAsync(audioPath string, profile string, q *Queue, rowID int64, originalFilename string, whisperModel string) {
	ctx, cancel := context.WithTimeout(context.Background(), 1800*time.Second)
	defer cancel()
	defer os.Remove(audioPath) // temp m4a cleanup

	slog.Info("score_audio: start",
		"event_type", "score_audio_start",
		"row_id", rowID,
		"audio_path", audioPath,
		"original_filename", originalFilename,
	)

	if q != nil {
		q.SetProgress(rowID, "converting")
	}

	// Step 1: ffmpeg convert m4a → wav (16kHz mono for whisper).
	// 60s timeout for 200MB conversions (up from 30s).
	wavPath := audioPath + ".wav"
	defer os.Remove(wavPath) // temp wav cleanup

	ffmpegCtx, ffmpegCancel := context.WithTimeout(ctx, 60*time.Second)
	defer ffmpegCancel()
	if err := execFfmpegConvert(ffmpegCtx, audioPath, wavPath); err != nil {
		slog.Warn("score_audio: ffmpeg failed",
			"event_type", "score_audio_ffmpeg_error",
			"row_id", rowID,
			"error", err,
		)
		if q != nil {
			q.MarkFailedWithReason(rowID, "ffmpeg_failed")
		}
		return
	}

	// Step 2: whisper transcribe — chunk if WAV is large enough.
	var transcript string
	wavInfo, err := os.Stat(wavPath)
	if err != nil {
		slog.Warn("score_audio: stat wav failed", "row_id", rowID, "error", err)
		if q != nil {
			q.MarkFailedWithReason(rowID, "wav_stat_failed")
		}
		return
	}

	if wavInfo.Size() > audioChunkSizeThreshold {
		// Large file — segment into chunks and transcribe sequentially.
		if q != nil {
			q.SetProgress(rowID, "segmenting")
		}

		segCtx, segCancel := context.WithTimeout(ctx, 30*time.Second)
		chunks, err := execFfmpegSegment(segCtx, wavPath, audioChunkSeconds)
		segCancel()
		if err != nil {
			slog.Warn("score_audio: segment failed",
				"event_type", "score_audio_segment_error",
				"row_id", rowID,
				"error", err,
			)
			if q != nil {
				q.MarkFailedWithReason(rowID, "segment_failed")
			}
			return
		}
		// Clean up chunk files on exit.
		defer func() {
			for _, c := range chunks {
				os.Remove(c)
			}
		}()

		var parts []string
		for i, chunk := range chunks {
			if q != nil {
				q.SetProgress(rowID, fmt.Sprintf("transcribing %d/%d", i+1, len(chunks)))
			}
			slog.Info("score_audio: transcribing chunk",
				"row_id", rowID,
				"chunk", i+1,
				"total", len(chunks),
			)
			part, err := execWhisper(ctx, chunk, whisperModel)
			if err != nil {
				slog.Warn("score_audio: whisper chunk failed",
					"event_type", "score_audio_whisper_error",
					"row_id", rowID,
					"chunk", i+1,
					"error", err,
				)
				if q != nil {
					q.MarkFailedWithReason(rowID, fmt.Sprintf("transcription_failed_chunk_%d", i+1))
				}
				return
			}
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
		transcript = strings.Join(parts, "\n\n")
	} else {
		// Small file — single-pass transcription.
		if q != nil {
			q.SetProgress(rowID, "transcribing 1/1")
		}
		transcript, err = execWhisper(ctx, wavPath, whisperModel)
		if err != nil {
			slog.Warn("score_audio: whisper failed",
				"event_type", "score_audio_whisper_error",
				"row_id", rowID,
				"error", err,
			)
			if q != nil {
				q.MarkFailedWithReason(rowID, "transcription_failed")
			}
			return
		}
	}

	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		slog.Warn("score_audio: empty transcript",
			"event_type", "score_audio_empty_transcript",
			"row_id", rowID,
		)
		if q != nil {
			q.MarkFailedWithReason(rowID, "empty_transcript")
		}
		return
	}

	slog.Info("score_audio: transcribed",
		"event_type", "score_audio_transcribed",
		"row_id", rowID,
		"transcript_len", len(transcript),
	)

	// Step 3: backfill transcript into queue row.
	if q != nil {
		if err := q.SetText(rowID, transcript); err != nil {
			slog.Warn("score_audio: SetText failed", "row_id", rowID, "error", err)
		}
	}

	// Step 4: save transcript file to docs/transcripts/ (EPIC-071 M2).
	if profile == "" {
		profile = "voice"
	}
	txPath, err := saveTranscriptFile(rowID, profile, originalFilename, transcript)
	if err != nil {
		slog.Warn("score_audio: save transcript failed",
			"row_id", rowID,
			"error", err,
		)
		// Non-fatal — continue to synopsis generation.
	} else {
		slog.Info("score_audio: transcript saved",
			"event_type", "score_audio_transcript_saved",
			"row_id", rowID,
			"path", txPath,
		)
	}

	// Step 5: load vnote_synopsis template and generate synopsis via Haiku.
	// EPIC-071 M2: uses execHaiku directly for plain text output instead of
	// eval.Evaluate which expects TriageVerdict JSON.
	_, sysPrompt, err := loadProfileTemplate("vnote_synopsis")
	if err != nil {
		slog.Warn("score_audio: load vnote_synopsis template failed",
			"event_type", "score_audio_template_error",
			"row_id", rowID,
			"error", err,
		)
		if q != nil {
			q.MarkFailedWithReason(rowID, "template_load_failed")
		}
		return
	}

	// Replace {{transcript}} placeholder with actual transcript in the prompt.
	sysPrompt = strings.Replace(sysPrompt, "{{transcript}}", transcript, 1)

	if q != nil {
		q.SetProgress(rowID, "summarizing")
	}

	synopsis, err := execHaiku(ctx, sysPrompt, transcript)
	if err != nil {
		slog.Warn("score_audio: synopsis failed",
			"event_type", "score_audio_synopsis_error",
			"row_id", rowID,
			"error", err,
		)
		if q != nil {
			q.MarkFailedWithReason(rowID, "synopsis_failed")
		}
		return
	}
	synopsis = strings.TrimSpace(synopsis)

	slog.Info("score_audio: synopsis generated",
		"event_type", "score_audio_synopsis",
		"row_id", rowID,
		"synopsis_len", len(synopsis),
		"synopsis", synopsis,
	)

	// Step 6: persist — score=100 ensures push delivery past any min-score floor.
	if q == nil {
		return
	}
	slug := fmt.Sprintf("vnote-%d", rowID)
	if err := q.UpdateScore(rowID, 100, "", synopsis, slug); err != nil {
		slog.Warn("score_audio: UpdateScore failed",
			"event_type", "score_audio_queue_error",
			"row_id", rowID,
			"error", err,
		)
		return
	}

	// Step 7: auto-archive voice notes (always — no threshold gating).
	q.Archive(rowID)

	// Step 8: FCM push via the dual-writer invariant (EPIC-051).
	// score=100 bypasses NotifyMinScore floor. content_type="voice_note"
	// tells the Android client to render synopsis instead of score.
	resolvePushConfigOnce(q)
	_, _ = q.EnqueueDigestIfDue(context.Background(),
		profile, 100, slug, synopsis, "", "", "voice_note")

	slog.Info("score_audio: complete",
		"event_type", "score_audio_complete",
		"row_id", rowID,
		"profile", profile,
		"synopsis", synopsis,
	)
}

// transcriptDir is the directory where voice note transcripts are saved.
// Resolved relative to the user's docs/transcripts/ path.
var transcriptDir = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "code", "personal", "docs", "transcripts")
}()

// saveTranscriptFile writes a voice note transcript to docs/transcripts/ with
// YAML frontmatter containing metadata. Returns the written file path or error.
// EPIC-071 M2.
func saveTranscriptFile(rowID int64, profile, originalFilename, transcript string) (string, error) {
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		return "", fmt.Errorf("create transcript dir: %w", err)
	}

	now := time.Now().UTC()
	date := now.Format("20060102")
	ts := now.Format("20060102T150405Z")

	// Sanitize original filename for use in the path — replace spaces,
	// non-ASCII, and filesystem-illegal characters with underscores,
	// then collapse repeated underscores.
	safeName := sanitizeTranscriptFilename(originalFilename)
	if safeName == "" {
		safeName = "untitled"
	}
	// Strip extension — the file is always .md.
	safeName = strings.TrimSuffix(safeName, filepath.Ext(safeName))

	filename := fmt.Sprintf("%s_%d_%s.md", date, rowID, safeName)
	path := filepath.Join(transcriptDir, filename)

	var buf strings.Builder
	fmt.Fprintf(&buf, "---\n")
	fmt.Fprintf(&buf, "timestamp: %q\n", ts)
	fmt.Fprintf(&buf, "row_id: %d\n", rowID)
	fmt.Fprintf(&buf, "profile: %q\n", profile)
	fmt.Fprintf(&buf, "original_filename: %q\n", originalFilename)
	fmt.Fprintf(&buf, "---\n\n")
	buf.WriteString(transcript)
	buf.WriteString("\n")

	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		return "", fmt.Errorf("write transcript: %w", err)
	}
	return path, nil
}

// sanitizeFilenameRE matches any character that is not alphanumeric, underscore, or dot.
var sanitizeFilenameRE = regexp.MustCompile(`[^A-Za-z0-9_.]`)

// collapseUnderscoresRE matches two or more consecutive underscores.
var collapseUnderscoresRE = regexp.MustCompile(`_{2,}`)

// sanitizeTranscriptFilename replaces spaces, non-ASCII, and filesystem-illegal
// characters with underscores, then collapses repeated underscores.
func sanitizeTranscriptFilename(name string) string {
	name = sanitizeFilenameRE.ReplaceAllString(name, "_")
	name = collapseUnderscoresRE.ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	return name
}
