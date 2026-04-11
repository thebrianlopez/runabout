package main

// EPIC-060 M1: server-side scoring pipeline for uinit_* actions.
//
// Replaces the tmux → fish uinit → linkari triage chain with a single
// goroutine: fetch → truncate → eval → ScoreByURL → archive → FCM push.
// Activated when ActionConfig.ServerScore is true (set on all builtinConfig
// uinit_* actions by this milestone).

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	item, _, err := q.ScoreByURL(rawURL, sc.Score, sc.Verdict, sc.Tags, profile, slug)
	if err != nil {
		slog.Warn("server_score: ScoreByURL failed",
			"event_type", "server_score_queue_error",
			"url", rawURL,
			"profile", profile,
			"error", err,
		)
		return
	}

	// Auto-archive when score meets profile threshold (mirrors cmd_score path).
	threshold := archiveThreshold(profile)
	if threshold >= 0 && item.Score != nil && *item.Score >= threshold {
		if archErr := q.Archive(item.ID); archErr == nil {
			item.Status = "archived"
		}
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
