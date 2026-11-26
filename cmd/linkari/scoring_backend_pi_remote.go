package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// piRemoteDefaultTimeout is used when PiRemoteScoringBackend.Client is nil.
// No existing timeout convention was found elsewhere in the scoring backend
// files (PiScoringBackend relies on ctx cancellation from the caller), so
// this mirrors the common HTTP client default of 60s.
const piRemoteDefaultTimeout = 60 * time.Second

// errPiRemoteRequestFailed is the sentinel wrapped into the "pi_remote: %w
// (stderr=%s)" error when the remote service reports a non-empty error field
// in an otherwise-200 response.
var errPiRemoteRequestFailed = errors.New("pi_remote request failed")

// PiRemoteScoringBackend implements ScoringBackend by delegating single-turn
// LLM calls to a remote HTTP service that wraps the pi CLI, instead of
// exec.Command against a locally installed pi binary (see
// PiScoringBackend). This is for use when linkari runs in a container that
// cannot bundle the pi CLI's Node.js runtime.
//
// Endpoints (relative to BaseURL):
//
//	POST /v1/complete         {"system_prompt", "content"}
//	POST /v1/complete-json    {"system_prompt", "content", "schema"}
//	POST /v1/complete-vision  {"system_prompt", "content", "schema", "image_base64"}
//
// Response body (all endpoints): {"stdout": string, "error": string}. A
// non-200 status or a non-empty "error" field is treated as a failure.
type PiRemoteScoringBackend struct {
	BaseURL string       // base URL of the pi-service, e.g. http://pi-service.linkari.svc.cluster.local:9090
	Client  *http.Client // nil defaults to a client with piRemoteDefaultTimeout
}

func (b PiRemoteScoringBackend) Name() string { return "pi_remote" }

// piRemoteCompleteRequest is the shared JSON request body across all three
// endpoints; fields not applicable to a given endpoint are omitted.
type piRemoteCompleteRequest struct {
	SystemPrompt string `json:"system_prompt"`
	Content      string `json:"content"`
	Schema       string `json:"schema,omitempty"`
	ImageBase64  string `json:"image_base64,omitempty"`
}

// piRemoteResponse is the shared JSON response body across all three
// endpoints.
type piRemoteResponse struct {
	Stdout string `json:"stdout"`
	Error  string `json:"error"`
}

func (b PiRemoteScoringBackend) httpClient() *http.Client {
	if b.Client != nil {
		return b.Client
	}
	return &http.Client{Timeout: piRemoteDefaultTimeout}
}

// post sends reqBody as JSON to BaseURL+path and returns the decoded
// response. Non-200 statuses and non-empty response error fields are both
// surfaced as errors, matching PiScoringBackend's "pi exec: %w (stderr=%s)"
// convention with the response error field standing in for stderr.
func (b PiRemoteScoringBackend) post(ctx context.Context, path string, reqBody piRemoteCompleteRequest) (piRemoteResponse, error) {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return piRemoteResponse{}, fmt.Errorf("pi_remote: marshal request: %w", err)
	}

	url := strings.TrimRight(b.BaseURL, "/") + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return piRemoteResponse{}, fmt.Errorf("pi_remote: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := b.httpClient().Do(httpReq)
	if err != nil {
		return piRemoteResponse{}, fmt.Errorf("pi_remote: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return piRemoteResponse{}, fmt.Errorf("pi_remote: read response body: %w", err)
	}

	var result piRemoteResponse
	if jsonErr := json.Unmarshal(bodyBytes, &result); jsonErr != nil {
		return piRemoteResponse{}, fmt.Errorf("pi_remote: %w (stderr=%s)", jsonErr, strings.TrimSpace(string(bodyBytes)))
	}

	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
		return piRemoteResponse{}, fmt.Errorf("pi_remote: %w (stderr=%s)", statusErr, strings.TrimSpace(result.Error))
	}
	if result.Error != "" {
		return piRemoteResponse{}, fmt.Errorf("pi_remote: %w (stderr=%s)", errPiRemoteRequestFailed, strings.TrimSpace(result.Error))
	}
	return result, nil
}

// Complete sends systemPrompt + content to the remote pi-service and returns
// the trimmed text response.
func (b PiRemoteScoringBackend) Complete(ctx context.Context, systemPrompt, content string) (string, error) {
	result, err := b.post(ctx, "/v1/complete", piRemoteCompleteRequest{
		SystemPrompt: systemPrompt,
		Content:      content,
	})
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(result.Stdout)
	if out == "" {
		return "", fmt.Errorf("pi_remote returned empty output")
	}
	return out, nil
}

// CompleteJSON sends systemPrompt + content + schema to the remote
// pi-service and returns the raw response bytes, same contract as
// PiScoringBackend.CompleteJSON (trimmed stdout bytes).
func (b PiRemoteScoringBackend) CompleteJSON(ctx context.Context, systemPrompt, content, schema string) ([]byte, error) {
	result, err := b.post(ctx, "/v1/complete-json", piRemoteCompleteRequest{
		SystemPrompt: systemPrompt,
		Content:      content,
		Schema:       schema,
	})
	if err != nil {
		return nil, err
	}
	out := bytes.TrimSpace([]byte(result.Stdout))
	if len(out) == 0 {
		return nil, fmt.Errorf("pi_remote returned empty output")
	}
	return out, nil
}

// CompleteVision reads the image at imagePath, base64-encodes it, and sends
// it alongside systemPrompt/textContent/schema to the remote pi-service.
func (b PiRemoteScoringBackend) CompleteVision(ctx context.Context, systemPrompt, textContent, imagePath, schema string) ([]byte, error) {
	imgBytes, err := os.ReadFile(imagePath)
	if err != nil {
		return nil, fmt.Errorf("pi_remote vision: image file not readable: %w", err)
	}

	result, err := b.post(ctx, "/v1/complete-vision", piRemoteCompleteRequest{
		SystemPrompt: systemPrompt,
		Content:      textContent,
		Schema:       schema,
		ImageBase64:  base64.StdEncoding.EncodeToString(imgBytes),
	})
	if err != nil {
		return nil, err
	}
	out := bytes.TrimSpace([]byte(result.Stdout))
	if len(out) == 0 {
		return nil, fmt.Errorf("pi_remote returned empty output")
	}
	return out, nil
}
