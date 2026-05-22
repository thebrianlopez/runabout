package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
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
	DimNextAction:     `Does the response correctly identify the single highest-priority next step in the delivery pipeline? Score 5 if: (1) the correct step number is explicitly named, AND (2) clear rationale is provided. Score 3 if the correct step is implied but not explicitly numbered. Score 1 if the wrong step is identified, no step is recommended, or output is incoherent.`,
	DimValidateRecall: `Does the response identify all critical violations present in the documents? Score 5 if all expected violations are named with specific artifact evidence. Score 3 if most violations caught but one is missed or evidence is vague. Score 1 if violations are missed, fabricated, or none are stated.`,
	DimIconAccuracy:   `Do the status icons in the response correctly reflect the artifact states shown in the documents? Score 5 if every artifact icon matches its expected state. Score 3 if more than 75% are correct. Score 1 if fewer than 50% are correct or no icons are present.`,
}

// judgeBaseURL is the HF Inference Providers OpenAI-compatible router base.
// Overridable in tests via httptest.
var judgeBaseURL = "https://router.huggingface.co/v1"

// reScore matches the last 1-5 integer in a judge response, tolerating labeled
// formats like "Score: 5", "score=4", markdown bold "**3**", and bare integers.
var reScore = regexp.MustCompile(`(?i)(?:score[=:\s]+)?\*{0,2}([1-5])\*{0,2}\s*$`)

// extractJudgeScore extracts a 1-5 integer score from a judge response that may
// be a bare integer, labeled ("Score: 5"), fenced, or explanatory with a trailing
// score. Returns an error only when no valid 1-5 integer can be found anywhere.
func extractJudgeScore(content string) (int, error) {
	// Fast path: bare integer.
	if n, err := strconv.Atoi(content); err == nil {
		if n >= 1 && n <= 5 {
			return n, nil
		}
		return 0, fmt.Errorf("integer %d out of 1-5 range", n)
	}
	// Labeled / embedded: scan first line, last line, then full content.
	for _, candidate := range []string{firstLine(content), lastLine(content), content} {
		if m := reScore.FindStringSubmatch(candidate); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				return n, nil
			}
		}
	}
	return 0, fmt.Errorf("no 1-5 score found in judge response")
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n\r ")
	if i := strings.LastIndexAny(s, "\n\r"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimLeft(s, "\n\r ")
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		return s[:i]
	}
	return s
}

// judgeScore calls an HF Inference Providers chat-completions judge and returns a
// normalized score 0.0-1.0 (1-5 → (score-1)/4). On error: returns 0.0, non-fatal.
func judgeScore(ctx context.Context, output string, dim Dimension) (float64, error) {
	apiKey := os.Getenv("HUGGINGFACE_API_KEY")
	if apiKey == "" {
		return 0.0, fmt.Errorf("HUGGINGFACE_API_KEY not set - judge disabled")
	}

	model := os.Getenv("HF_JUDGE_MODEL")
	if model == "" {
		model = "meta-llama/Llama-3.2-1B-Instruct"
	}

	rubric := judgeRubrics[dim]
	prompt := fmt.Sprintf("You are a fair evaluator. Rubric: %s\n\nResponse to evaluate:\n%s\n\nOutput a single integer from 1 to 5. Respond with only the integer.", rubric, output)

	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens": 20,
	})

	url := fmt.Sprintf("%s/chat/completions", strings.TrimRight(judgeBaseURL, "/"))
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

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0.0, fmt.Errorf("judge decode: %w", err)
	}
	if len(result.Choices) == 0 || strings.TrimSpace(result.Choices[0].Message.Content) == "" {
		return 0.0, fmt.Errorf("judge empty response")
	}

	content := strings.TrimSpace(result.Choices[0].Message.Content)
	score, err := extractJudgeScore(content)
	if err != nil {
		return 0.0, fmt.Errorf("judge parse %q: %w", content, err)
	}
	if score < 1 {
		score = 1
	}
	if score > 5 {
		score = 5
	}
	return float64(score-1) / 4.0, nil
}
