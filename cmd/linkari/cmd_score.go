// Package main — EPIC-053: `linkari score` CLI subcommand for prompt iteration.
//
// This file wires two Cobra subcommands:
//
//   - `score` — run the production scoring pipeline (Haiku via `claude`) against
//     a single URL and persist through the same Queue.ScoreByURL +
//     EnqueueDigestIfDue path used by `handleQueueScore`. This is the dedicated
//     prompt-iteration entrypoint: it bypasses /share, uinit, the score cache,
//     and (optionally) push digest, but never mocks the scoring pipeline.
//     Flags:
//       --profile <p>       required — scoring profile
//       --content-file <f>  content to feed the model (default stdin, '-' also means stdin)
//       --prompt-file <f>   override the profile's default system prompt for this run only
//       --no-push           skip EnqueueDigestIfDue (queue row still written)
//       --dry-run           skip queue write AND push; print verdict to stdout (implies --no-push)
//       --queue-db <path>   override queue.db path
//
//   - `score-write` (hidden legacy) — preserves the pre-EPIC-053 behavior of
//     writing a pre-computed score directly via ScoreByURL. Retained because
//     existing scripts and the EPIC-050 auto-archive regression test still
//     exercise that path.
//
// Iteration workflow:
//
//	# 1. Bank the current verdict
//	linkari score https://foo.com/post --profile eng --content-file ./post.md
//
//	# 2. Try a tweaked prompt without touching FCM
//	linkari score https://foo.com/post --profile eng \
//	    --prompt-file ./prompts/eng.v2.md --no-push --content-file ./post.md
//
//	# 3. Dry-run to compare verdicts without DB churn
//	linkari score https://foo.com/post --profile eng \
//	    --prompt-file ./prompts/eng.v3.md --dry-run --content-file ./post.md
//
// See: ~/code/personal/docs/epics/PERSONAL_20260409T122541Z_Linkari_EPIC-053_linkari_score_CLI_subcommand_for_prompt_iteration.md
// For a batch regression suite across a fixture corpus, see EPIC-054 (planned).

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// scoreCmd wires `linkari score <url> --profile <p>` — the dedicated
// prompt-iteration entrypoint. Reuses Queue.ScoreByURL + EnqueueDigestIfDue
// verbatim so there is no shadow persistence path (EPIC-051 dual-writer
// invariant preserved).
func scoreCmd() *cobra.Command {
	var (
		queueDB     string
		profile     string
		contentFile string
		promptFile  string
		noPush      bool
		dryRun      bool
	)

	cmd := &cobra.Command{
		Use:   "score <url>",
		Short: "Run the scoring pipeline against a URL (prompt iteration, EPIC-053)",
		Long: `Run the production Haiku scoring pipeline against a single URL and
persist via the same Queue.ScoreByURL + EnqueueDigestIfDue path used by
handleQueueScore. Dedicated path for prompt iteration — bypasses /share,
uinit, and the score cache without mocking the scoring pipeline.

Examples:
  # Default: score + queue + digest push
  linkari score https://foo.com/post --profile eng < post.md

  # Iterate on a prompt without pushing to FCM
  linkari score https://foo.com/post --profile eng \
      --prompt-file ./prompts/eng.v2.md --no-push --content-file post.md

  # Dry-run: print verdict only, no DB writes
  linkari score https://foo.com/post --profile eng \
      --prompt-file ./prompts/eng.v3.md --dry-run --content-file post.md

For batch evaluation across a fixture set, see EPIC-054 (planned).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			url := args[0]
			if profile == "" {
				return fmt.Errorf("--profile is required")
			}
			// --dry-run implies --no-push semantically.
			if dryRun {
				noPush = true
			}

			// Resolve system prompt: --prompt-file overrides profile template
			// for this invocation only. No global state mutation — we pass
			// the rendered prompt directly to execHaiku.
			sysPrompt, promptSource, err := resolveScorePrompt(profile, promptFile)
			if err != nil {
				return err
			}

			// Read content from --content-file or stdin.
			content, err := readScoreContent(contentFile, cmd.InOrStdin())
			if err != nil {
				return err
			}
			content = truncateRunes(content, contentTruncationRunes)
			if strings.TrimSpace(content) == "" {
				return fmt.Errorf("empty content from %s", contentSourceLabel(contentFile))
			}

			// Call Haiku via Evaluator interface (EPIC-058 M2).
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			eval := HaikuMarkdownEvaluator{}
			sc, err := eval.Evaluate(ctx, content, sysPrompt)
			if err != nil {
				return err
			}
			sc.SourceType = "cli-score"
			sc.PromptVersion = promptVersionFromPath(promptSourcePath(promptSource))

			// EPIC-058 M3: confidence gate.
			if actionCfg := lookupGinitAction(profile); actionCfg != nil && CheckGate(sc, *actionCfg) {
				if dryRun {
					fmt.Fprintf(os.Stderr, "score: gate passed (score=%d >= %d) — dry-run, skipping\n",
						sc.Score, actionCfg.ConfidenceThreshold)
				} else {
					// EPIC-058 M4: auto-launch ginit when confidence gate passes.
					autoLaunchGinit(url, profile, sc.Score)
				}
			} else if actionCfg != nil && actionCfg.ConfidenceThreshold > 0 {
				slog.Info("confidence gate: below threshold",
					"score", sc.Score, "threshold", actionCfg.ConfidenceThreshold, "gap_count", len(sc.Gaps))
			}

			// EPIC-058 M5: emit evaluator_scored event with prompt version tracking.
			emitPushEvent("evaluator_scored", map[string]interface{}{
				"url":               url,
				"profile":           profile,
				"score":             sc.Score,
				"gap_count":         len(sc.Gaps),
				"prompt_version":    sc.PromptVersion,
				"evaluator_backend": sc.Backend,
				"latency_ms":        sc.LatencyMs,
				"source_type":       sc.SourceType,
			})

			// Back-compat: populate TriageResult for the persist path.
			res := TriageResult{
				Score:       sc.Score,
				Verdict:     sc.Verdict,
				ActionItems: sc.Gaps,
				Tags:        sc.Tags,
				RawMarkdown: sc.RawMarkdown,
			}

			slug := deriveSlugFromURL(url)

			// Dry-run: print verdict, no persistence.
			if dryRun {
				out := map[string]interface{}{
					"url":           url,
					"profile":       profile,
					"score":         res.Score,
					"verdict":       res.Verdict,
					"slug":          slug,
					"prompt_source": promptSource,
					"dry_run":       true,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			// Persist via the sanctioned entry point (same as handleQueueScore).
			queueDB = resolveQueueDB(queueDB)
			q, err := NewQueue(queueDB, false)
			if err != nil {
				return fmt.Errorf("opening queue: %w", err)
			}
			defer q.Close()

			item, _, err := q.ScoreByURL(url, res.Score, res.Verdict, res.Tags, profile, slug)
			if err != nil {
				return fmt.Errorf("score: %w", err)
			}

			// Auto-archive + digest push on threshold crossing. Mirrors
			// handleQueueScore exactly so there is no drift.
			threshold := archiveThreshold(item.Profile)
			if threshold >= 0 && item.Score != nil && *item.Score >= threshold {
				if archErr := q.Archive(item.ID); archErr == nil {
					item.Status = "archived"
					if !noPush {
						// EPIC-051 M3: single sanctioned entry point.
						resolvePushConfigOnce(q)
						_, _ = q.EnqueueDigestIfDue(context.Background(),
							item.Profile, *item.Score, item.Slug, item.Verdict, item.URL,
							sc.GapSummary(3))
					}
				}
			}

			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(item)
		},
	}

	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().StringVar(&profile, "profile", "", "scoring profile (required)")
	cmd.Flags().StringVar(&contentFile, "content-file", "-", "content file to score ('-' for stdin)")
	cmd.Flags().StringVar(&promptFile, "prompt-file", "", "override profile's system prompt for this run only")
	cmd.Flags().BoolVar(&noPush, "no-push", false, "skip EnqueueDigestIfDue (queue row still written)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "skip queue write AND push; print verdict only (implies --no-push)")
	_ = cmd.MarkFlagRequired("profile")

	return cmd
}

// resolveScorePrompt returns the system prompt bytes for a score run.
// When promptFile is set, its contents replace the profile's default template
// for this invocation only — no global state is mutated. The second return is
// a human-readable label for telemetry/output.
func resolveScorePrompt(profile, promptFile string) (prompt, source string, err error) {
	if promptFile != "" {
		b, rerr := os.ReadFile(promptFile)
		if rerr != nil {
			return "", "", fmt.Errorf("--prompt-file: %w", rerr)
		}
		if len(strings.TrimSpace(string(b))) == 0 {
			return "", "", fmt.Errorf("--prompt-file %q is empty", promptFile)
		}
		return string(b), "file:" + promptFile, nil
	}
	path, rendered, lerr := loadProfileTemplate(profile)
	if lerr != nil {
		return "", "", lerr
	}
	return rendered, "profile:" + path, nil
}

// readScoreContent reads the content to score: from file, stdin (when path is
// empty or "-"), or returns an error. cmdIn lets tests inject a stubbed stdin.
func readScoreContent(path string, cmdIn io.Reader) (string, error) {
	if path == "" || path == "-" {
		if cmdIn == nil {
			cmdIn = os.Stdin
		}
		b, err := io.ReadAll(cmdIn)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(b), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read content: %w", err)
	}
	return string(b), nil
}

// promptSourcePath extracts the file path from a prompt source label like
// "profile:/path/to/eng.yaml" or "file:/path/to/custom.md". Returns empty
// string for unrecognized formats.
func promptSourcePath(source string) string {
	for _, prefix := range []string{"profile:", "file:"} {
		if strings.HasPrefix(source, prefix) {
			return strings.TrimPrefix(source, prefix)
		}
	}
	return ""
}

func contentSourceLabel(path string) string {
	if path == "" || path == "-" {
		return "stdin"
	}
	return path
}

// deriveSlugFromURL produces a stable, filesystem-safe slug from a URL for use
// as the queue row's workspace identifier. Matches the shape of share-ingress
// slugs (host + last path segment) without depending on the share pipeline.
func deriveSlugFromURL(url string) string {
	s := url
	for _, prefix := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimSuffix(s, "/")
	// Collapse to ASCII-safe: replace separators with '-', drop others.
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	// Trim leading/trailing dashes.
	out = strings.Trim(out, "-")
	if out == "" {
		out = "url"
	}
	if len(out) > 96 {
		out = out[:96]
	}
	return out
}

// scoreWriteCmd is the legacy pre-EPIC-053 `score` subcommand, exposed as
// `score-write` so existing scripts + the EPIC-050 auto-archive regression
// test keep working. Writes a pre-computed score directly via ScoreByURL.
// Hidden from --help; no new callers should be added.
func scoreWriteCmd() *cobra.Command {
	var (
		queueDB string
		url     string
		score   int
		verdict string
		profile string
		slug    string
		tags    string
	)

	cmd := &cobra.Command{
		Use:    "score-write",
		Short:  "Write a pre-computed score to the queue (legacy)",
		Hidden: true,
		Long: `Legacy: persist a pre-computed score and verdict for a URL in the
Linkari queue. EPIC-053 split this out of 'score' so the new subcommand can
own the prompt-iteration pipeline; this path remains for scripts that inject
scores directly.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if score < 0 || score > 100 {
				return fmt.Errorf("--score must be 0-100, got %d", score)
			}

			queueDB = resolveQueueDB(queueDB)
			q, err := NewQueue(queueDB, false)
			if err != nil {
				return fmt.Errorf("opening queue: %w", err)
			}
			defer q.Close()

			item, _, err := q.ScoreByURL(url, score, verdict, tags, profile, slug)
			if err != nil {
				return fmt.Errorf("score: %w", err)
			}

			threshold := archiveThreshold(item.Profile)
			if threshold >= 0 && item.Score != nil && *item.Score >= threshold {
				if archErr := q.Archive(item.ID); archErr == nil {
					item.Status = "archived"
					resolvePushConfigOnce(q)
					_, _ = q.EnqueueDigestIfDue(context.Background(),
						item.Profile, *item.Score, item.Slug, item.Verdict, item.URL)
				}
			}

			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(item)
		},
	}

	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().StringVar(&url, "url", "", "URL to score (required)")
	cmd.Flags().IntVar(&score, "score", 0, "score 0-100")
	cmd.Flags().StringVar(&verdict, "verdict", "", "verdict text")
	cmd.Flags().StringVar(&profile, "profile", "eng", "scoring profile")
	cmd.Flags().StringVar(&slug, "slug", "", "workspace slug")
	cmd.Flags().StringVar(&tags, "tags", "", "comma-separated tags")
	_ = cmd.MarkFlagRequired("url")
	_ = cmd.MarkFlagRequired("score")

	return cmd
}
