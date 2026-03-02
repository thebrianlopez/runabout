package perfgate

import (
	"encoding/json"
	"fmt"
	"os"
)

// GateConfig defines thresholds for a performance gate.
type GateConfig struct {
	MaxRegression  float64
	MinImprovement float64
}

// GateResult holds the outcome of a performance gate evaluation.
type GateResult struct {
	Passed     bool
	Regression float64
	Message    string
}

// Evaluate compares current stats against a baseline using the gate config.
func Evaluate(baseline, current Stats, cfg GateConfig) GateResult {
	if baseline.Mean == 0 {
		return GateResult{
			Passed:  true,
			Message: "baseline mean is zero, skipping comparison",
		}
	}

	regression := (current.Mean - baseline.Mean) / baseline.Mean * 100

	if regression > cfg.MaxRegression {
		return GateResult{
			Passed:     false,
			Regression: regression,
			Message:    fmt.Sprintf("FAIL: %.2f%% regression exceeds max allowed %.2f%%", regression, cfg.MaxRegression),
		}
	}

	if cfg.MinImprovement > 0 {
		improvement := -regression
		if improvement < cfg.MinImprovement {
			return GateResult{
				Passed:     false,
				Regression: regression,
				Message:    fmt.Sprintf("FAIL: %.2f%% improvement below required %.2f%%", improvement, cfg.MinImprovement),
			}
		}
	}

	return GateResult{
		Passed:     true,
		Regression: regression,
		Message:    fmt.Sprintf("PASS: %.2f%% change (max allowed: %.2f%%)", regression, cfg.MaxRegression),
	}
}

// SaveResult marshals a RunResult to a JSON file.
func SaveResult(path string, result *RunResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// LoadResult reads a RunResult from a JSON file.
func LoadResult(path string) (*RunResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var result RunResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &result, nil
}
