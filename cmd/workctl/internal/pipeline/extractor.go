package pipeline

import (
	"context"
	"errors"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/insights"
)

// Tier identifies the automation layer an Extractor operates at.
// Matches the five-layer topology: Rules = Go/deterministic layer,
// LocalAI = reserved for local inference, Cloud = Cloud LLM.
type Tier int

const (
	TierRules   Tier = iota // fully deterministic; no model involved
	TierLocalAI             // local inference (reserved; not currently used)
	TierCloud               // Cloud LLM fallback
)

// ErrNotImplemented is returned by stub Extractor implementations (CloudExtractor).
var ErrNotImplemented = errors.New("extractor: not implemented")

// Extractor derives a SignalSet from a slice of Events.
// The extraction strategy (rules, local AI, cloud) is an implementation
// detail invisible to call sites.
type Extractor interface {
	// Extract transforms events into signals.
	// Implementations at TierLocalAI and TierCloud must emit a metrics record
	// to ~/.automation-metrics/local-ai.log as a best-effort side effect.
	// Metric emission failure must never cause Extract to return an error.
	Extract(ctx context.Context, events []Event) (*insights.SignalSet, error)

	// Tier returns the automation layer this extractor operates at.
	Tier() Tier
}
