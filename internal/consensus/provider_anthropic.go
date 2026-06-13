package consensus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicProvider implements Provider using the Anthropic Messages API.
type AnthropicProvider struct {
	client  anthropic.Client
	modelID string
}

// NewAnthropicProvider returns a Provider backed by Anthropic's Claude.
// apiKey must not appear in log events; it is used only for authentication.
func NewAnthropicProvider(apiKey, modelID string) Provider {
	if modelID == "" {
		modelID = "claude-haiku-4-5-20251001"
	}
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &AnthropicProvider{client: client, modelID: modelID}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

func (p *AnthropicProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	msgs := make([]anthropic.MessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case "assistant":
			msgs = append(msgs, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		}
	}

	// Request JSON output via system prompt when a schema is provided.
	system := ""
	if req.Schema != nil {
		system = fmt.Sprintf("Respond only with valid JSON matching this schema: %s", string(req.Schema))
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.modelID),
		MaxTokens: 1024,
		Messages:  msgs,
	}
	if system != "" {
		params.System = []anthropic.TextBlockParam{{Text: system}}
	}

	start := time.Now()
	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("anthropic provider: %w", err)
	}

	// Extract text content from the first content block.
	var text string
	for _, block := range resp.Content {
		if block.Type == "text" {
			text = block.Text
			break
		}
	}

	output, err := extractJSON(text)
	if err != nil {
		return CompletionResponse{}, &ConsensusError{
			Code:    "MC-003",
			Class:   "schema_validation_failed",
			Message: fmt.Sprintf("anthropic provider returned non-JSON output: %v", err),
		}
	}

	// Anthropic doesn't return explicit confidence; derive from stop reason.
	confidence := 0.85
	if resp.StopReason == anthropic.StopReasonEndTurn {
		confidence = 0.90
	}

	// Approximate cost using input+output token counts.
	inputTokens := int64(resp.Usage.InputTokens)
	outputTokens := int64(resp.Usage.OutputTokens)
	costUSD := estimateAnthropicCost(p.modelID, inputTokens, outputTokens)

	_ = time.Since(start)

	return CompletionResponse{
		Output:     output,
		Confidence: confidence,
		LatencyMs:  time.Since(start).Milliseconds(),
		CostUSD:    costUSD,
	}, nil
}

// extractJSON finds the first JSON object or array in text.
func extractJSON(text string) (json.RawMessage, error) {
	for i, c := range text {
		if c == '{' || c == '[' {
			candidate := text[i:]
			// Find the matching close bracket by scanning.
			b := []byte(candidate)
			if json.Valid(b) {
				return json.RawMessage(b), nil
			}
			// Try trimming trailing non-JSON content.
			for j := len(b); j > 0; j-- {
				if b[j-1] == '}' || b[j-1] == ']' {
					if json.Valid(b[:j]) {
						return json.RawMessage(b[:j]), nil
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("no JSON found in response")
}

func estimateAnthropicCost(modelID string, input, output int64) float64 {
	// Approximate pricing per million tokens (MTok) as of 2026-06.
	var inputMTok, outputMTok float64
	switch modelID {
	case "claude-opus-4-8":
		inputMTok, outputMTok = 15.0, 75.0
	case "claude-sonnet-4-6":
		inputMTok, outputMTok = 3.0, 15.0
	default: // haiku
		inputMTok, outputMTok = 0.80, 4.0
	}
	return float64(input)/1e6*inputMTok + float64(output)/1e6*outputMTok
}
