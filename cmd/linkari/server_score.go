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
	"errors"
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
func classificationPreamble(profile, rawURL string, source string) string {
	if source == "" {
		source = "url"
	}
	return fmt.Sprintf(
		"[Auto-classified profile: %s (source: %s, URL: %s)]\n"+
			"Score this content using the %s profile rubric.\n\n",
		profile, source, rawURL, profile,
	)
}

// scoreAsync is the unified server-side scoring pipeline for URL and file
// shares. EPIC-077 M5: merges scoreURLAsync and scoreFileAsync into a single
// function that branches on req.Type only for content acquisition.
//
// Content acquisition:
//   - URL shares (req.Type=="url"): Jina Reader fetch (30s) or screenshot text
//   - File shares (req.Type=="image","document"): metadata synthesis (instant)
//
// URL-only features (topic tags, clusters, action routes, unsupported platform
// guards) are gated on req.Type=="url" to preserve existing behavior.
//
// Must be launched as a goroutine from handleTemplate.
// Takes eval as a parameter so tests can inject a stub Evaluator.
func scoreAsync(req *ShareRequest, q *Queue, eval Evaluator, events *EventLogger) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// EPIC-079 M3: clean up temp file for image/document shares.
	// scoreAsync now owns the temp file (disarmed in handleShare via audioCleanup="").
	if req.AudioPath != "" && req.Type != "audio" {
		defer os.Remove(req.AudioPath)
	}

	isURLShare := req.Type == "url"
	rawURL := req.URL
	profile := req.Profile

	// Screenshot detection — unconditional, orthogonal to profile assignment.
	detectScreenshot(req)

	// EPIC-081 M2: route image file shares to image_triage profile.
	// This bypasses the classification cascade — images always use
	// image_triage for vision-specific rubric evaluation.
	if req.Type == "image" && profile == "" {
		profile = "image_triage"
	}

	// Classification cascade — unified for URL and file shares.
	autoClassified := false
	contentClassify := false
	classifySource := ""
	if profile == "" {
		profile, classifySource = classifyShareRequest(ctx, req)
		if profile != "" {
			autoClassified = true
		}
		if classifySource == "url_domain_fallback" {
			contentClassify = true
		}
	}
	// EPIC-079 M1: removed default_fallback block — file shares now fall
	// through to stage-6 LLM classify in scoreAsync (classifyContentProfile)
	// instead of being hardcoded to "eng".
	if profile == "" && !isURLShare {
		contentClassify = true
	}

	// Emit classify_stage_win for telemetry (EPIC-077 M6).
	//
	// Accuracy baseline queries (linkari_events.jsonl):
	//   # Distribution of winning cascade stages:
	//   jq 'select(.event_type=="classify_stage_win") | .classify_source' linkari_events.jsonl | sort | uniq -c
	//
	//   # Per-profile stage breakdown:
	//   jq 'select(.event_type=="classify_stage_win") | {profile, classify_source}' linkari_events.jsonl | sort | uniq -c
	//
	//   # Content-LLM reclassification rate (proxy for URL domain fallback frequency):
	//   jq 'select(.event_type=="classify_stage_win" and .classify_source=="content")' linkari_events.jsonl | wc -l
	if events != nil {
		_ = events.Emit("classify_stage_win", map[string]interface{}{
			"url":             rawURL,
			"profile":         profile,
			"classify_source": classifySource,
			"auto_classified": autoClassified,
			"row_id":          req.QueueRowID,
			"content_type":    req.Type,
		})
	}

	slog.Info("score_async: start",
		"event_type", "score_async_start",
		"url", rawURL,
		"type", req.Type,
		"profile", profile,
		"auto_classified", autoClassified,
		"classify_source", classifySource,
		"filename", req.Filename,
		"row_id", req.QueueRowID,
	)

	// URL-only: early exit for unsupported streaming platforms.
	if isURLShare && unsupportedPipelineRE.MatchString(rawURL) {
		slog.Info("score_async: unsupported pipeline",
			"event_type", "score_async_skip",
			"url", rawURL,
			"reason", "unsupported_pipeline",
		)
		return
	}

	// Content acquisition — branches on share type.
	var content string
	if isURLShare {
		if req.IsScreenshot {
			// Screenshots use ExtraSubject+ExtraText instead of Jina.
			screenshotContent := strings.TrimSpace(req.ExtraSubject + "\n" + req.ExtraText)
			if screenshotContent == "" {
				slog.Info("score_async: screenshot no text",
					"event_type", "score_async_skip",
					"url", rawURL,
					"reason", "screenshot_no_text",
				)
				return
			}
			if profile == "" || contentClassify {
				if classified := classifyContentProfile(ctx, screenshotContent); classified != "" {
					profile = classified
					autoClassified = true
					contentClassify = false
				}
			}
			content = screenshotContent
		} else {
			// Jina fetch for normal URL shares.
			fetchCtx, fetchCancel := context.WithTimeout(ctx, 30*time.Second)
			defer fetchCancel()
			var err error
			content, err = fetchJinaContent(fetchCtx, rawURL)
			if err != nil {
				slog.Warn("score_async: fetch failed",
					"event_type", "score_async_fetch_error",
					"url", rawURL,
					"error", err,
				)
				return
			}
			content = truncateRunes(content, contentTruncationRunes)
			if strings.TrimSpace(content) == "" {
				slog.Warn("score_async: empty content", "event_type", "score_async_empty_content", "url", rawURL)
				return
			}

			// Content-based profile reclassification when domain fallback triggered.
			if contentClassify {
				classifyInput := content
				if req.ExtraSubject != "" {
					classifyInput = "[Shared with subject: " + req.ExtraSubject + "]\n\n" + content
				}
				if classified := classifyContentProfile(ctx, classifyInput); classified != "" {
					slog.Info("score_async: content-classified",
						"event_type", "score_async_content_classify",
						"url", rawURL,
						"content_classified", classified,
					)
					profile = classified
					classifySource = "content"
				}
			}
		}
	} else {
		// File share: synthesize content from metadata.
		var parts []string
		if req.ExtraSubject != "" {
			parts = append(parts, "Subject: "+req.ExtraSubject)
		}
		if req.ExtraText != "" {
			parts = append(parts, "Text: "+req.ExtraText)
		}
		if req.Filename != "" {
			parts = append(parts, "Filename: "+req.Filename)
		}
		if req.MimeType != "" {
			parts = append(parts, "Type: "+req.MimeType)
		}
		// EPIC-081 M3: include file size in content synthesis.
		if req.FileSize > 0 {
			parts = append(parts, fmt.Sprintf("FileSize: %d bytes", req.FileSize))
		}
		content = strings.Join(parts, "\n")
		if strings.TrimSpace(content) == "" {
			slog.Info("score_async: no metadata",
				"event_type", "score_async_skip",
				"type", req.Type,
				"reason", "no_metadata",
			)
			return
		}

		// EPIC-079 M1: stage-6 LLM classify for file shares that had no
		// profile signal from the fast cascade.
		if contentClassify {
			if classified := classifyContentProfile(ctx, content); classified != "" {
				slog.Info("score_async: file content-classified",
					"event_type", "score_async_content_classify",
					"type", req.Type,
					"content_classified", classified,
				)
				profile = classified
				classifySource = "content"
			}
		}
	}

	// Load profile template.
	// EPIC-081 M2: use vision mode for image shares to select vision-specific
	// rubric axes and persona intro from the profile manifest.
	// EPIC-082 M1: capture tmplPath for prompt traceability.
	var tmplPath, sysPrompt string
	var err error
	if req.Type == "image" {
		tmplPath, sysPrompt, err = loadProfileTemplateForMode(profile, "vision")
	} else {
		tmplPath, sysPrompt, err = loadProfileTemplate(profile)
	}
	if err != nil {
		slog.Warn("score_async: load template failed",
			"event_type", "score_async_template_error",
			"profile", profile,
			"error", err,
		)
		return
	}
	if autoClassified {
		sysPrompt = classificationPreamble(profile, rawURL, classifySource) + sysPrompt
	}

	// EPIC-079 M3: use vision evaluator for image shares with a readable file.
	// EPIC-080 M7: when image file is absent, log degradation — the image is
	// not recoverable from the queue row after the HTTP handler completes.
	// EPIC-081 M3: noise gate — skip vision subprocess for low-metadata images
	// to save ~$0.04/call when the image is too small and has no text context.
	if req.Type == "image" {
		if req.AudioPath != "" {
			if _, statErr := os.Stat(req.AudioPath); statErr == nil {
				// Noise gate: skip vision for images below threshold with no metadata.
				hasMetadata := req.ExtraText != "" || req.ExtraSubject != ""
				if req.FileSize > 0 && req.FileSize < imageNoiseGateMinBytes && !hasMetadata {
					slog.Info("score_async: image noise gate — skipping vision",
						"event_type", "image_noise_gate_skip",
						"row_id", req.QueueRowID,
						"file_size", req.FileSize,
						"filename", req.Filename,
						"min_bytes", imageNoiseGateMinBytes,
					)
					if events != nil {
						events.Emit("image_noise_gate_skip", map[string]any{
							"row_id":    req.QueueRowID,
							"file_size": req.FileSize,
							"filename":  req.Filename,
						})
					}
					// Skip vision — fall through to metadata-only eval below.
				} else {
					eval = HaikuVisionEvaluator{ImagePath: req.AudioPath}
				}
			}
		} else {
			slog.Warn("score_async: image share without file — scoring with metadata only",
				"event_type", "score_async_vision_degraded",
				"row_id", req.QueueRowID,
				"filename", req.Filename,
			)
		}
	}

	// Evaluate via Haiku.
	sc, err := eval.Evaluate(ctx, content, sysPrompt)
	if err != nil {
		logArgs := []any{
			"event_type", "score_async_eval_error",
			"profile", profile,
			"error", err,
		}
		if req.AudioPath != "" {
			logArgs = append(logArgs, "image_path", req.AudioPath)
			if fi, statErr := os.Stat(req.AudioPath); statErr != nil {
				logArgs = append(logArgs, "image_stat_error", statErr.Error())
			} else {
				logArgs = append(logArgs, "image_size", fi.Size(), "image_mode", fi.Mode().String())
			}
		}
		slog.Warn("score_async: evaluate failed", logArgs...)
		// EPIC-080 M2: mark queue row as failed so it doesn't stay stuck in
		// relayed status forever (watchdog can't rescue file shares).
		if q != nil && req.QueueRowID > 0 {
			if mErr := q.MarkFailedWithReason(req.QueueRowID, "eval_failed"); mErr != nil {
				slog.Warn("score_async: MarkFailedWithReason failed", "row_id", req.QueueRowID, "error", mErr)
			}
		}
		return
	}
	sc.Profile = profile
	sc.SourceType = "server-score"
	// EPIC-082 M1: prompt traceability — populate hash and version from template.
	sc.PromptHash = promptHash(sysPrompt)
	sc.PromptVersion = promptVersionFromPath(tmplPath)

	slog.Info("score_async: evaluated",
		"event_type", "score_async_evaluated",
		"url", rawURL,
		"type", req.Type,
		"profile", profile,
		"score", sc.Score,
		"classify_source", classifySource,
	)

	if q == nil {
		return
	}

	// Persist — URL shares use ScoreByURL (URL-based dedup upsert);
	// file shares use ScoreByID (row-ID key, idempotency guard).
	var itemID int64
	var itemScore *int
	var itemStatus, itemProfile, itemSlug, itemVerdict, itemURL string
	if isURLShare {
		slug := deriveSlugFromURL(rawURL)
		item, _, err := q.ScoreByURL(rawURL, sc.Score, sc.Verdict, sc.Tags, profile, slug, sc.PromptHash, sc.PromptVersion, sc.RubricScores)
		if err != nil {
			slog.Warn("score_async: ScoreByURL failed", "url", rawURL, "error", err)
			return
		}
		itemID = item.ID
		itemScore = item.Score
		itemStatus = item.Status
		itemProfile = item.Profile
		itemSlug = item.Slug
		itemVerdict = item.Verdict
		itemURL = item.URL
	} else {
		slug := fmt.Sprintf("file-%d", req.QueueRowID)
		_, err := q.ScoreByID(req.QueueRowID, sc.Score, sc.Tags, sc.Verdict, slug, sc.PromptHash, sc.PromptVersion)
		if err != nil {
			slog.Warn("score_async: ScoreByID failed", "row_id", req.QueueRowID, "error", err)
			return
		}
		itemID = req.QueueRowID
		score := sc.Score
		itemScore = &score
		itemStatus = "scored"
		itemProfile = profile
		itemSlug = slug
		itemVerdict = sc.Verdict
		itemURL = ""
	}

	// EPIC-079 M4: persist topic tags for all share types (was URL-only).
	if len(sc.TopicTags) > 0 {
		if tagErr := q.SetTopicTags(itemID, sc.TopicTags); tagErr != nil {
			slog.Warn("score_async: SetTopicTags failed", "id", itemID, "error", tagErr)
		}
	}

	// Auto-archive when score meets threshold.
	threshold := archiveThreshold(itemProfile)
	if itemScore != nil && threshold >= 0 && *itemScore >= threshold {
		if archErr := q.Archive(itemID); archErr == nil {
			itemStatus = "archived"
		}
	}

	// URL-only: cluster detection.
	if isURLShare && len(sc.TopicTags) > 0 && itemProfile != "" {
		go detectClusters(context.Background(), q, itemProfile, 0, 0)
	}

	// URL-only: action route dispatch.
	if isURLShare && itemScore != nil {
		dispatchActionRoute(context.Background(), sc, itemProfile, rawURL, q, itemID, 0)
	}

	// FCM push via dual-writer invariant (EPIC-051).
	// EPIC-077 M6: classify_source threaded through to FCM payload for provenance.
	if itemScore != nil {
		resolvePushConfigOnce(q)
		_, _ = q.EnqueueDigestIfDue(context.Background(),
			itemProfile, *itemScore, itemSlug, itemVerdict, itemURL,
			sc.GapSummary(3), "", classifySource)
	}

	slog.Info("score_async: complete",
		"event_type", "score_async_complete",
		"url", rawURL,
		"type", req.Type,
		"profile", itemProfile,
		"score", sc.Score,
		"status", itemStatus,
		"classify_source", classifySource,
	)
}

// scoreURLAsync delegates to scoreAsync. EPIC-077 M5: retained because
// tests and some call sites use it by name. Ensures req.Type == "url" so
// scoreAsync takes the URL-share branch regardless of how the caller constructed
// the request (legacy callers may leave Type empty when URL is populated).
func scoreURLAsync(req *ShareRequest, q *Queue, eval Evaluator, events *EventLogger) {
	if req.Type == "" && req.URL != "" {
		req.Type = "url"
	}
	scoreAsync(req, q, eval, events)
}

// scoreFileAsync delegates to scoreAsync. EPIC-077 M5: retained for the same
// reason as scoreURLAsync. handleTemplate dispatch will be updated to call
// scoreAsync directly.
func scoreFileAsync(req *ShareRequest, q *Queue, eval Evaluator, events *EventLogger) {
	scoreAsync(req, q, eval, events)
}

// imageNoiseGateMinBytes is the minimum file size in bytes to invoke vision
// subprocess. Images below this with no text metadata skip vision. Set from
// ServerConfig.ImageNoiseGateMinBytes at startup; defaults to 1024 (1KB).
var imageNoiseGateMinBytes int64 = 1024

// validProfiles is the set of profiles that classifyContentProfile accepts.
var validProfiles = map[string]bool{
	"eng": true, "travel": true, "life": true,
	"dining": true, "fashion": true, "finance": true, "music": true,
	"image_triage": true,
}

// EPIC-038 M4: packageProfileMap maps known Android package names to Linkari
// profiles. Used as a high-confidence signal before URL-based classification.
var packageProfileMap = map[string]string{
	"com.spotify.music":                "music",
	"com.google.android.youtube":       "eng",
	"com.google.android.apps.youtube.music": "music",
	"com.soundcloud.android":           "music",
	"com.airbnb.android":               "travel",
	"com.booking":                      "travel",
	"com.tripadvisor.tripadvisor":      "travel",
	"com.google.android.apps.maps":     "travel",
	"com.robinhood.android":            "finance",
	"com.venmo":                        "finance",
	"com.squareup.cash":                "finance",
	"com.mint":                         "finance",
	"com.coinbase.android":             "finance",
	// NOTE: com.instagram.android and com.reddit.frontpage intentionally omitted
	// (EPIC-075 M4): these are multi-topic apps whose shares do not reliably
	// indicate a single profile. They fall through to URL/content classification.
	"com.twitter.android":              "life",
	"com.github.android":               "eng",
	"org.mozilla.firefox":              "eng",
	"com.chrome.beta":                  "eng",
	"com.amazon.mShop.android.shopping": "life",
	"com.ubercab.eats":                 "dining",
	"com.doordash.driverapp":           "dining",
	"com.grubhub.android":              "dining",
	"com.yelp.android":                 "dining",
	"com.opentable":                    "dining",
}

// EPIC-038 M4: appCategoryProfileMap maps Android ApplicationInfo.category
// constants to Linkari profiles. Lower confidence than packageProfileMap.
// Constants from android.content.pm.ApplicationInfo:
//
//	CATEGORY_GAME = 0, CATEGORY_AUDIO = 1, CATEGORY_VIDEO = 2,
//	CATEGORY_IMAGE = 3, CATEGORY_SOCIAL = 4, CATEGORY_NEWS = 5,
//	CATEGORY_MAPS = 6, CATEGORY_PRODUCTIVITY = 7, CATEGORY_ACCESSIBILITY = 8
var appCategoryProfileMap = map[int]string{
	1: "music", // CATEGORY_AUDIO
	// EPIC-081 M3: CATEGORY_IMAGE (3) removed — image shares are routed to
	// image_triage by scoreAsync's type-based routing (M2), not by app category.
	4: "life",   // CATEGORY_SOCIAL
	5: "eng",    // CATEGORY_NEWS
	6: "travel", // CATEGORY_MAPS
}

// EPIC-075 M4: mimeProfileMap maps specific MIME types to Linkari profiles.
// Only types with strong profile signal are included — generic types like
// "application/pdf" or "image/jpeg" are omitted to avoid false positives.
var mimeProfileMap = map[string]string{
	"application/vnd.ms-excel":                                         "finance",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": "finance",
	"text/x-vcard": "life",
	"text/vcard":   "life",
}

// mapsRestaurantRE detects dining-context signals in subject/text for shares
// originating from Google Maps. When matched, overrides the default travel
// profile assigned to com.google.android.apps.maps.
var mapsRestaurantRE = regexp.MustCompile(`(?i)\b(?:restaurant|cafe|café|bistro|diner|eatery|bar|pub|grill|sushi|pizza|burger|menu|cuisine|food|dining|brunch|lunch|dinner|breakfast)\b`)

// compositeProfileOverride returns a non-empty profile override when a
// package+subject/text combination carries a stronger signal than the
// package alone provides. Currently handles:
//   - com.google.android.apps.maps + restaurant keyword → dining
//
// Returns empty string when no override applies.
func compositeProfileOverride(pkg, subject, text string) string {
	if pkg == "com.google.android.apps.maps" {
		combined := subject + " " + text
		if mapsRestaurantRE.MatchString(combined) {
			return "dining"
		}
	}
	return ""
}

// classifyByIntentMetadata returns a profile derived from Android intent
// metadata (package name, app category, MIME type). Returns empty string if
// no mapping matches, signaling that URL/content classification should proceed.
func classifyByIntentMetadata(req *ShareRequest) string {
	// Highest confidence: exact package name match, with composite override.
	if req.CallingPackage != "" {
		if override := compositeProfileOverride(req.CallingPackage, req.ExtraSubject, req.ExtraText); override != "" {
			return override
		}
		if p, ok := packageProfileMap[req.CallingPackage]; ok {
			return p
		}
	}
	// MIME type classification (EPIC-075 M4).
	if req.MimeType != "" {
		if p, ok := mimeProfileMap[req.MimeType]; ok {
			return p
		}
	}
	// Medium confidence: Android app category.
	if req.AppCategory > 0 {
		if p, ok := appCategoryProfileMap[req.AppCategory]; ok {
			return p
		}
	}
	return ""
}

// EPIC-075 M2: filenameKeywords is an ordered slice of keyword→profile mappings.
// Finance keywords come first to take priority over travel in ambiguous cases
// (e.g., "ticket" could be event/travel, but finance signals are more specific).
// Short keywords (≤3 chars: "cv", "tax") use word-boundary matching via
// filenameKeywordREs to avoid false positives like "recover"→life or "taxi"→finance.
var filenameKeywords = []struct {
	keyword string
	profile string
}{
	{"invoice", "finance"},
	{"receipt", "finance"},
	{"statement", "finance"},
	{"tax", "finance"},
	{"payslip", "finance"},
	{"resume", "life"},
	{"cv", "life"},
	{"recipe", "dining"},
	{"menu", "dining"},
	{"itinerary", "travel"},
	{"boarding", "travel"},
	{"ticket", "travel"},
}

// filenameShortKeywords is the set of keywords (≤3 chars) that require
// token-boundary matching. For these keywords classifyByFilename splits the
// filename on [-_. ] separators and checks for an exact token match rather
// than a substring match, preventing "taxi"→"tax" and "recover"→"cv" false positives.
var filenameShortKeywords = func() map[string]struct{} {
	m := make(map[string]struct{})
	for _, entry := range filenameKeywords {
		if len(entry.keyword) <= 3 {
			m[entry.keyword] = struct{}{}
		}
	}
	return m
}()

// filenameSplitRE splits a filename (lower-cased, extension stripped) on the
// separator characters used by common file-naming conventions.
var filenameSplitRE = regexp.MustCompile(`[-_. ]+`)

// EPIC-038 M5 / EPIC-075 M5: relativePathPrefixes maps MediaStore RELATIVE_PATH
// prefixes to profiles or screenshot flags. Checked in order of specificity.
// isScreenshot=true entries set req.IsScreenshot without returning a profile.
var relativePathPrefixes = []struct {
	prefix      string
	profile     string
	isScreenshot bool
}{
	// Stock Android / most OEMs
	{"DCIM/Screenshots", "", true},
	{"Screenshots", "", true},
	// Samsung (One UI): shares from the Gallery app use this path
	{"Pictures/Screenshots", "", true},
	// Xiaomi (MIUI/HyperOS): screenshot path differs from stock Android
	{"Screencap", "", true},
	{"Music/", "music", false},
	{"Recordings/", "music", false},
	{"Download/", "", false},  // too generic for classification
	{"Documents/", "", false}, // too generic
}

// classifyByFilename returns a profile derived from filename keyword matching.
// Iterates filenameKeywords in declared order (finance before travel) for
// deterministic results. Short keywords (≤3 chars) use token-boundary matching:
// the filename is split on [-_. ] separators and the keyword must be an exact
// token, preventing "taxi.jpg"→"tax"→finance and "recover.pdf"→"cv"→life
// false positives.
func classifyByFilename(filename string) string {
	lower := strings.ToLower(filename)
	for _, entry := range filenameKeywords {
		if _, isShort := filenameShortKeywords[entry.keyword]; isShort {
			// Token-boundary match: split on separators, check for exact token.
			tokens := filenameSplitRE.Split(lower, -1)
			for _, tok := range tokens {
				if tok == entry.keyword {
					return entry.profile
				}
			}
		} else {
			if strings.Contains(lower, entry.keyword) {
				return entry.profile
			}
		}
	}
	return ""
}

// EPIC-075 M3: subjectKeywords maps lowercase subject substrings to profiles.
// Ordered by specificity — more specific terms first within each profile group.
var subjectKeywords = []struct {
	keyword string
	profile string
}{
	// Finance
	{"portfolio", "finance"},
	{"stock", "finance"},
	{"invest", "finance"},
	{"invoice", "finance"},
	// Dining
	{"recipe", "dining"},
	{"restaurant", "dining"},
	{"menu", "dining"},
	// Travel
	{"flight", "travel"},
	{"hotel", "travel"},
	{"itinerary", "travel"},
	// Music
	{"album", "music"},
	{"playlist", "music"},
	{"track", "music"},
}

// classifyBySubjectKeywords returns a profile derived from keyword matching on
// the share's ExtraSubject field. Uses substring matching (subjects are natural
// language sentences, not structured filenames, so token splitting is not needed).
// Returns empty string if no keyword matches.
func classifyBySubjectKeywords(subject string) string {
	lower := strings.ToLower(subject)
	for _, entry := range subjectKeywords {
		if strings.Contains(lower, entry.keyword) {
			return entry.profile
		}
	}
	return ""
}

// classifyByRelativePath returns a profile and isScreenshot flag based on
// the MediaStore RELATIVE_PATH prefix. Screenshot entries (isScreenshot=true)
// return an empty profile — the caller sets req.IsScreenshot instead.
func classifyByRelativePath(relPath string) (profile string, isScreenshot bool) {
	for _, entry := range relativePathPrefixes {
		if strings.HasPrefix(relPath, entry.prefix) {
			return entry.profile, entry.isScreenshot
		}
	}
	return "", false
}

// screenshotFilenameRE matches filenames that identify a screenshot regardless
// of directory path. Used as a fallback when RelativePath is empty (e.g.
// non-MediaStore URIs from Samsung Gallery). EPIC-078 M3.
var screenshotFilenameRE = regexp.MustCompile(`(?i)^Screenshot[_\s\-]`)

// detectScreenshot sets req.IsScreenshot=true when the RelativePath indicates
// a screenshot origin. Runs unconditionally before profile classification —
// screenshot detection is an orthogonal concern that must not be skipped even
// when a profile is already set. EPIC-077 M4.
//
// EPIC-078 M3: when RelativePath is empty, falls back to matching req.Filename
// against screenshotFilenameRE so that non-MediaStore URIs (e.g. Samsung
// Gallery) are still detected via the filename sent by the Android client.
func detectScreenshot(req *ShareRequest) {
	if req.RelativePath != "" {
		_, isScreenshot := classifyByRelativePath(req.RelativePath)
		if isScreenshot {
			req.IsScreenshot = true
		}
		return
	}
	// RelativePath is empty — fall back to filename pattern.
	if req.Filename != "" && screenshotFilenameRE.MatchString(req.Filename) {
		req.IsScreenshot = true
	}
}

// classifyShareRequest is the single entry point for the fast classification
// cascade. It replaces the duplicate inline cascade in scoreURLAsync and the
// classifyIntentProfile helper used by scoreFileAsync. EPIC-077 M4.
//
// Cascade order:
//  1. intent_metadata (package name, MIME type, app category) — highest confidence
//  2. filename keywords
//  3. subject keywords
//  4. relativePath prefix (profile signal only; screenshot detection is separate)
//  5. URL domain heuristic — only when req.URL is non-empty
//  6. Haiku content LLM — only when contentClassify=true (URL domain fell through
//     to "eng" fallback) or when all metadata stages missed and hints are available
//
// Returns (profile, source) where source names the winning cascade stage.
// Returns ("", "") when no classification was possible.
//
// The contentClassify flag is set when URL domain matching returns the "eng"
// fallback rather than a positive match — the caller may then run Haiku
// classification on fetched page content to refine the profile.
func classifyShareRequest(ctx context.Context, req *ShareRequest) (profile, source string) {
	// Stage 1: intent metadata (package name, MIME type, app category).
	if p := classifyByIntentMetadata(req); p != "" {
		return p, "intent_metadata"
	}

	// Stage 2: filename keywords.
	if req.Filename != "" {
		if p := classifyByFilename(req.Filename); p != "" {
			return p, "filename"
		}
	}

	// Stage 3: subject keywords.
	if req.ExtraSubject != "" {
		if p := classifyBySubjectKeywords(req.ExtraSubject); p != "" {
			return p, "subject_keywords"
		}
	}

	// Stage 4: relativePath prefix (profile signal only — screenshot detection
	// is handled by detectScreenshot, which runs unconditionally before this).
	if req.RelativePath != "" {
		if p, _ := classifyByRelativePath(req.RelativePath); p != "" {
			return p, "relative_path"
		}
	}

	// Stage 5: URL domain heuristic (URL shares only).
	if req.URL != "" {
		classified, matched := classifyURLProfile(req.URL)
		if matched {
			return classified, "url_domain"
		}
		// Domain fell through to "eng" fallback — signal caller to run content LLM.
		// Return a sentinel so scoreURLAsync can trigger content classification.
		return classified, "url_domain_fallback"
	}

	// Stage 6: Haiku LLM fallback with metadata hints (non-URL shares only).
	// Builds a context snippet from all available metadata and calls Haiku.
	var hints []string
	if req.ExtraSubject != "" {
		hints = append(hints, "Subject: "+req.ExtraSubject)
	}
	if req.ExtraText != "" {
		hints = append(hints, "Text: "+truncateRunes(req.ExtraText, 500))
	}
	if req.Filename != "" {
		hints = append(hints, "Filename: "+req.Filename)
	}
	if req.CallingPackage != "" {
		hints = append(hints, "Source app: "+req.CallingPackage)
	}
	if len(hints) == 0 {
		return "", ""
	}
	snippet := strings.Join(hints, "\n")
	if p := classifyContentProfile(ctx, snippet); p != "" {
		return p, "content_llm_hints"
	}
	return "", ""
}

// classifyShareRequestFast runs stages 1-5 of the classification cascade
// (intent_metadata → filename → subject_keywords → relative_path → url_domain)
// without any LLM calls. This is the synchronous pre-enqueue classification
// used in handleShare. Stage-6 (Haiku LLM) runs async in scoreAsync.
// EPIC-079 M2.
func classifyShareRequestFast(req *ShareRequest) (profile, source string) {
	// Stage 1: intent metadata.
	if p := classifyByIntentMetadata(req); p != "" {
		return p, "intent_metadata"
	}
	// Stage 2: filename keywords.
	if req.Filename != "" {
		if p := classifyByFilename(req.Filename); p != "" {
			return p, "filename"
		}
	}
	// Stage 3: subject keywords.
	if req.ExtraSubject != "" {
		if p := classifyBySubjectKeywords(req.ExtraSubject); p != "" {
			return p, "subject_keywords"
		}
	}
	// Stage 4: relativePath prefix.
	if req.RelativePath != "" {
		if p, _ := classifyByRelativePath(req.RelativePath); p != "" {
			return p, "relative_path"
		}
	}
	// Stage 5: URL domain heuristic.
	if req.URL != "" {
		classified, matched := classifyURLProfile(req.URL)
		if matched {
			return classified, "url_domain"
		}
		return classified, "url_domain_fallback"
	}
	return "", ""
}

// classifyIntentProfile uses intent metadata to classify a non-URL share into
// the best-matching profile. Delegates to classifyShareRequest for the unified
// cascade. Retained for backward-compat with scoreAudioAsync callers.
// EPIC-077 M4: callers should prefer classifyShareRequest directly.
//
// Deprecated: use classifyShareRequest instead.
func classifyIntentProfile(ctx context.Context, req *ShareRequest) string {
	p, _ := classifyShareRequest(ctx, req)
	return p
}

// contentClassifyPrompt is the system prompt for the lightweight Haiku
// classification call. Designed for minimal token usage.
const contentClassifyPrompt = "Given shared content metadata or web page text, respond with exactly one profile name from: eng, travel, life, dining, fashion, finance, music. No explanation."

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

// processVoiceNoteAsync runs the voice note transcription and synopsis pipeline.
// Must be launched as a goroutine from handleTemplate. Owns a 1800s context
// independent of the HTTP request to accommodate 200MB uploads.
//
// EPIC-077 M5: renamed from scoreAudioAsync. Architecturally incompatible with
// scoreAsync — uses hardcoded score=100, calls execHaiku directly (not via
// eval.Evaluate), has a 1800s timeout (vs 120s), and manages transcript files.
// This pipeline is intentionally excluded from the URL/file unification.
//
// EPIC-071 M2: replaced eval.Evaluate (JSON scorecard) with execHaiku
// (plain text synopsis) using the vnote_synopsis prompt template. Audio
// shares no longer produce a numeric score — they produce a 1-2 sentence
// synopsis pushed via FCM with content_type=voice_note.
//
// Pipeline: ffmpeg m4a→wav → [segment if large] → whisper transcribe →
// backfill queue text → save transcript file → loadProfileTemplate("vnote_synopsis") →
// execHaiku (plain text) → UpdateScore → EnqueueDigestIfDue (FCM push).
func processVoiceNoteAsync(audioPath string, profile string, q *Queue, rowID int64, originalFilename string, whisperModel string, extraText string, req *ShareRequest, events *EventLogger) {
	ctx, cancel := context.WithTimeout(context.Background(), 1800*time.Second)
	defer cancel()
	defer os.Remove(audioPath) // temp m4a cleanup

	var mimeType, callingPackage string
	if req != nil {
		mimeType = req.MimeType
		callingPackage = req.CallingPackage
	}
	slog.Info("score_audio: start",
		"event_type", "score_audio_start",
		"row_id", rowID,
		"audio_path", audioPath,
		"original_filename", originalFilename,
		"profile", profile,
		"mime_type", mimeType,
		"calling_package", callingPackage,
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
		if errors.Is(err, ErrContainerOOM) {
			audioInfo, _ := os.Stat(audioPath)
			var actualMB float64
			if audioInfo != nil {
				actualMB = float64(audioInfo.Size()) / (1 << 20)
			}
			slog.Warn("score_audio: OOM killed during ffmpeg",
				"event_type", "score_audio_oom",
				"row_id", rowID,
				"actual_file_size_mb", actualMB,
			)
		} else {
			slog.Warn("score_audio: ffmpeg failed",
				"event_type", "score_audio_ffmpeg_error",
				"row_id", rowID,
				"error", err,
			)
		}
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
				if errors.Is(err, ErrContainerOOM) {
					wavChunkInfo, _ := os.Stat(chunk)
					var actualMB float64
					if wavChunkInfo != nil {
						actualMB = float64(wavChunkInfo.Size()) / (1 << 20)
					}
					slog.Warn("score_audio: OOM killed during whisper chunk",
						"event_type", "score_audio_oom",
						"row_id", rowID,
						"chunk", i+1,
						"actual_file_size_mb", actualMB,
					)
				} else {
					slog.Warn("score_audio: whisper chunk failed",
						"event_type", "score_audio_whisper_error",
						"row_id", rowID,
						"chunk", i+1,
						"error", err,
					)
				}
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
			if errors.Is(err, ErrContainerOOM) {
				var actualMB float64
				if wavInfo != nil {
					actualMB = float64(wavInfo.Size()) / (1 << 20)
				}
				slog.Warn("score_audio: OOM killed during whisper",
					"event_type", "score_audio_oom",
					"row_id", rowID,
					"actual_file_size_mb", actualMB,
				)
			} else {
				slog.Warn("score_audio: whisper failed",
					"event_type", "score_audio_whisper_error",
					"row_id", rowID,
					"error", err,
				)
			}
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
	// EPIC-038 GAP-04: use classifyIntentProfile as LLM fallback when profile
	// is still empty after heuristic cascade. Falls back to extraText-only
	// classification when the full request is unavailable.
	//
	// EPIC-077 M6: track the cascade stage that won for classify_stage_win telemetry.
	audioClassifySource := "caller"
	if profile == "" && req != nil {
		classified := classifyIntentProfile(ctx, req)
		if classified != "" {
			profile = classified
			audioClassifySource = "intent_metadata"
			slog.Info("score_audio: classified via intent profile",
				"event_type", "score_audio_intent_classify",
				"row_id", rowID,
				"classified_profile", classified,
			)
		}
	} else if profile == "" && extraText != "" {
		ctx2, cancel2 := context.WithTimeout(ctx, 15*time.Second)
		classified := classifyContentProfile(ctx2, extraText)
		cancel2()
		if classified != "" {
			profile = classified
			audioClassifySource = "content_lm"
			slog.Info("score_audio: classified from extraText",
				"event_type", "score_audio_extratext_classify",
				"row_id", rowID,
				"classified_profile", classified,
			)
		}
	}
	if profile == "" {
		// EPIC-081 M4: "life" default for voice notes — audio content is more
		// often personal/lifestyle than engineering. Replaces EPIC-077 M6's "eng".
		profile = "life"
		audioClassifySource = "default_fallback"
	}

	// Emit classify_stage_win for the audio pipeline (EPIC-077 M6).
	// jq recipe for accuracy baseline:
	//   jq 'select(.event_type=="classify_stage_win" and .content_type=="audio") | {profile, classify_source, row_id}' linkari_events.jsonl
	if events != nil {
		_ = events.Emit("classify_stage_win", map[string]interface{}{
			"row_id":          rowID,
			"profile":         profile,
			"classify_source": audioClassifySource,
			"content_type":    "audio",
		})
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

	// Step 5: rubric scoring via eval.Evaluate() (EPIC-081 M4).
	// Loads vnote_triage profile (or classified profile) with audio mode
	// and produces a real rubric-scored verdict instead of hardcoded 100.
	audioScore := 0
	audioVerdict := ""
	var audioTopicTags string
	if q != nil {
		q.SetProgress(rowID, "scoring")
	}
	_, scoreSysPrompt, scoreErr := loadProfileTemplateForMode(profile, "audio")
	if scoreErr != nil {
		slog.Warn("score_audio: load scoring template failed, using vnote_triage fallback",
			"event_type", "score_audio_scoring_template_error",
			"row_id", rowID,
			"profile", profile,
			"error", scoreErr,
		)
		// Fallback: try vnote_triage directly.
		_, scoreSysPrompt, scoreErr = loadProfileTemplateForMode("vnote_triage", "audio")
	}
	if scoreErr == nil {
		eval := HaikuJSONEvaluator{}
		sc, evalErr := eval.Evaluate(ctx, transcript, scoreSysPrompt)
		if evalErr != nil {
			slog.Warn("score_audio: rubric evaluation failed, falling back to score=0",
				"event_type", "score_audio_eval_error",
				"row_id", rowID,
				"error", evalErr,
			)
		} else {
			audioScore = sc.Score
			audioVerdict = sc.Verdict
			audioTopicTags = sc.Tags
			slog.Info("score_audio: rubric scored",
				"event_type", "score_audio_rubric_scored",
				"row_id", rowID,
				"score", sc.Score,
				"verdict", sc.Verdict,
				"profile", profile,
			)
		}
	}

	// Step 6: synopsis generation via Haiku (decoupled from score).
	// EPIC-071 M2: uses execHaiku directly for plain text output.
	// Synopsis is used for FCM notification body, not scoring.
	_, synopsisSysPrompt, err := loadProfileTemplate("vnote_synopsis")
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
	synopsisSysPrompt = strings.Replace(synopsisSysPrompt, "{{transcript}}", transcript, 1)

	if q != nil {
		q.SetProgress(rowID, "summarizing")
	}

	synopsis, err := execHaiku(ctx, synopsisSysPrompt, transcript)
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

	// Step 7: persist — use rubric score from Step 5 (EPIC-081 M4).
	if q == nil {
		return
	}
	slug := fmt.Sprintf("vnote-%d", rowID)
	_ = audioTopicTags // topic tags available for future use
	if err := q.UpdateScore(rowID, audioScore, audioVerdict, synopsis, slug, "", ""); err != nil {
		slog.Warn("score_audio: UpdateScore failed",
			"event_type", "score_audio_queue_error",
			"row_id", rowID,
			"error", err,
		)
		return
	}

	// Step 8: auto-archive voice notes (always — no threshold gating).
	q.Archive(rowID)

	// Step 9: FCM push via the dual-writer invariant (EPIC-051).
	// content_type="voice_note" tells the Android client to render synopsis.
	// EPIC-081 M4: uses rubric score instead of hardcoded 100.
	// EPIC-077 M6: classify_source included in push payload for provenance.
	resolvePushConfigOnce(q)
	_, _ = q.EnqueueDigestIfDue(context.Background(),
		profile, audioScore, slug, synopsis, "", "", "voice_note", audioClassifySource)

	slog.Info("score_audio: complete",
		"event_type", "score_audio_complete",
		"row_id", rowID,
		"profile", profile,
		"score", audioScore,
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
