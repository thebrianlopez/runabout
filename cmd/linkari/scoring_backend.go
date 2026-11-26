package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// ScoringBackend is the abstraction over single-turn LLM calls used by the
// scoring pipeline. Both text and JSON completion paths are represented.
// Implementations must be safe for concurrent use.
//
// EPIC-217 M1: introduced to decouple the scoring pipeline from the hard-wired
// claude CLI binary. The active implementation is selected at startup via
// initScoringBackend (F4).
type ScoringBackend interface {
	// Name identifies the active backend for logging and tests.
	Name() string

	// Complete sends systemPrompt + content to the model and returns the
	// raw text response. Equivalent to the current execHaiku signature.
	Complete(ctx context.Context, systemPrompt, content string) (string, error)

	// CompleteJSON sends systemPrompt + content with a JSON schema hint and
	// returns the raw response bytes. Equivalent to the current execHaikuJSON
	// signature. schema is passed as a string (same as current path).
	CompleteJSON(ctx context.Context, systemPrompt, content, schema string) ([]byte, error)

	// CompleteVision sends the text prompt plus image path to the model and
	// returns the raw response bytes for multimodal scoring.
	CompleteVision(ctx context.Context, systemPrompt, textContent, imagePath, schema string) ([]byte, error)
}

// effectiveScoringBackend returns the backend name after applying the
// config default. Empty or omitted [server.scoring] backend selects "pi".
// "claude_cli" is rejected at load time (EPIC-217 step 6).
func effectiveScoringBackend(name string) string {
	if name == "" {
		return "pi"
	}
	return name
}

var errClaudeCLIRemoved = errors.New(`[server.scoring] backend = "claude_cli" is no longer supported; use "pi" or "pi_remote"`)

func scoringBackendFromConfig(cfg ScoringConfig) (ScoringBackend, error) {
	switch effectiveScoringBackend(cfg.Backend) {
	case "claude_cli":
		return nil, errClaudeCLIRemoved
	case "pi":
		path := cfg.PiPath
		if path == "" {
			path = "pi"
		}
		return PiScoringBackend{model: piModelString(cfg.Provider, cfg.Model), BinaryPath: path}, nil
	case "pi_remote":
		if cfg.PiServiceURL == "" {
			return nil, errors.New(`[server.scoring] pi_service_url is required when backend = "pi_remote"`)
		}
		return PiRemoteScoringBackend{BaseURL: cfg.PiServiceURL}, nil
	default:
		return nil, fmt.Errorf("unsupported scoring backend %q", cfg.Backend)
	}
}

func loadConfiguredScoringBackend(ctx context.Context, path string) (ScoringBackend, error) {
	cfg, err := LoadConfig(ctx, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && path == "" && os.Getenv("LINKARI_CONFIG") == "" {
			return resolveBackend(nil), nil
		}
		return nil, fmt.Errorf("load scoring config: %w", err)
	}
	SetProfilePathOverride(cfg.Server.ProfilePath)
	return scoringBackendFromConfig(cfg.Server.Scoring)
}

// activeScoringBackend is set at startup from ServerConfig.Scoring via
// initScoringBackend. Tests may replace it with a deterministic fake
// implementing ScoringBackend. If nil at call time it is a programming error
// (missing init call) - callers will panic.
//
// Process default before initScoringBackend: PiScoringBackend ("pi" on PATH).
// Tests that need a deterministic executor must inject a fake ScoringBackend.
var activeScoringBackend ScoringBackend = PiScoringBackend{}

// Envelope format identifiers used to compose evaluator labels. These describe
// the *response format* an evaluator parses, not the provider that produced it.
// The provider half is resolved at runtime from activeScoringBackend.
const (
	evalFormatMarkdown       = "md"
	evalFormatJSON           = "json"
	evalFormatVision         = "vision"
	evalFormatVisionFallback = "vision-fallback"
)

// backendLabel composes the active backend name with an envelope format, e.g.
// "pi:json" or "claude_cli:vision". It is the single source of truth for
// backend attribution in telemetry.
//
// POMO scoring-backend-attribution-split-brain (PA-1, PA-2): evaluator labels
// were previously compile-time constants ("claude-haiku-json") that kept
// reporting Claude after EPIC-246 routed execution through ScoringBackend.
// Resolving from activeScoringBackend makes eval.Name() and Scorecard.Backend
// agree with the scoring_call telemetry emitted by the exec* wrappers.
func backendLabel(b ScoringBackend, format string) string {
	b = resolveBackend(b)
	if b == nil {
		return "unknown:" + format
	}
	return b.Name() + ":" + format
}

// resolveBackend returns b, or the process default when b is nil.
//
// EPIC-258 M2: activeScoringBackend remains the startup-configured default,
// but it is now only read here. Scoring paths carry their backend explicitly
// (scoringDeps.Backend, HaikuJSONEvaluator.Backend), so tests inject a fake
// instead of swapping a package var that scoring goroutines read concurrently.
func resolveBackend(b ScoringBackend) ScoringBackend {
	if b != nil {
		return b
	}
	return activeScoringBackend
}

// backendComplete calls b.Complete and emits the scoring_call telemetry that
// the former execHaiku wrapper owned.
func backendComplete(ctx context.Context, b ScoringBackend, sp, content string) (string, error) {
	b = resolveBackend(b)
	start := time.Now()
	result, err := b.Complete(ctx, sp, content)
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	slog.Info(
		"scoring_call_complete",
		"event_type", "scoring_call",
		"backend", b.Name(),
		"method", "Complete",
		"duration_ms", time.Since(start).Milliseconds(),
		"error", errStr,
	)
	return result, err
}

// backendCompleteJSON calls b.CompleteJSON with scoring_call telemetry.
func backendCompleteJSON(ctx context.Context, b ScoringBackend, sp, content, schema string) ([]byte, error) {
	b = resolveBackend(b)
	start := time.Now()
	result, err := b.CompleteJSON(ctx, sp, content, schema)
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	slog.Info(
		"scoring_call_complete_json",
		"event_type", "scoring_call",
		"backend", b.Name(),
		"method", "CompleteJSON",
		"duration_ms", time.Since(start).Milliseconds(),
		"error", errStr,
	)
	return result, err
}

// backendCompleteVision calls b.CompleteVision with scoring_call telemetry.
func backendCompleteVision(ctx context.Context, b ScoringBackend, sp, textContent, imagePath, schema string) ([]byte, error) {
	b = resolveBackend(b)
	start := time.Now()
	result, err := b.CompleteVision(ctx, sp, textContent, imagePath, schema)
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	slog.Info(
		"scoring_call_complete_vision",
		"event_type", "scoring_call",
		"backend", b.Name(),
		"method", "CompleteVision",
		"duration_ms", time.Since(start).Milliseconds(),
		"error", errStr,
	)
	return result, err
}
