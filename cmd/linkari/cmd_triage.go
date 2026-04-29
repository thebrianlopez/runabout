package main

// EPIC-043 M2: `linkari triage` — Go port of _uinit_profile_prompt.fish.
//
// This subcommand owns the link-triage scoring loop. It loads a profile
// prompt template, assembles the system+user prompt the same way fish does,
// shells out to the `claude` CLI for a single-turn Haiku call, parses the
// returned markdown into a TriageResult, and persists the score via the
// same Queue.ScoreByURL path that the /notify HTTP handler uses.
//
// Why shell out to `claude` instead of using the Anthropic SDK directly:
// fish currently calls `claude --print --model claude-haiku-4-5-20251001
// --max-turns 1 --tools "" --system-prompt ...` so output bytes flow through
// Claude Code, not the raw API. Matching transport keeps M2 byte-for-byte
// equivalent to what M1's 42-fixture corpus captured, and avoids splitting
// billing between Claude Code subscription and API spend.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// TriageResult is the parsed output of one Haiku triage call.
type TriageResult struct {
	Score       int      `json:"score"`
	Verdict     string   `json:"verdict"`
	ActionItems []string `json:"action_items,omitempty"`
	Tags        string   `json:"tags,omitempty"`
	RawMarkdown string   `json:"raw_markdown"`
}

// claudeModel is the Haiku model fish currently calls. Pinned for parity with
// the M1 fixture corpus until M3+ revisits the model selection.
const claudeModel = "claude-haiku-4-5-20251001"

// contentTruncationRunes matches the fish 2000-char truncation in
// _uinit_profile_prompt.fish line 39 (`string sub -l 2000`). Fish counts
// runes, not bytes — match that semantic.
const contentTruncationRunes = 2000

// execHaiku is the indirection point that tests stub. Production path is
// runClaudeHaiku; tests can swap in a deterministic fake.
var execHaiku = runClaudeHaiku

// triageCmd wires `linkari triage` into the root command.
func triageCmd() *cobra.Command {
	var (
		queueDB     string
		url         string
		profile     string
		workspace   string
		contentFile string
		dryRun      bool
		noPersist   bool
		useMarkdown bool
	)

	cmd := &cobra.Command{
		Use:   "triage",
		Short: "Score link content via Haiku and persist to the queue (EPIC-043 M2)",
		Long: `Run profile-specific triage on workspace content via Haiku and
persist the score to the linkari queue.

Mirrors the fish function _uinit_profile_prompt: loads
docs/prompts/profiles/<profile>.md as the system prompt, pipes the workspace
content (truncated to 2000 chars) through Haiku via the claude CLI, parses
the resulting markdown, and writes the score to the queue + a _score.json
sidecar in the workspace.

Use --dry-run to inspect the assembled prompt without calling Haiku.
Use --no-persist to call Haiku and parse but skip the queue write (used by
the eval harness path).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if profile == "" {
				return fmt.Errorf("--profile required")
			}
			if workspace == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("resolve workspace: %w", err)
				}
				workspace = cwd
			}
			if contentFile == "" {
				contentFile = filepath.Join(workspace, "README.md")
			}

			// 1. Load profile template.
			tmplPath, sysPrompt, err := loadProfileTemplate(profile)
			if err != nil {
				return err
			}

			// 2. Read content (or stdin if "-").
			var content string
			if contentFile == "-" {
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				content = string(b)
			} else {
				b, err := os.ReadFile(contentFile)
				if err != nil {
					return fmt.Errorf("read content: %w", err)
				}
				content = string(b)
			}
			content = truncateRunes(content, contentTruncationRunes)
			if strings.TrimSpace(content) == "" {
				return fmt.Errorf("empty content from %s", contentFile)
			}

			// 3. Dry run? Dump exact bytes Haiku would see and exit.
			if dryRun {
				fmt.Fprintf(os.Stderr, "triage: dry-run profile=%s template=%s content=%dch\n", profile, tmplPath, len(content))
				fmt.Printf("===SYSTEM PROMPT===\n%s\n===USER INPUT===\n%s", sysPrompt, content)
				return nil
			}

			// 4. Call Haiku via Evaluator interface (EPIC-058 M1).
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			var eval Evaluator
			if useMarkdown {
				eval = HaikuMarkdownEvaluator{}
			} else {
				eval = HaikuJSONEvaluator{}
			}
			sc, err := eval.Evaluate(ctx, content, sysPrompt)
			if err != nil {
				return err
			}
			sc.Profile = profile
			sc.SourceType = "cli-triage"
			sc.PromptVersion = promptVersionFromPath(tmplPath)
			sc.PromptHash = promptHash(sysPrompt)

			// EPIC-058 M3: confidence gate — check if score meets threshold
			// for auto-launch. M4 wires the actual ginit launch.
			if actionCfg := lookupGinitAction(profile); actionCfg != nil && CheckGate(sc, *actionCfg) {
				if dryRun {
					fmt.Fprintf(os.Stderr, "triage: gate passed (score=%d >= %d) — dry-run, skipping\n",
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
			raw := sc.RawMarkdown
			res := TriageResult{
				Score:       sc.Score,
				Verdict:     sc.Verdict,
				ActionItems: sc.Gaps,
				Tags:        sc.Tags,
				RawMarkdown: raw,
			}

			if noPersist {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}

			// 6. Persist via queue + sidecar + README append.
			slug := filepath.Base(workspace)
			queueDB = resolveQueueDB(queueDB)
			q, err := NewQueue(queueDB, false)
			if err != nil {
				return fmt.Errorf("opening queue: %w", err)
			}
			defer q.Close()

			item, _, err := q.ScoreByURL(url, res.Score, res.Verdict, res.Tags, profile, slug, sc.PromptHash, sc.PromptVersion, sc.RubricScores)
			if err != nil {
				return fmt.Errorf("score: %w", err)
			}
			// EPIC-072 M5: persist topic tags from scoring.
			if len(sc.TopicTags) > 0 {
				_ = q.SetTopicTags(item.ID, sc.TopicTags)
			}
			// Auto-archive if score meets profile threshold (mirrors cmd_score.go).
			threshold := archiveThreshold(item.Profile)
			if threshold >= 0 && item.Score != nil && *item.Score >= threshold {
				if archErr := q.Archive(item.ID); archErr == nil {
					item.Status = "archived"
				}
			}

			// EPIC-059: triage path now produces push notifications (was missing
			// since EPIC-051). Dual-writer invariant preserved.
			if item.Score != nil {
				resolvePushConfigOnce(q)
				_, _ = q.EnqueueDigestIfDue(context.Background(),
					item.Profile, *item.Score, item.Slug, item.Verdict, item.URL,
					sc.GapSummary(3))
			}

			// 7. Write _score.json sidecar (fish line 138-145 shape).
			//    EPIC-044: additive-only; JSON path also writes profile_version
			//    + rubric_scores. Existing readers (cmd_eval.go captureFromWorkspace)
			//    decode by named field and ignore unknown keys.
			var extras *sidecarExtras
			if len(sc.RubricScores) > 0 || len(sc.TopicTags) > 0 {
				extras = &sidecarExtras{
					SchemaVersion:  "triage_verdict_v1",
					ProfileVersion: sc.ProfileVersion,
					RubricScores:   sc.RubricScores,
					TopicTags:      sc.TopicTags,
				}
			}
			if err := writeScoreSidecar(workspace, res.Score, res.Verdict, slug, profile, url, extras); err != nil {
				fmt.Fprintf(os.Stderr, "WARN: write _score.json: %v\n", err)
			}

			// 8. Append triage block to README.md (fish line 122 format).
			if err := appendTriageToReadme(workspace, raw); err != nil {
				fmt.Fprintf(os.Stderr, "WARN: append README: %v\n", err)
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(item)
		},
	}

	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().StringVar(&url, "url", "", "URL being triaged (required for persist)")
	cmd.Flags().StringVar(&profile, "profile", "eng", "scoring profile (eng/life/finance/...)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "workspace directory (default: cwd)")
	cmd.Flags().StringVar(&contentFile, "content-file", "", "content file to score (default: <workspace>/README.md, '-' for stdin)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "assemble + dump prompt, skip Haiku call and persist")
	cmd.Flags().BoolVar(&noPersist, "no-persist", false, "call Haiku + parse but skip queue write and sidecar (used by eval)")
	cmd.Flags().BoolVar(&useMarkdown, "use-markdown", false, "fallback to legacy regex-parsed markdown evaluator instead of the default JSON evaluator")

	return cmd
}

// loadProfileTemplate finds the profile prompt template using the same
// precedence as _uinit_profile_prompt.fish lines 19-29:
//
//  1. $ORG_PATH/docs/prompts/profiles/<profile>.{yaml,md}
//  2. ~/code/personal/docs/prompts/profiles/<profile>.{yaml,md}
//
// EPIC-044 M2: YAML manifests (Layer 1) are tried first; the legacy .md
// path is the fallback so the migration can land per-profile without
// bricking unmigrated ones.
func loadProfileTemplate(profile string) (path, content string, err error) {
	return profileTemplateLookup(profile, func(m *ProfileManifest) (string, error) {
		return m.Render()
	})
}

// loadProfileTemplateJSON is loadProfileTemplate for the JSON scoring path.
// Strips the markdown-only Output Format table and Key Facts section to
// reduce token cost and avoid conflicting rendering instructions. EPIC-089 M3.
func loadProfileTemplateJSON(profile string) (path, content string, err error) {
	return profileTemplateLookup(profile, func(m *ProfileManifest) (string, error) {
		return m.RenderForJSON()
	})
}

// profileTemplateLookup is the shared directory-search implementation for
// loadProfileTemplate and loadProfileTemplateJSON.
func profileTemplateLookup(profile string, render func(*ProfileManifest) (string, error)) (path, content string, err error) {
	var dirs []string
	if orgPath := os.Getenv("ORG_PATH"); orgPath != "" {
		dirs = append(dirs, filepath.Join(orgPath, "docs", "prompts", "profiles"))
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		dirs = append(dirs, filepath.Join(home, "code", "personal", "docs", "prompts", "profiles"))
	}
	var checked []string
	for _, d := range dirs {
		yamlPath := filepath.Join(d, profile+".yaml")
		checked = append(checked, yamlPath)
		if _, statErr := os.Stat(yamlPath); statErr == nil {
			m, lerr := LoadProfileManifest(yamlPath)
			if lerr != nil {
				return "", "", lerr
			}
			rendered, rerr := render(m)
			if rerr != nil {
				return "", "", rerr
			}
			return yamlPath, rendered, nil
		}
		mdPath := filepath.Join(d, profile+".md")
		checked = append(checked, mdPath)
		b, rerr := os.ReadFile(mdPath)
		if rerr != nil {
			continue
		}
		if len(bytes.TrimSpace(b)) == 0 {
			continue
		}
		return mdPath, string(b), nil
	}
	return "", "", fmt.Errorf("no profile prompt template for %q (checked %v)", profile, checked)
}

// loadProfileTemplateForMode loads a profile YAML and renders it for
// the given content mode (e.g., "vision", "audio", "text").
// Falls back to standard Render() for non-YAML profiles.
func loadProfileTemplateForMode(profile, mode string) (path, content string, err error) {
	return profileTemplateForModeLookup(profile, mode, func(m *ProfileManifest) (string, error) {
		return m.RenderForMode(mode)
	})
}

// loadProfileTemplateForModeJSON is loadProfileTemplateForMode for the JSON
// scoring path. Strips markdown-only sections. EPIC-089 M3.
func loadProfileTemplateForModeJSON(profile, mode string) (path, content string, err error) {
	return profileTemplateForModeLookup(profile, mode, func(m *ProfileManifest) (string, error) {
		return m.RenderForModeJSON(mode)
	})
}

// profileTemplateForModeLookup is the shared implementation for
// loadProfileTemplateForMode and loadProfileTemplateForModeJSON.
func profileTemplateForModeLookup(profile, mode string, render func(*ProfileManifest) (string, error)) (path, content string, err error) {
	var dirs []string
	if orgPath := os.Getenv("ORG_PATH"); orgPath != "" {
		dirs = append(dirs, filepath.Join(orgPath, "docs", "prompts", "profiles"))
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		dirs = append(dirs, filepath.Join(home, "code", "personal", "docs", "prompts", "profiles"))
	}
	var checked []string
	for _, d := range dirs {
		yamlPath := filepath.Join(d, profile+".yaml")
		checked = append(checked, yamlPath)
		if _, statErr := os.Stat(yamlPath); statErr == nil {
			m, lerr := LoadProfileManifest(yamlPath)
			if lerr != nil {
				return "", "", lerr
			}
			rendered, rerr := render(m)
			if rerr != nil {
				return "", "", rerr
			}
			return yamlPath, rendered, nil
		}
		mdPath := filepath.Join(d, profile+".md")
		checked = append(checked, mdPath)
		b, rerr := os.ReadFile(mdPath)
		if rerr != nil {
			continue
		}
		if len(bytes.TrimSpace(b)) == 0 {
			continue
		}
		return mdPath, string(b), nil
	}
	return "", "", fmt.Errorf("no profile prompt template for %q (checked %v)", profile, checked)
}

// claudeBinaryPath is the resolved path to the claude CLI binary.
// Set at startup from ServerConfig.ClaudePath; defaults to "claude".
var claudeBinaryPath = "claude"

// visionModelName is the model used for vision scoring.
// Set at startup from ServerConfig.VisionModel; defaults to claudeModel.
var visionModelName = claudeModel

// initClaudeConfig resolves ClaudePath and VisionModel from ServerConfig and
// logs the resolved values at startup. EPIC-080 M6.
//
// Architecture note (EPIC-008 M5): CLI exec is the permanent integration
// pattern. The claude CLI authenticates via OAuth2 device flow, storing tokens
// in ~/.claude/. Linkari is designed for self-hosted deployment — each user
// installs and runs it on their own laptop using their own Claude Code
// subscription. There are no plans to support Anthropic API keys, SDK client
// libraries, or direct HTTP calls to the Anthropic API. All scoring paths
// (URL, image, audio) shell out to the claude binary and this is by design,
// not a migration gap. Do not introduce ANTHROPIC_API_KEY passthrough.
func initClaudeConfig(cfg *ServerConfig) {
	if cfg != nil && cfg.ClaudePath != "" {
		claudeBinaryPath = cfg.ClaudePath
	}
	if cfg != nil && cfg.VisionModel != "" {
		visionModelName = cfg.VisionModel
	}
	// EPIC-081 M3: image noise gate threshold.
	if cfg != nil && cfg.ImageNoiseGateMinBytes > 0 {
		imageNoiseGateMinBytes = cfg.ImageNoiseGateMinBytes
	}
	// EPIC-084 M2: prefilter notification gate.
	if cfg != nil {
		notifyOnPrefilterSkip = cfg.NotifyOnPrefilterSkip
	}
	// EPIC-088 M4: configurable unsupported pipeline domain list.
	if cfg != nil && len(cfg.UnsupportedPipelineDomains) > 0 {
		setUnsupportedPipelineDomains(cfg.UnsupportedPipelineDomains)
	}
	// EPIC-009 M1: transcript directory and yt-dlp path.
	// EPIC-090 M5: tilde expansion for transcripts_dir.
	if cfg != nil && cfg.TranscriptsDir != "" {
		transcriptDir = expandTilde(cfg.TranscriptsDir)
	}
	if cfg != nil && cfg.YtdlpPath != "" {
		ytdlpBinaryPath = cfg.YtdlpPath
	}
	if cfg != nil && cfg.FfmpegPath != "" {
		ffmpegBinaryPath = cfg.FfmpegPath
	}
	if cfg != nil && cfg.LiteParseePath != "" {
		liteparseBinaryPath = cfg.LiteParseePath
	}
	// EPIC-090 M5: per-field YouTube tuning.
	if cfg != nil && cfg.YouTube.SubtitleLangs != "" {
		ytSubtitleLangs = cfg.YouTube.SubtitleLangs
	}
	if cfg != nil && cfg.YouTube.TimeoutSeconds > 0 {
		ytTimeoutSeconds = cfg.YouTube.TimeoutSeconds
	}
	// EPIC-001 M3: enable audio fallback when no subtitles.
	// Only override the package default (true) when config explicitly sets true.
	// An absent YAML field (Go zero-value false) must NOT kill the default.
	if cfg != nil && cfg.YouTube.FallbackToAudio {
		ytFallbackToAudio = true
	}
	slog.Info("claude config resolved",
		"event_type", "claude_config_init",
		"claude_path", claudeBinaryPath,
		"vision_model", visionModelName,
		"scoring_model", claudeModel,
		"image_noise_gate_min_bytes", imageNoiseGateMinBytes,
	)

	// EPIC-008 M5: startup smoke test — validate the claude binary is
	// accessible and responds to --version. Fail fast with an actionable
	// error rather than discovering the problem on the first scoring request.
	if err := validateClaudeCLI(); err != nil {
		slog.Error("claude CLI validation failed — scoring will not work",
			"event_type", "claude_cli_validation_failed",
			"claude_path", claudeBinaryPath,
			"error", err,
		)
	}
}

// expandTilde expands a leading "~/" in a path to the user's home directory.
// Non-tilde paths are returned unchanged. EPIC-090 M5.
func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, path[2:])
}

// validateClaudeCLI runs `claude --version` as a lightweight smoke test to
// confirm the binary is accessible and executable. EPIC-008 M5.
func validateClaudeCLI() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudeBinaryPath, "--version")
	cmd.Env = haikuEnv()
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("claude --version failed: %w (is %q in PATH?)", err, claudeBinaryPath)
	}
	version := strings.TrimSpace(string(out))
	slog.Info("claude CLI validated",
		"event_type", "claude_cli_validated",
		"claude_path", claudeBinaryPath,
		"version", version,
	)
	return nil
}

// haikuEnv returns os.Environ with scoring-unsafe variables stripped.
// CLAUDECODE is removed so the claude CLI behaves as a standalone subprocess
// rather than a nested Claude Code session. ANTHROPIC_API_KEY and CLAUDE_API_KEY
// are stripped to enforce the CLI-only auth invariant — Linkari authenticates
// exclusively via the claude CLI's OAuth2 session, not API keys. EPIC-089 M5.
func haikuEnv() []string {
	env := os.Environ()
	filtered := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDECODE=") ||
			strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") ||
			strings.HasPrefix(kv, "CLAUDE_API_KEY=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	// EPIC-062 M6: suppress CLAUDE.md injection in scoring calls to save ~5-8K tokens.
	filtered = append(filtered, "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1")
	return filtered
}

// logHaikuEnvKeys logs the presence (not values) of LLM-related environment
// variables at startup. These are diagnostic only — Linkari authenticates
// exclusively via the claude CLI's OAuth2 session, NOT via API keys.
// The keys are logged to help debug subprocess behavior, not because they
// are expected to be set. EPIC-080 M3.
func logHaikuEnvKeys() {
	slog.Debug("haiku env keys", "stripped", []string{"ANTHROPIC_API_KEY", "CLAUDE_API_KEY", "CLAUDECODE"})
}

// claudeExecOpts configures the flag set for a `claude --print` invocation.
// buildClaudeArgs assembles the args slice from these options, ensuring a
// single point of flag assembly across all scoring paths. EPIC-008 M2.
type claudeExecOpts struct {
	Model          string // e.g. claudeModel or visionModelName
	MaxTurns       string // "1" for plain text, "3" for JSON/vision
	Tools          string // "--tools" value; empty string disables all
	AllowedTools   string // "--allowedTools" value; empty omits the flag
	OutputFormat   string // "json" or empty (plain text)
	JSONSchema     string // schema string; empty omits --json-schema
	SystemPrompt   string // path to system prompt temp file
}

// buildClaudeArgs returns the args slice for exec.CommandContext. The binary
// path is NOT included — it's the first arg to CommandContext, not part of args.
// When adding or removing flags, update allBuildClaudeArgsFlags() in
// claude_contract_test.go — the contract test validates flags against the
// installed claude binary.
func buildClaudeArgs(opts claudeExecOpts) []string {
	args := []string{
		"--print",
		"--model", opts.Model,
		"--max-turns", opts.MaxTurns,
	}
	if opts.Tools != "" || opts.AllowedTools == "" {
		// --tools "" disables all tools (plain text + JSON paths).
		// Only omitted when AllowedTools is explicitly set (vision path).
		args = append(args, "--tools", opts.Tools)
	}
	if opts.AllowedTools != "" {
		args = append(args, "--allowedTools", opts.AllowedTools)
	}
	if opts.OutputFormat != "" {
		args = append(args, "--output-format", opts.OutputFormat)
	}
	if opts.JSONSchema != "" {
		args = append(args, "--json-schema", opts.JSONSchema)
	}
	args = append(args,
		"--system-prompt-file", opts.SystemPrompt,
		"--effort", "low",
		"--no-session-persistence",
	)
	return args
}

// writeSystemPromptFile writes the system prompt to a temp file and returns
// the path and content hash. The caller must defer os.Remove on the returned path.
// EPIC-062 M1: --system-prompt-file is used instead of inline --system-prompt.
// EPIC-082 M1: returns prompt hash alongside path for traceability.
func writeSystemPromptFile(prompt string) (string, string, error) {
	// EPIC-008 M6: explicit 0600 permissions — system prompts may contain
	// scoring rubrics and profile-specific instructions.
	tmpDir := os.TempDir()
	f, err := os.CreateTemp(tmpDir, "linkari-sysprompt-*.txt")
	if err != nil {
		return "", "", fmt.Errorf("create system prompt file: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", "", fmt.Errorf("chmod system prompt file: %w", err)
	}
	if _, err := f.WriteString(prompt); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", "", fmt.Errorf("write system prompt file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", "", fmt.Errorf("close system prompt file: %w", err)
	}
	return f.Name(), promptHash(prompt), nil
}

// runClaudeHaiku shells out to the claude CLI for a single-turn Haiku call.
//
// EPIC-062: --system-prompt-file replaces the entire default system prompt
// (including dynamic sections like git status, working dir, memory), so no
// additional flag is needed to suppress them. --effort low for faster inference,
// --no-session-persistence to avoid writing session state to disk.
func runClaudeHaiku(ctx context.Context, systemPrompt, content string) (string, error) {
	spFile, _, err := writeSystemPromptFile(systemPrompt)
	if err != nil {
		return "", err
	}
	defer os.Remove(spFile)

	cmd := exec.CommandContext(ctx, claudeBinaryPath, buildClaudeArgs(claudeExecOpts{
		Model:        claudeModel,
		MaxTurns:     "1",
		Tools:        "",
		SystemPrompt: spFile,
	})...)
	cmd.Stdin = strings.NewReader(content)
	cmd.Dir = os.TempDir() // EPIC-088 M1: prevent subprocess from discovering workspace CLAUDE.md
	cmd.Env = haikuEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude exec: %w (stderr=%s)", err, strings.TrimSpace(stderr.String()))
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return "", fmt.Errorf("claude returned empty output")
	}
	return out, nil
}

// parseTriageMarkdown extracts a TriageResult from Haiku's markdown output.
// Score is mandatory; Verdict / ActionItems / Tags are best-effort.
func parseTriageMarkdown(md string) (TriageResult, error) {
	// Try parse as-is first; if score regex misses, run normalizer.
	score, err := parseScoreFromMarkdown(md)
	normalized := md
	if err != nil {
		normalized = normalizeTriageMarkdown(md)
		score, err = parseScoreFromMarkdown(normalized)
		if err != nil {
			return TriageResult{}, err
		}
	}

	verdict := extractVerdict(normalized)
	actionItems := extractActionItems(normalized)
	tags := extractTagsLine(normalized)

	return TriageResult{
		Score:       score,
		Verdict:     verdict,
		ActionItems: actionItems,
		Tags:        tags,
	}, nil
}

// verdictRE captures the text after `## Verdict` up to the next `##` or EOF.
// Handles both `## Verdict\nbody` and the flat-line `## Verdict body` form
// the existing fixtures use (see 20260404_103238_gh_teamchong-turboquant-wasm).
var verdictRE = regexp.MustCompile(`(?s)##\s*Verdict\s*\n?\s*(.+?)(?:\n##|$)`)

func extractVerdict(md string) string {
	m := verdictRE.FindStringSubmatch(md)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// actionItemsRE locates the `## Action Items` block and grabs everything
// up to the next `##` heading.
var actionItemsRE = regexp.MustCompile(`(?s)##\s*Action Items\s*\n?(.+?)(?:\n##|$)`)
var actionItemLineRE = regexp.MustCompile(`(?m)^(?:\s*[-*]\s+|\s*\d+\.\s+)(.+)$`)

func extractActionItems(md string) []string {
	m := actionItemsRE.FindStringSubmatch(md)
	if len(m) < 2 {
		return nil
	}
	block := m[1]
	var items []string
	for _, lm := range actionItemLineRE.FindAllStringSubmatch(block, -1) {
		if len(lm) < 2 {
			continue
		}
		items = append(items, strings.TrimSpace(lm[1]))
	}
	return items
}

// tagsLineRE matches `Tags: a, b, c` on its own line.
var tagsLineRE = regexp.MustCompile(`(?mi)^\s*Tags:\s*(.+)$`)

func extractTagsLine(md string) string {
	m := tagsLineRE.FindStringSubmatch(md)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// normalizeTriageMarkdown is a Go port of the python normalizer in
// _uinit_profile_prompt.fish lines 62-114. It only kicks in if the raw
// parser failed — Haiku usually emits clean markdown, but occasionally
// returns everything on one line.
func normalizeTriageMarkdown(text string) string {
	// 1. Newlines before ## headings.
	reHeading := regexp.MustCompile(`  ?(?:## )`)
	text = reHeading.ReplaceAllStringFunc(text, func(s string) string {
		return "\n\n## "
	})
	// 2. Newlines before list items.
	reBullet := regexp.MustCompile(` (?:- \*?\*?[A-Z])`)
	text = reBullet.ReplaceAllStringFunc(text, func(s string) string {
		return "\n" + strings.TrimPrefix(s, " ")
	})
	reNum := regexp.MustCompile(` (?:\d+\.\s)`)
	text = reNum.ReplaceAllStringFunc(text, func(s string) string {
		return "\n" + strings.TrimPrefix(s, " ")
	})
	// 3. Collapse triple+ newlines.
	reNL := regexp.MustCompile(`\n{3,}`)
	text = reNL.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// truncateRunes returns at most n runes from s, matching fish `string sub -l n`.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// sidecarExtras carries the EPIC-044 M1 additive fields. Pass nil from the
// legacy markdown path; pass a populated value from the JSON path. Every
// field is omitempty so the v1 sidecar shape (the six fields below) stays
// byte-stable when extras is nil — see TestWriteScoreSidecar / the
// `_score.json` additive-only invariant in cmd_eval.go captureFromWorkspace.
type sidecarExtras struct {
	SchemaVersion  string         `json:"schema_version,omitempty"`
	ProfileVersion int            `json:"profile_version,omitempty"`
	RubricScores   map[string]int `json:"rubric_scores,omitempty"`
	TopicTags      []string       `json:"topic_tags,omitempty"`
}

// writeScoreSidecar writes _score.json with byte-equivalent shape to fish
// _uinit_profile_prompt.fish lines 138-145, plus optional additive fields
// (schema_version, profile_version, rubric_scores) when extras is non-nil.
func writeScoreSidecar(workspace string, score int, verdict, slug, profile, url string, extras *sidecarExtras) error {
	type payloadV1 struct {
		Score    int    `json:"score"`
		Verdict  string `json:"verdict"`
		Slug     string `json:"slug"`
		Profile  string `json:"profile"`
		URL      string `json:"url"`
		ScoredAt string `json:"scored_at"`
	}
	type payloadV2 struct {
		payloadV1
		sidecarExtras
	}
	v1 := payloadV1{
		Score:    score,
		Verdict:  verdict,
		Slug:     slug,
		Profile:  profile,
		URL:      url,
		ScoredAt: nowRFC3339UTC(),
	}
	var b []byte
	var err error
	if extras == nil {
		b, err = json.Marshal(v1)
	} else {
		b, err = json.Marshal(payloadV2{payloadV1: v1, sidecarExtras: *extras})
	}
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(filepath.Join(workspace, "_score.json"), b, 0644)
}

// appendTriageToReadme appends `\n---\n\n<raw>\n` to README.md, matching
// fish line 122: `printf '\n---\n\n%s\n' "$triage_normalized" >> "$readme_file"`.
func appendTriageToReadme(workspace, raw string) error {
	path := filepath.Join(workspace, "README.md")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n---\n\n%s\n", raw)
	return err
}

// nowRFC3339UTC is split out so tests can stub the timestamp.
var nowRFC3339UTC = func() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// --- Eval harness wiring ---

// triageScorer implements Scorer (cmd_eval.go) by running the real Go
// triage path against each fixture's content + profile. Used by `linkari
// eval run` once registeredScorer() returns it.
type triageScorer struct{}

func (triageScorer) Name() string { return "triage-haiku" }

func (triageScorer) Score(fix Fixture) (Golden, error) {
	tmplPath, sysPrompt, err := loadProfileTemplateJSON(fix.Profile)
	if err != nil {
		return Golden{}, fmt.Errorf("load template: %w", err)
	}
	// Fixture content was already truncated at capture time, but apply the
	// same truncation here so eval is robust against future fixture sources.
	content := truncateRunes(fix.Content, contentTruncationRunes)

	eval := HaikuJSONEvaluator{}
	sc, err := eval.Evaluate(context.Background(), content, sysPrompt)
	if err != nil {
		// M6b: scorer brittleness fix. A malformed Haiku response (missing
		// `## Score: N/100` line) no longer hard-errors the whole eval run.
		// Return a Skip so the runner reports it and moves on.
		return Golden{Skip: true, SkipReason: "parse_failed", RawMarkdown: ""}, nil
	}
	// M6b: noise-gate skip. When Haiku emits a `Score: 0/100 — Skip (...)`
	// response against a fixture whose golden is non-zero, that's not a
	// regression — it's the profile's noise gate firing on stale or
	// JavaScript-stripped content. Treat as skip, not fail.
	if sc.Score == 0 && fix.Golden.Score > 0 && isNoiseGateOutput(sc.RawMarkdown) {
		return Golden{Skip: true, SkipReason: "noise_gate", RawMarkdown: sc.RawMarkdown}, nil
	}
	return Golden{
		Score:         sc.Score,
		Verdict:       sc.Verdict,
		RawMarkdown:   sc.RawMarkdown,
		Gaps:          sc.Gaps,
		SourceType:    "eval-fixture",
		PromptVersion: promptVersionFromPath(tmplPath),
		PromptHash:    promptHash(sysPrompt), // EPIC-082 M4
	}, nil
}

// isNoiseGateOutput returns true if the raw Haiku markdown looks like the
// profile noise-gate template response (score 0 + "Skip" label).
func isNoiseGateOutput(raw string) bool {
	low := strings.ToLower(raw)
	return strings.Contains(low, "score: 0/100") && strings.Contains(low, "skip")
}
