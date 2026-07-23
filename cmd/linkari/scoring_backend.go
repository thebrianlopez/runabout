package main

import (
	"context"
	"log/slog"
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

// activeScoringBackend is set at startup from ServerConfig.Scoring via
// initScoringBackend. Tests may replace it with a deterministic fake
// implementing ScoringBackend. If nil at call time it is a programming error
// (missing init call) - callers will panic.
//
// Default: ClaudeCLIScoringBackend so the server is functional before
// initScoringBackend is called (e.g., in unit tests that don't run server
// startup).
var activeScoringBackend ScoringBackend = ClaudeCLIScoringBackend{}

// ClaudeCLIScoringBackend wraps the existing runClaudeHaiku / runClaudeHaikuJSON
// functions. This is the default backend and produces zero behavioral change
// from pre-EPIC-217 baseline. EPIC-217 F2.
type ClaudeCLIScoringBackend struct{}

func (b ClaudeCLIScoringBackend) Name() string { return "claude_cli" }

// Complete delegates to runClaudeHaiku.
func (b ClaudeCLIScoringBackend) Complete(ctx context.Context, systemPrompt, content string) (string, error) {
	return runClaudeHaiku(ctx, systemPrompt, content)
}

// CompleteJSON delegates to runClaudeHaikuJSON.
func (b ClaudeCLIScoringBackend) CompleteJSON(ctx context.Context, systemPrompt, content, schema string) ([]byte, error) {
	return runClaudeHaikuJSON(ctx, systemPrompt, content, schema)
}

// CompleteVision delegates to runClaudeHaikuVision.
func (b ClaudeCLIScoringBackend) CompleteVision(ctx context.Context, systemPrompt, textContent, imagePath, schema string) ([]byte, error) {
	return runClaudeHaikuVision(ctx, systemPrompt, textContent, imagePath, schema)
}

// execHaiku is the indirection point tests stub. Production path routes
// through activeScoringBackend.Complete; tests can swap in a deterministic
// fake by replacing either this var or activeScoringBackend. EPIC-217 M1.
var execHaiku = func(ctx context.Context, sp, content string) (string, error) {
	start := time.Now()
	result, err := activeScoringBackend.Complete(ctx, sp, content)
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	slog.Info(
		"scoring_call_complete",
		"event_type", "scoring_call",
		"backend", activeScoringBackend.Name(),
		"method", "Complete",
		"duration_ms", time.Since(start).Milliseconds(),
		"error", errStr,
	)
	return result, err
}

// execHaikuJSON is the indirection point tests stub. Production path routes
// through activeScoringBackend.CompleteJSON; tests can swap in a deterministic
// fake by replacing either this var or activeScoringBackend. EPIC-217 M1.
var execHaikuJSON = func(ctx context.Context, sp, content, schema string) ([]byte, error) {
	start := time.Now()
	result, err := activeScoringBackend.CompleteJSON(ctx, sp, content, schema)
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	slog.Info(
		"scoring_call_complete_json",
		"event_type", "scoring_call",
		"backend", activeScoringBackend.Name(),
		"method", "CompleteJSON",
		"duration_ms", time.Since(start).Milliseconds(),
		"error", errStr,
	)
	return result, err
}

// execHaikuVision routes image scoring through the active backend.
var execHaikuVision = func(ctx context.Context, systemPrompt, textContent, imagePath, schema string) ([]byte, error) {
	start := time.Now()
	result, err := activeScoringBackend.CompleteVision(ctx, systemPrompt, textContent, imagePath, schema)
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	slog.Info(
		"scoring_call_complete_vision",
		"event_type", "scoring_call",
		"backend", activeScoringBackend.Name(),
		"method", "CompleteVision",
		"duration_ms", time.Since(start).Milliseconds(),
		"error", errStr,
	)
	return result, err
}
