package perfgate

import (
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// RunConfig defines how to run a benchmark.
type RunConfig struct {
	Cmd    string
	Runs   int
	Warmup int
	SaveTo string
}

// RunResult holds the outcome of a benchmark run.
type RunResult struct {
	Config  RunConfig
	Samples []Sample
	Stats   Stats
}

// Sample holds timing data for a single run.
type Sample struct {
	Index    int
	Duration float64
	ExitCode int
}

// Run executes cfg.Cmd cfg.Runs times and collects timing samples.
func Run(cfg RunConfig) (*RunResult, error) {
	if cfg.Cmd == "" {
		return nil, fmt.Errorf("cmd must not be empty")
	}
	if cfg.Runs <= 0 {
		return nil, fmt.Errorf("runs must be > 0")
	}

	// Warmup iterations (discard results)
	for i := 0; i < cfg.Warmup; i++ {
		cmd := exec.Command("sh", "-c", cfg.Cmd)
		_ = cmd.Run()
	}

	// Timed iterations
	samples := make([]Sample, 0, cfg.Runs)
	durations := make([]float64, 0, cfg.Runs)

	for i := 0; i < cfg.Runs; i++ {
		cmd := exec.Command("sh", "-c", cfg.Cmd)
		start := time.Now()
		err := cmd.Run()
		elapsed := time.Since(start).Seconds()

		exitCode := 0
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				return nil, fmt.Errorf("run %d: %w", i+1, err)
			}
		}

		samples = append(samples, Sample{
			Index:    i + 1,
			Duration: elapsed,
			ExitCode: exitCode,
		})
		durations = append(durations, elapsed)
	}

	stats := Compute(durations)

	result := &RunResult{
		Config:  cfg,
		Samples: samples,
		Stats:   stats,
	}

	if cfg.SaveTo != "" {
		if err := SaveResult(cfg.SaveTo, result); err != nil {
			return result, fmt.Errorf("save results: %w", err)
		}
	}

	return result, nil
}
