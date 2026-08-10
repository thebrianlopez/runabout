package main

import (
	"context"
	"fmt"
)

// funcScoringBackend is a func-based ScoringBackend fake for tests that need
// to inspect inputs, count calls, or return sequenced responses. Unset methods
// return an error so unexpected LLM calls fail loudly rather than hanging on
// a real CLI invocation.
//
// EPIC-258 M2: this replaces the former exec* package-var stubs
// (execHaikuJSON, execContentClassify, execHaikuVision, execHaikuSynopsisJSON,
// execHaiku). Those were package globals written by tests and read by scoring
// goroutines - the data-race class this epic removes. Tests now inject a
// backend explicitly: via function arguments for unit-level calls, via
// evaluator/deps structs for scoreAsync-level calls, or via
// Router.SetScoringBackend for HTTP-path tests.
type funcScoringBackend struct {
	name           string
	complete       func(ctx context.Context, systemPrompt, content string) (string, error)
	completeJSON   func(ctx context.Context, systemPrompt, content, schema string) ([]byte, error)
	completeVision func(ctx context.Context, systemPrompt, textContent, imagePath, schema string) ([]byte, error)
}

func (f *funcScoringBackend) Name() string {
	if f.name != "" {
		return f.name
	}
	return "func-stub"
}

func (f *funcScoringBackend) Complete(ctx context.Context, systemPrompt, content string) (string, error) {
	if f.complete == nil {
		return "", fmt.Errorf("funcScoringBackend: unexpected Complete call")
	}
	return f.complete(ctx, systemPrompt, content)
}

func (f *funcScoringBackend) CompleteJSON(ctx context.Context, systemPrompt, content, schema string) ([]byte, error) {
	if f.completeJSON == nil {
		return nil, fmt.Errorf("funcScoringBackend: unexpected CompleteJSON call")
	}
	return f.completeJSON(ctx, systemPrompt, content, schema)
}

func (f *funcScoringBackend) CompleteVision(ctx context.Context, systemPrompt, textContent, imagePath, schema string) ([]byte, error) {
	if f.completeVision == nil {
		return nil, fmt.Errorf("funcScoringBackend: unexpected CompleteVision call")
	}
	return f.completeVision(ctx, systemPrompt, textContent, imagePath, schema)
}

// cannedVerdictJSON is a valid claude-CLI-envelope scoring response usable as
// a CompleteJSON return value wherever a background scoring goroutine just
// needs to terminate cleanly.
const cannedVerdictJSON = `{"type":"result","result":"{\"score\":70,\"verdict\":\"ok\",\"rubric_scores\":{\"Clarity\":14,\"Actionability\":14,\"Novelty\":14,\"Urgency\":14,\"Topic Match\":14},\"topic_tags\":[\"test\"]}","is_error":false,"usage":{"input_tokens":10,"output_tokens":20},"total_cost_usd":0.001}`

// jsonOnlyBackend returns a backend whose CompleteJSON always returns raw.
func jsonOnlyBackend(raw string) *funcScoringBackend {
	return &funcScoringBackend{
		completeJSON: func(context.Context, string, string, string) ([]byte, error) {
			return []byte(raw), nil
		},
	}
}
