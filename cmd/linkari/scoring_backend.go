package main

import "context"

// ScoringBackend is the abstraction over single-turn LLM calls used by the
// scoring pipeline. Both text and JSON completion paths are represented.
// Implementations must be safe for concurrent use.
//
// EPIC-217 M1: introduced to decouple the scoring pipeline from the hard-wired
// claude CLI binary. The active implementation is selected at startup via
// initScoringBackend (F4).
type ScoringBackend interface {
	// Complete sends systemPrompt + content to the model and returns the
	// raw text response. Equivalent to the current execHaiku signature.
	Complete(ctx context.Context, systemPrompt, content string) (string, error)

	// CompleteJSON sends systemPrompt + content with a JSON schema hint and
	// returns the raw response bytes. Equivalent to the current execHaikuJSON
	// signature. schema is passed as a string (same as current path).
	CompleteJSON(ctx context.Context, systemPrompt, content, schema string) ([]byte, error)
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

// Complete delegates to runClaudeHaiku.
func (b ClaudeCLIScoringBackend) Complete(ctx context.Context, systemPrompt, content string) (string, error) {
	return runClaudeHaiku(ctx, systemPrompt, content)
}

// CompleteJSON delegates to runClaudeHaikuJSON.
func (b ClaudeCLIScoringBackend) CompleteJSON(ctx context.Context, systemPrompt, content, schema string) ([]byte, error) {
	return runClaudeHaikuJSON(ctx, systemPrompt, content, schema)
}
