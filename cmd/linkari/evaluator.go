package main

// EPIC-058 M1: Evaluator interface and Scorecard struct.
//
// This file defines the first-class abstraction that unifies all scoring
// paths behind a single Evaluate() contract. The Evaluator is
// backend-agnostic — production uses Claude Haiku via the claude CLI,
// but the interface is swappable for local models or alternative providers.

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// TokenUsage tracks per-call token consumption from the claude CLI JSON envelope.
// EPIC-062 M2: parsed from the --output-format json response metadata.
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Scorecard is the unified output of any Evaluate() call. It replaces the
// split between TriageResult (markdown path) and TriageVerdict (JSON path)
// with a single struct that both paths populate.
type Scorecard struct {
	Score          int            `json:"score"`
	Verdict        string         `json:"verdict"`
	Gaps           []string       `json:"gaps,omitempty"`           // specific missing elements needed to reach threshold
	Tags           string         `json:"tags,omitempty"`
	TopicTags      []string       `json:"topic_tags,omitempty"`
	ActionRoute    string         `json:"action_route,omitempty"`
	RubricScores   map[string]int `json:"rubric_scores,omitempty"`
	RawMarkdown    string         `json:"raw_markdown,omitempty"`
	Profile        string         `json:"profile,omitempty"`
	ProfileVersion int            `json:"profile_version,omitempty"`
	Backend        string         `json:"backend,omitempty"`        // e.g. "claude-haiku"
	LatencyMs      int64          `json:"latency_ms,omitempty"`
	PromptVersion  string         `json:"prompt_version,omitempty"` // git SHA of template file
	SourceType     string         `json:"source_type,omitempty"`    // "cli-triage", "cli-score", "eval-refresh", "eval-fixture"
	CostUSD        float64        `json:"cost_usd,omitempty"`       // EPIC-062 M2: per-call cost from JSON envelope
	Usage          *TokenUsage    `json:"usage,omitempty"`           // EPIC-062 M2: per-call token usage
}

// Evaluator is the backend-agnostic scoring contract. All content
// assessment paths converge on this interface.
type Evaluator interface {
	// Evaluate scores content against a prompt template and returns a Scorecard.
	Evaluate(ctx context.Context, content, promptTemplate string) (*Scorecard, error)

	// Name returns a human-readable identifier for the evaluator backend
	// (e.g. "claude-haiku-md", "claude-haiku-json").
	Name() string
}

// HaikuMarkdownEvaluator implements Evaluator using the legacy markdown path
// (execHaiku + parseTriageMarkdown).
type HaikuMarkdownEvaluator struct{}

func (HaikuMarkdownEvaluator) Name() string { return "claude-haiku-md" }

func (HaikuMarkdownEvaluator) Evaluate(ctx context.Context, content, promptTemplate string) (*Scorecard, error) {
	start := time.Now()
	rawMD, err := execHaiku(ctx, promptTemplate, content)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("haiku: %w", err)
	}
	res, err := parseTriageMarkdown(rawMD)
	if err != nil {
		return nil, fmt.Errorf("parse triage: %w", err)
	}
	return &Scorecard{
		Score:       res.Score,
		Verdict:     res.Verdict,
		Gaps:        res.ActionItems,
		Tags:        res.Tags,
		RawMarkdown: rawMD,
		Backend:     "claude-haiku-md",
		LatencyMs:   latency,
	}, nil
}

// HaikuJSONEvaluator implements Evaluator using the typed JSON path
// (haikuVerdictWithRepair → TriageVerdict).
type HaikuJSONEvaluator struct{}

func (HaikuJSONEvaluator) Name() string { return "claude-haiku-json" }

func (HaikuJSONEvaluator) Evaluate(ctx context.Context, content, promptTemplate string) (*Scorecard, error) {
	start := time.Now()
	v, meta, err := haikuVerdictWithRepair(ctx, promptTemplate, content)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("haiku-json: %w", err)
	}
	sc := &Scorecard{
		Score:          v.Score,
		Verdict:        v.Verdict,
		Gaps:           v.ActionItems,
		Tags:           v.Tags,
		TopicTags:      v.TopicTags,
		RubricScores:   v.RubricScores,
		RawMarkdown:    v.RenderMarkdown(),
		Profile:        v.Profile,
		ProfileVersion: v.ProfileVersion,
		Backend:        "claude-haiku-json",
		LatencyMs:      latency,
	}
	if meta != nil {
		sc.CostUSD = meta.CostUSD
		sc.Usage = meta.Usage
		slog.Info("evaluator: token usage",
			"backend", "claude-haiku-json",
			"cost_usd", meta.CostUSD,
			"input_tokens", tokenCount(meta.Usage, true),
			"output_tokens", tokenCount(meta.Usage, false),
			"latency_ms", latency,
		)
	}
	return sc, nil
}

// HaikuVisionEvaluator implements Evaluator for image shares. It calls the
// claude CLI with the Read tool enabled so it can read local image files for
// multimodal scoring. EPIC-079 M3.
type HaikuVisionEvaluator struct {
	ImagePath string // path to the local image file
}

func (e HaikuVisionEvaluator) Name() string { return "claude-haiku-vision" }

func (e HaikuVisionEvaluator) Evaluate(ctx context.Context, content, promptTemplate string) (*Scorecard, error) {
	start := time.Now()
	raw, err := runClaudeHaikuVision(ctx, promptTemplate, content, e.ImagePath, triageVerdictSchema)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("haiku-vision: %w", err)
	}
	v, meta, err := parseHaikuEnvelope(raw)
	if err != nil {
		return nil, fmt.Errorf("haiku-vision parse: %w", err)
	}
	sc := &Scorecard{
		Score:          v.Score,
		Verdict:        v.Verdict,
		Gaps:           v.ActionItems,
		Tags:           v.Tags,
		TopicTags:      v.TopicTags,
		RubricScores:   v.RubricScores,
		RawMarkdown:    v.RenderMarkdown(),
		Profile:        v.Profile,
		ProfileVersion: v.ProfileVersion,
		Backend:        "claude-haiku-vision",
		LatencyMs:      latency,
	}
	if meta != nil {
		sc.CostUSD = meta.CostUSD
		sc.Usage = meta.Usage
		slog.Info("evaluator: token usage",
			"backend", "claude-haiku-vision",
			"cost_usd", meta.CostUSD,
			"input_tokens", tokenCount(meta.Usage, true),
			"output_tokens", tokenCount(meta.Usage, false),
			"latency_ms", latency,
		)
	}
	return sc, nil
}

// tokenCount safely extracts input or output token count from a TokenUsage pointer.
func tokenCount(u *TokenUsage, input bool) int {
	if u == nil {
		return 0
	}
	if input {
		return u.InputTokens
	}
	return u.OutputTokens
}

// GapSummary returns a short human-readable summary of the scorecard's gaps
// for use in push notifications and log messages.
func (s *Scorecard) GapSummary(maxItems int) string {
	if len(s.Gaps) == 0 {
		return ""
	}
	n := len(s.Gaps)
	if maxItems > 0 && n > maxItems {
		n = maxItems
	}
	items := s.Gaps[:n]
	summary := strings.Join(items, "; ")
	if len(s.Gaps) > n {
		summary += fmt.Sprintf(" (+%d more)", len(s.Gaps)-n)
	}
	return summary
}

// CheckGate returns true if the scorecard's score meets the action's
// confidence threshold and auto-launch is enabled.
func CheckGate(sc *Scorecard, cfg ActionConfig) bool {
	if cfg.ConfidenceThreshold <= 0 || !cfg.AutoLaunch {
		return false
	}
	return sc.Score >= cfg.ConfidenceThreshold
}

// lookupGinitAction loads the config and returns the ginit_auto action
// if it exists and has a confidence threshold configured. Returns nil if
// the action doesn't exist or the config can't be loaded.
func lookupGinitAction(_ string) *ActionConfig {
	cfg, err := LoadConfig("")
	if err != nil {
		return nil
	}
	for i := range cfg.Actions {
		if cfg.Actions[i].ID == "ginit_auto" {
			return &cfg.Actions[i]
		}
	}
	return nil
}

// autoLaunchGinit launches `ginit <key>` in a new tmux window when the
// confidence gate passes (EPIC-058 M4). The Jira key is extracted from
// the URL; if no key is found, the launch is skipped with a warning.
func autoLaunchGinit(url, profile string, score int) {
	key := extractJiraKey(url)
	if key == "" {
		slog.Warn("auto-launch: no Jira key in URL, skipping", "url", url, "profile", profile)
		return
	}
	tmux := &TmuxRunner{}
	cmd := fmt.Sprintf("ginit %s", key)
	if err := tmux.NewWindow("linkari", cmd, "ginit-"+key); err != nil {
		slog.Warn("auto-launch ginit failed", "error", err, "key", key)
	} else {
		slog.Info("auto-launched ginit", "key", key, "score", score, "profile", profile)
	}
}

// extractJiraKey extracts a Jira issue key (e.g. "PROJ-123") from a URL or
// text string. Returns empty string if no key is found.
var jiraKeyExtractRE = regexp.MustCompile(`[A-Z][A-Z0-9_]+-\d+`)

func extractJiraKey(s string) string {
	m := jiraKeyExtractRE.FindString(s)
	return m
}

// promptVersionFromPath returns the git SHA of the last commit that touched
// the given template file. Returns empty string on any error (not in a repo,
// uncommitted file, etc.).
func promptVersionFromPath(tmplPath string) string {
	cmd := exec.CommandContext(context.Background(), "git", "log", "-1", "--format=%H", "--", tmplPath)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
