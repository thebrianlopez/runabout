package consensus

import (
	"context"
	"encoding/json"
	"time"
)

// Provider is an OpenAI-compatible LLM provider.
type Provider interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
	Name() string
}

// CompletionRequest is an OpenAI-compatible chat completion request.
type CompletionRequest struct {
	Model       string
	Messages    []Message
	Schema      json.RawMessage
	Temperature float64
}

// Message is a single chat message.
type Message struct {
	Role    string
	Content string
}

// CompletionResponse is the provider response with structured output.
type CompletionResponse struct {
	Output     json.RawMessage
	Confidence float64
	LatencyMs  int64
	CostUSD    float64
}

// ConsensusRequest configures a single L1 round.
type ConsensusRequest struct {
	RoundType       string
	Prompt          string
	Schema          json.RawMessage
	SchemaID        string
	Providers       []Provider
	StabilityRounds int
	PrevHash        string
}

// ModelConsensusRound is the result of a completed L1 round.
type ModelConsensusRound struct {
	ConsensusID     string
	RoundType       string
	PromptHash      string
	SchemaID        string
	Providers       []ProviderResult
	AgreementScore  float64
	ConsensusOutput json.RawMessage
	DivergenceFlags []string
	Resolution      string // "consensus" | "diverged" | "human_required"
	PrevHash        string
	Timestamp       time.Time
}

// ProviderResult is one provider's contribution to a consensus round.
type ProviderResult struct {
	Provider   string
	ModelID    string
	Output     json.RawMessage
	LatencyMs  int64
	CostUSD    float64
	Confidence float64
	Timestamp  time.Time
	Cancelled  bool
}

// Resolution values.
const (
	ResolutionConsensus     = "consensus"
	ResolutionDiverged      = "diverged"
	ResolutionHumanRequired = "human_required"
)

// Thresholds.
const (
	ThresholdConsensus       = 0.85
	ThresholdHumanRequired   = 0.65
	ThresholdProgressiveExit = 0.90
	MinProviders             = 2
)
