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
func scoreURLAsync(req *ShareRequest, q *Queue, eval Evaluator, events *EventLogger) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	rawURL := req.URL
	profile := req.Profile

	// EPIC-038 M4: try intent metadata classification first (highest signal).
	autoClassified := false
	contentClassify := false
	classifySource := "" // tracks which signal classified the profile
	if profile == "" {
		if metaProfile := classifyByIntentMetadata(req); metaProfile != "" {
			profile = metaProfile
			autoClassified = true
			classifySource = "intent_metadata"
			slog.Info("server_score: intent-metadata classified",
				"event_type", "server_score_metadata_classify",
				"url", rawURL,
				"calling_package", req.CallingPackage,
				"app_category", req.AppCategory,
				"classified_profile", metaProfile,
			)
		}
	}

	// EPIC-038 M5: filename keyword heuristics.
	if profile == "" && req.Filename != "" {
		if p := classifyByFilename(req.Filename); p != "" {
			profile = p
			autoClassified = true
			classifySource = "filename"
		}
	}

	// EPIC-075 M3: subject keyword heuristics (higher signal than relativePath).
	if profile == "" && req.ExtraSubject != "" {
		if p := classifyBySubjectKeywords(req.ExtraSubject); p != "" {
			profile = p
			autoClassified = true
			classifySource = "subject_keywords"
		}
	}

	// EPIC-038 M5 / EPIC-075 M5: relativePath prefix matching.
	// This block runs unconditionally (even when profile is already set) because
	// screenshot detection must always fire — a screenshot from a finance app is
	// still a screenshot. Profile assignment is gated on profile=="".
	if req.RelativePath != "" {
		if p, isSS := classifyByRelativePath(req.RelativePath); isSS {
			req.IsScreenshot = true
		} else if p != "" && profile == "" {
			profile = p
			autoClassified = true
			classifySource = "relative_path"
		}
	}

	// EPIC-061 M3: fall back to URL-based classification when metadata didn't match.
	if profile == "" {
		classified, matched := classifyURLProfile(rawURL)
		profile = classified
		autoClassified = true
		if matched {
			classifySource = "url_domain"
		} else {
			contentClassify = true
		}
	}

	// EPIC-076 M1: emit classify_stage_win after cascade resolves.
	// jq recipe: jq 'select(.event_type=="classify_stage_win") | .metadata.classify_source' linkari_events.jsonl | sort | uniq -c
	if events != nil {
		_ = events.Emit("classify_stage_win", map[string]interface{}{
			"url":              rawURL,
			"profile":          profile,
			"classify_source":  classifySource,
			"auto_classified":  autoClassified,
			"row_id":           req.QueueRowID,
		})
	}

	slog.Info("server_score: start",
		"event_type", "server_score_start",
		"url", rawURL,
		"profile", profile,
		"auto_classified", autoClassified,
		"classify_source", classifySource,
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

	// EPIC-038 M4: screenshots skip Jina fetch — use extraText/extraSubject
	// as classification input instead. No URL content to fetch for screenshots.
	var content string
	if req.IsScreenshot {
		screenshotContent := strings.TrimSpace(req.ExtraSubject + "\n" + req.ExtraText)
		if screenshotContent == "" {
			slog.Info("server_score: screenshot with no text metadata",
				"event_type", "server_score_skip",
				"url", rawURL,
				"profile", profile,
				"reason", "screenshot_no_text",
			)
			return
		}
		if profile == "" || contentClassify {
			classified := classifyContentProfile(ctx, screenshotContent)
			if classified != "" {
				profile = classified
				autoClassified = true
				contentClassify = false
			}
		}
		slog.Info("server_score: screenshot classification",
			"event_type", "server_score_screenshot",
			"url", rawURL,
			"profile", profile,
			"text_len", len(screenshotContent),
		)
		content = screenshotContent
	} else {
		// Fetch page content. 30s sub-context leaves headroom within the 60s budget.
		fetchCtx, fetchCancel := context.WithTimeout(ctx, 30*time.Second)
		defer fetchCancel()
		var err error
		content, err = fetchJinaContent(fetchCtx, rawURL)
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
	}

	// Content-based classification: when domain matching fell through to the
	// "eng" fallback, ask Haiku to classify the fetched content into the best
	// profile. This is cheap (~100 tokens) and avoids scoring travel/life/dining
	// content under the eng rubric.
	// EPIC-038 M4: prepend extraSubject as supplementary classification signal.
	if contentClassify {
		classifyInput := content
		if req.ExtraSubject != "" {
			classifyInput = "[Shared with subject: " + req.ExtraSubject + "]\n\n" + content
		}
		classified := classifyContentProfile(ctx, classifyInput)
		if classified != "" {
			slog.Info("server_score: content-classified profile",
				"event_type", "server_score_content_classify",
				"url", rawURL,
				"domain_fallback", profile,
				"content_classified", classified,
			)
			profile = classified
			classifySource = "content"
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
		sysPrompt = classificationPreamble(profile, rawURL, classifySource) + sysPrompt
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
		"classify_source", classifySource,
		"auto_classified", autoClassified,
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
		"classify_source", classifySource,
	)
}

// scoreFileAsync scores image/document shares server-side without Jina fetch.
// EPIC-038 GAP-05: classifies via classifyIntentProfile, synthesizes content
// from metadata, and runs the standard template-load + eval + persist pipeline.
func scoreFileAsync(req *ShareRequest, q *Queue, eval Evaluator, events *EventLogger) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	profile := req.Profile

	// Classify via intent metadata cascade + LLM fallback.
	autoClassified := false
	classifySource := ""
	if profile == "" {
		classified := classifyIntentProfile(ctx, req)
		if classified != "" {
			profile = classified
			autoClassified = true
			classifySource = "intent_profile"
		}
	}
	if profile == "" {
		profile = "eng"
		autoClassified = true
		classifySource = "default_fallback"
	}

	// EPIC-076 M1: emit classify_stage_win after cascade resolves.
	if events != nil {
		_ = events.Emit("classify_stage_win", map[string]interface{}{
			"url":             "",
			"profile":         profile,
			"classify_source": classifySource,
			"auto_classified": autoClassified,
			"row_id":          req.QueueRowID,
		})
	}

	// Synthesize content from available metadata (no URL to fetch).
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
	content := strings.Join(parts, "\n")
	if strings.TrimSpace(content) == "" {
		slog.Info("score_file: no metadata for scoring",
			"event_type", "score_file_skip",
			"profile", profile,
			"type", req.Type,
			"reason", "no_metadata",
		)
		return
	}

	slog.Info("score_file: start",
		"event_type", "score_file_start",
		"profile", profile,
		"type", req.Type,
		"content_len", len(content),
		"classify_source", classifySource,
	)

	// Load profile template and evaluate.
	_, sysPrompt, err := loadProfileTemplate(profile)
	if err != nil {
		slog.Warn("score_file: load template failed",
			"event_type", "score_file_template_error",
			"profile", profile,
			"error", err,
		)
		return
	}
	if autoClassified {
		sysPrompt = classificationPreamble(profile, "", classifySource) + sysPrompt
	}

	sc, err := eval.Evaluate(ctx, content, sysPrompt)
	if err != nil {
		slog.Warn("score_file: evaluate failed",
			"event_type", "score_file_eval_error",
			"profile", profile,
			"error", err,
		)
		return
	}
	sc.Profile = profile
	sc.SourceType = "server-score"

	slog.Info("score_file: evaluated",
		"event_type", "score_file_evaluated",
		"profile", profile,
		"score", sc.Score,
		"verdict", sc.Verdict,
		"classify_source", classifySource,
	)

	if q == nil {
		return
	}

	// Persist via queue row ID (file shares have no URL to match on).
	slug := fmt.Sprintf("file-%d", req.QueueRowID)
	if err := q.UpdateScore(req.QueueRowID, sc.Score, sc.Tags, sc.Verdict, slug); err != nil {
		slog.Warn("score_file: UpdateScore failed",
			"event_type", "score_file_queue_error",
			"row_id", req.QueueRowID,
			"error", err,
		)
		return
	}

	// Auto-archive when score meets threshold.
	threshold := archiveThreshold(profile)
	if threshold >= 0 && sc.Score >= threshold {
		q.Archive(req.QueueRowID)
	}

	// FCM push via dual-writer invariant (EPIC-051).
	resolvePushConfigOnce(q)
	_, _ = q.EnqueueDigestIfDue(context.Background(),
		profile, sc.Score, slug, sc.Verdict, "", "")

	slog.Info("score_file: complete",
		"event_type", "score_file_complete",
		"row_id", req.QueueRowID,
		"profile", profile,
		"score", sc.Score,
		"classify_source", classifySource,
	)
}

// validProfiles is the set of profiles that classifyContentProfile accepts.
var validProfiles = map[string]bool{
	"eng": true, "travel": true, "life": true,
	"dining": true, "fashion": true, "finance": true, "music": true,
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
	1: "music",  // CATEGORY_AUDIO
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

// classifyIntentProfile uses intent metadata to classify a non-URL share into
// the best-matching profile. Cascades through: package → app category →
// filename keywords → relativePath → Haiku with metadata context.
// Returns empty string if no classification could be made.
func classifyIntentProfile(ctx context.Context, req *ShareRequest) string {
	// 1. Package name (highest confidence).
	if p := classifyByIntentMetadata(req); p != "" {
		return p
	}
	// 2. Filename keywords.
	if req.Filename != "" {
		if p := classifyByFilename(req.Filename); p != "" {
			return p
		}
	}
	// 3. Subject keywords (EPIC-075 M3).
	if req.ExtraSubject != "" {
		if p := classifyBySubjectKeywords(req.ExtraSubject); p != "" {
			return p
		}
	}
	// 4. RelativePath prefix.
	if req.RelativePath != "" {
		if p, _ := classifyByRelativePath(req.RelativePath); p != "" {
			return p
		}
	}
	// 5. Haiku classification with metadata context (last resort).
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
		return ""
	}
	snippet := strings.Join(hints, "\n")
	classified := classifyContentProfile(ctx, snippet)
	return classified
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
func scoreAudioAsync(audioPath string, profile string, q *Queue, rowID int64, originalFilename string, whisperModel string, extraText string, req *ShareRequest) {
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
	if profile == "" && req != nil {
		classified := classifyIntentProfile(ctx, req)
		if classified != "" {
			profile = classified
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
			slog.Info("score_audio: classified from extraText",
				"event_type", "score_audio_extratext_classify",
				"row_id", rowID,
				"classified_profile", classified,
			)
		}
	}
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
