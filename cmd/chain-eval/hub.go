package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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

// hubPushBatch writes all scored rows to the HF Hub dataset as a single commit.
// One commit per run regardless of fixture count, avoiding the 128 commits/hr
// free-tier rate limit that per-row pushes would exhaust. Non-fatal: errors are
// logged by the caller; the CI gate exit code is never affected by push failures.
func hubPushBatch(ctx context.Context, rows []ResultRow) error {
	if len(rows) == 0 {
		return nil
	}
	repo := os.Getenv("HF_DATASET_REPO")
	if repo == "" {
		return nil // not configured, skip silently
	}
	apiKey := os.Getenv("HUGGINGFACE_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("HUGGINGFACE_API_KEY not set")
	}

	var buf bytes.Buffer
	for _, row := range rows {
		line, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("hub marshal: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	// Use the run_id from the first row as the commit summary (all rows share one run).
	runID := rows[0].RunID
	payload := map[string]any{
		"summary": fmt.Sprintf("chain-eval %s (%d fixtures)", runID, len(rows)),
		"files": []map[string]any{
			{
				"path":     "results.jsonl",
				"content":  buf.String(),
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

// hubPushBucket writes all scored rows to an HF Bucket using the `hf` CLI.
// Target path: hf://buckets/{bucket}/{runID}/results.jsonl
// One write per run at a unique timestamped path — no commit rate limit,
// historical runs accumulate. Requires `hf` CLI on PATH and HUGGINGFACE_API_KEY
// (or prior `hf auth login`). Non-fatal: errors logged by caller.
func hubPushBucket(ctx context.Context, rows []ResultRow, bucket string) error {
	if len(rows) == 0 || bucket == "" {
		return nil
	}

	var buf bytes.Buffer
	for _, row := range rows {
		line, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("bucket marshal: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	runID := rows[0].RunID
	dst := fmt.Sprintf("hf://buckets/%s/%s/results.jsonl", bucket, runID)

	cmd := exec.CommandContext(ctx, "hf", "buckets", "cp", "-", dst)
	cmd.Stdin = &buf
	if apiKey := os.Getenv("HUGGINGFACE_API_KEY"); apiKey != "" {
		cmd.Env = append(os.Environ(), "HF_TOKEN="+apiKey)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("hf buckets cp: %w: %s", err, string(out))
	}
	return nil
}

// bucketRunURL returns the canonical URL for a run's results in the bucket.
func bucketRunURL(bucket, runID string) string {
	return fmt.Sprintf("https://huggingface.co/buckets/%s/%s/results.jsonl", bucket, runID)
}

func limitedBody(resp *http.Response) string {
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return err.Error()
	}
	return string(data)
}
