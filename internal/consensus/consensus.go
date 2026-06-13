package consensus

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// ModelConsensus runs L1 multi-provider consensus rounds.
type ModelConsensus interface {
	Run(ctx context.Context, req ConsensusRequest) (*ModelConsensusRound, error)
}

// EventBus receives events from consensus rounds. The M1 hash chain integration
// attaches prev_hash; implementations may be no-op stubs until M1 is deployed.
type EventBus interface {
	Append(ctx context.Context, eventType string, payload map[string]any) error
}

// noopBus is used when no event bus is wired in.
type noopBus struct{}

func (noopBus) Append(_ context.Context, _ string, _ map[string]any) error { return nil }

type modelConsensus struct {
	bus EventBus
}

// New returns a ModelConsensus. Pass nil for bus to skip event emission.
func New(bus EventBus) ModelConsensus {
	if bus == nil {
		bus = noopBus{}
	}
	return &modelConsensus{bus: bus}
}

// Run executes a full L1 consensus round across all configured providers.
func (mc *modelConsensus) Run(ctx context.Context, req ConsensusRequest) (*ModelConsensusRound, error) {
	if req.StabilityRounds < 1 {
		req.StabilityRounds = 1
	}

	consensusID := newULID()
	promptHash := fmt.Sprintf("%x", sha256.Sum256([]byte(req.Prompt)))

	_ = mc.bus.Append(ctx, "model_consensus_round_started", map[string]any{
		"consensus_id":   consensusID,
		"round_type":     req.RoundType,
		"provider_count": len(req.Providers),
		"schema_id":      req.SchemaID,
	})

	var lastRound *ModelConsensusRound
	for round := 0; round < req.StabilityRounds; round++ {
		r, err := mc.runSingleRound(ctx, req, consensusID, promptHash)
		if err != nil {
			return nil, err
		}
		if lastRound != nil && !roundsAgree(lastRound, r) {
			return nil, &ConsensusError{
				Code:    "MC-006",
				Class:   "stability_round_failed",
				Message: fmt.Sprintf("consensus unstable: round %d disagrees with round 1 on fields %v", round+1, r.DivergenceFlags),
			}
		}
		lastRound = r
	}

	_ = mc.bus.Append(ctx, "model_consensus_round_completed", map[string]any{
		"consensus_id":     consensusID,
		"agreement_score":  lastRound.AgreementScore,
		"resolution":       lastRound.Resolution,
		"divergence_flags": lastRound.DivergenceFlags,
	})

	return lastRound, nil
}

func (mc *modelConsensus) runSingleRound(ctx context.Context, req ConsensusRequest, id, promptHash string) (*ModelConsensusRound, error) {
	type result struct {
		pr  ProviderResult
		err error
	}

	n := len(req.Providers)
	ch := make(chan result, n)

	// Progressive quorum: cancel the last provider if N-1 agree at >= 0.90.
	provCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex
	completed := make([]ProviderResult, 0, n)
	var quorumTriggered bool

	var wg sync.WaitGroup
	for i, p := range req.Providers {
		wg.Add(1)
		go func(idx int, prov Provider) {
			defer wg.Done()
			start := time.Now()
			resp, err := prov.Complete(provCtx, CompletionRequest{
				Messages: []Message{
					{Role: "user", Content: req.Prompt},
				},
				Schema:      req.Schema,
				Temperature: 0,
			})
			pr := ProviderResult{
				Provider:  prov.Name(),
				Timestamp: time.Now(),
				LatencyMs: time.Since(start).Milliseconds(),
			}
			if err != nil {
				pr.Cancelled = provCtx.Err() != nil
				if !pr.Cancelled {
					_ = mc.bus.Append(ctx, "provider_unavailable", map[string]any{
						"consensus_id": id,
						"provider":     prov.Name(),
						"error_class":  "provider_unavailable",
					})
				}
				ch <- result{pr: pr, err: err}
				return
			}
			// Validate output is valid JSON.
			if !json.Valid(resp.Output) {
				_ = mc.bus.Append(ctx, "provider_unavailable", map[string]any{
					"consensus_id": id,
					"provider":     prov.Name(),
					"error_class":  "schema_validation_failed",
				})
				ch <- result{pr: pr, err: fmt.Errorf("provider %s returned invalid JSON", prov.Name())}
				return
			}
			pr.Output = resp.Output
			pr.Confidence = resp.Confidence
			pr.CostUSD = resp.CostUSD

			ch <- result{pr: pr}

			// Progressive quorum: check if N-1 providers agree at >= threshold.
			if idx < n-1 {
				mu.Lock()
				completed = append(completed, pr)
				if len(completed) >= n-1 && !quorumTriggered {
					score, _, _ := scoreAgreement(completed)
					if score >= ThresholdProgressiveExit {
						quorumTriggered = true
						cancel()
					}
				}
				mu.Unlock()
			}
		}(i, p)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var provResults []ProviderResult
	for r := range ch {
		pr := r.pr
		provResults = append(provResults, pr)
	}

	// Separate valid from failed/cancelled.
	valid := make([]ProviderResult, 0, len(provResults))
	for _, pr := range provResults {
		if !pr.Cancelled && pr.Output != nil {
			valid = append(valid, pr)
		}
	}

	if len(valid) < MinProviders {
		return nil, &ConsensusError{
			Code:    "MC-001",
			Class:   "insufficient_providers",
			Message: fmt.Sprintf("consensus requires ≥%d providers; only %d available", MinProviders, len(valid)),
		}
	}

	score, diverged, output := scoreAgreement(provResults) // includes cancelled=false only internally

	resolution := resolutionFromScore(score)

	return &ModelConsensusRound{
		ConsensusID:     id,
		RoundType:       req.RoundType,
		PromptHash:      promptHash,
		SchemaID:        req.SchemaID,
		Providers:       provResults,
		AgreementScore:  score,
		ConsensusOutput: output,
		DivergenceFlags: diverged,
		Resolution:      resolution,
		PrevHash:        req.PrevHash,
		Timestamp:       time.Now(),
	}, nil
}

func resolutionFromScore(score float64) string {
	switch {
	case score >= ThresholdConsensus:
		return ResolutionConsensus
	case score >= ThresholdHumanRequired:
		return ResolutionDiverged
	default:
		return ResolutionHumanRequired
	}
}

func roundsAgree(a, b *ModelConsensusRound) bool {
	return a.Resolution == b.Resolution && len(a.DivergenceFlags) == len(b.DivergenceFlags)
}

// ConsensusError is a structured consensus error with a machine-readable code.
type ConsensusError struct {
	Code    string
	Class   string
	Message string
}

func (e *ConsensusError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", e.Code, e.Class, e.Message)
}
