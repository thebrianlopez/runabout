package consensus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAIProvider implements Provider using an OpenAI-compatible chat completions endpoint.
// This covers OpenAI, Google (Gemini via OpenAI compatibility), and other compatible APIs.
type OpenAIProvider struct {
	name       string
	baseURL    string
	apiKey     string // never logged
	modelID    string
	httpClient *http.Client
}

// NewOpenAIProvider returns a Provider for any OpenAI-compatible endpoint.
// name is used as the provider name in results (e.g., "openai", "google").
func NewOpenAIProvider(name, baseURL, apiKey, modelID string) Provider {
	return &OpenAIProvider{
		name:       name,
		baseURL:    baseURL,
		apiKey:     apiKey,
		modelID:    modelID,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

func (p *OpenAIProvider) Name() string { return p.name }

type openAIRequest struct {
	Model          string            `json:"model"`
	Messages       []openAIMessage   `json:"messages"`
	Temperature    float64           `json:"temperature"`
	ResponseFormat *openAIRespFormat `json:"response_format,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRespFormat struct {
	Type string `json:"type"` // "json_object"
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (p *OpenAIProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	msgs := make([]openAIMessage, 0, len(req.Messages)+1)

	// Inject system instruction for JSON output when schema present.
	if req.Schema != nil {
		msgs = append(msgs, openAIMessage{
			Role:    "system",
			Content: fmt.Sprintf("Respond only with valid JSON matching this schema: %s", string(req.Schema)),
		})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, openAIMessage{Role: m.Role, Content: m.Content})
	}

	body := openAIRequest{
		Model:       p.modelID,
		Messages:    msgs,
		Temperature: req.Temperature,
	}
	if req.Schema != nil {
		body.ResponseFormat = &openAIRespFormat{Type: "json_object"}
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	start := time.Now()
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, &ConsensusError{
			Code:    "MC-002",
			Class:   "provider_unavailable",
			Message: fmt.Sprintf("provider %s unavailable: %v", p.name, err),
		}
	}
	defer resp.Body.Close()
	latency := time.Since(start).Milliseconds()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return CompletionResponse{}, &ConsensusError{
			Code:    "MC-002",
			Class:   "provider_unavailable",
			Message: fmt.Sprintf("provider %s HTTP %d: %s", p.name, resp.StatusCode, string(b)),
		}
	}

	var oResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oResp); err != nil {
		return CompletionResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if len(oResp.Choices) == 0 {
		return CompletionResponse{}, fmt.Errorf("provider %s returned no choices", p.name)
	}

	content := oResp.Choices[0].Message.Content
	output, err := extractJSON(content)
	if err != nil {
		return CompletionResponse{}, &ConsensusError{
			Code:    "MC-003",
			Class:   "schema_validation_failed",
			Message: fmt.Sprintf("provider %s returned non-JSON: %v", p.name, err),
		}
	}

	confidence := 0.85
	if oResp.Choices[0].FinishReason == "stop" {
		confidence = 0.90
	}

	return CompletionResponse{
		Output:     output,
		Confidence: confidence,
		LatencyMs:  latency,
	}, nil
}
