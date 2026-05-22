package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// ResultRow is one scored fixture result, written locally as JSONL and pushed to HF Hub.
type ResultRow struct {
	RunID          string  `json:"run_id"`
	Fixture        string  `json:"fixture"`
	Command        string  `json:"command"`
	NextAction     float64 `json:"next_action"`
	ValidateRecall float64 `json:"validate_recall"`
	IconAccuracy   float64 `json:"icon_accuracy"`
	JudgeInvoked   bool    `json:"judge_invoked"`
	PassThreshold  bool    `json:"pass_threshold"`
	ScoredAt       string  `json:"scored_at"`
}

// hubBaseURL is the HF Hub API base. Overridable in tests via httptest.
var hubBaseURL = "https://huggingface.co"

// hubPush appends a JSONL row to the HF Hub dataset. Non-fatal: errors are logged by
// the caller; the CI gate exit code is never affected by push failures.
func hubPush(ctx context.Context, row ResultRow) error {
	repo := os.Getenv("HF_DATASET_REPO")
	if repo == "" {
		return nil // not configured, skip silently
	}
	apiKey := os.Getenv("HUGGINGFACE_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("HUGGINGFACE_API_KEY not set")
	}

	line, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("hub marshal: %w", err)
	}

	payload := map[string]any{
		"summary": fmt.Sprintf("chain-eval %s %s", row.RunID, row.Fixture),
		"files": []map[string]any{
			{
				"path":     "results.jsonl",
				"content":  string(line) + "\n",
				"encoding": "utf-8",
			},
		},
	}
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/api/datasets/%s/commit/main", hubBaseURL, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("hub request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("hub API: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("hub API %d: %s", resp.StatusCode, limitedBody(resp))
	}
	return nil
}

func limitedBody(resp *http.Response) string {
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return err.Error()
	}
	return string(data)
}
