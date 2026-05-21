package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Dimension identifies one of the three scoring axes.
type Dimension string

const (
	DimNextAction     Dimension = "next_action"
	DimValidateRecall Dimension = "validate_recall"
	DimIconAccuracy   Dimension = "icon_accuracy"
)

var judgeRubrics = map[Dimension]string{
	DimNextAction: `Does the response correctly identify the single highest-priority next step in the delivery pipeline? Score 5 if: (1) the correct step number is explicitly named, AND (2) clear rationale is provided. Score 3 if the correct step is implied but not explicitly numbered. Score 1 if the wrong step is identified, no step is recommended, or output is incoherent.`,
	DimValidateRecall: `Does the response identify all critical violations present in the documents? Score 5 if all expected violations are named with specific artifact evidence. Score 3 if most violations caught but one is missed or evidence is vague. Score 1 if violations are missed, fabricated, or none are stated.`,
	DimIconAccuracy: `Do the status icons in the response correctly reflect the artifact states shown in the documents? Score 5 if every artifact icon matches its expected state. Score 3 if more than 75% are correct. Score 1 if fewer than 50% are correct or no icons are present.`,
}

// judgeBaseURL is the HF Inference API base. Overridable in tests via httptest.
var judgeBaseURL = "https://api-inference.huggingface.co"

// judgeScore calls the Prometheus judge via HF Inference API and returns a normalized
// score 0.0-1.0 (Prometheus 1-5 → (score-1)/4). On error: returns 0.0, non-fatal.
func judgeScore(ctx context.Context, output string, dim Dimension) (float64, error) {
	apiKey := os.Getenv("HUGGINGFACE_API_KEY")
	if apiKey == "" {
		return 0.0, fmt.Errorf("HUGGINGFACE_API_KEY not set - judge disabled")
	}

	model := os.Getenv("HF_JUDGE_MODEL")
	if model == "" {
		model = "prometheus-eval/prometheus-7b-v2.0"
	}

	rubric := judgeRubrics[dim]
	prompt := fmt.Sprintf("[INST] You are a fair evaluator. Rubric: %s\n\nResponse to evaluate:\n%s\n\nOutput a single integer from 1 to 5. Respond with only the integer. [/INST]", rubric, output)

	body, _ := json.Marshal(map[string]any{
		"inputs": prompt,
		"parameters": map[string]any{
			"max_new_tokens":  5,
			"return_full_text": false,
		},
	})

	url := fmt.Sprintf("%s/models/%s", judgeBaseURL, model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0.0, fmt.Errorf("judge request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0.0, fmt.Errorf("judge API: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return 0.0, fmt.Errorf("judge API %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var result []struct {
		GeneratedText string `json:"generated_text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0.0, fmt.Errorf("judge decode: %w", err)
	}
	if len(result) == 0 || strings.TrimSpace(result[0].GeneratedText) == "" {
		return 0.0, fmt.Errorf("judge empty response")
	}

	var score int
	if _, err := fmt.Sscanf(strings.TrimSpace(result[0].GeneratedText), "%d", &score); err != nil {
		return 0.0, fmt.Errorf("judge parse %q: %w", result[0].GeneratedText, err)
	}
	if score < 1 {
		score = 1
	}
	if score > 5 {
		score = 5
	}
	return float64(score-1) / 4.0, nil
}
