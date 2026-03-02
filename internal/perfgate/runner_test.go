package perfgate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunEchoCommand(t *testing.T) {
	result, err := Run(RunConfig{Cmd: "echo hello", Runs: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Samples) != 3 {
		t.Errorf("expected 3 samples, got %d", len(result.Samples))
	}
	if result.Stats.N != 3 {
		t.Errorf("expected Stats.N=3, got %d", result.Stats.N)
	}
	for i, s := range result.Samples {
		if s.ExitCode != 0 {
			t.Errorf("sample %d: expected exit code 0, got %d", i, s.ExitCode)
		}
		if s.Duration <= 0 {
			t.Errorf("sample %d: expected positive duration, got %f", i, s.Duration)
		}
	}
}

func TestRunWithWarmup(t *testing.T) {
	result, err := Run(RunConfig{Cmd: "true", Runs: 2, Warmup: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Samples) != 2 {
		t.Errorf("expected 2 samples (warmup excluded), got %d", len(result.Samples))
	}
}

func TestRunSavesResults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")

	result, err := Run(RunConfig{Cmd: "echo test", Runs: 2, SaveTo: path})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected saved file at %s: %v", path, err)
	}

	loaded, err := LoadResult(path)
	if err != nil {
		t.Fatalf("LoadResult: %v", err)
	}
	if loaded.Stats.Mean != result.Stats.Mean {
		t.Errorf("loaded mean %f != result mean %f", loaded.Stats.Mean, result.Stats.Mean)
	}
}

func TestRunEmptyCmd(t *testing.T) {
	_, err := Run(RunConfig{Cmd: "", Runs: 1})
	if err == nil {
		t.Error("expected error for empty cmd")
	}
}

func TestRunZeroRuns(t *testing.T) {
	_, err := Run(RunConfig{Cmd: "echo hello", Runs: 0})
	if err == nil {
		t.Error("expected error for zero runs")
	}
}

func TestRunFailingCommand(t *testing.T) {
	result, err := Run(RunConfig{Cmd: "exit 1", Runs: 2})
	if err != nil {
		t.Fatalf("Run: %v (failing commands should still collect samples)", err)
	}
	for i, s := range result.Samples {
		if s.ExitCode != 1 {
			t.Errorf("sample %d: expected exit code 1, got %d", i, s.ExitCode)
		}
	}
}

func TestRunConfigPreserved(t *testing.T) {
	cfg := RunConfig{Cmd: "echo test", Runs: 3, Warmup: 1}
	result, err := Run(cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Config.Cmd != cfg.Cmd {
		t.Errorf("Config.Cmd: got %q, want %q", result.Config.Cmd, cfg.Cmd)
	}
	if result.Config.Runs != cfg.Runs {
		t.Errorf("Config.Runs: got %d, want %d", result.Config.Runs, cfg.Runs)
	}
}
