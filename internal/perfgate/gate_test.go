package perfgate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluatePass(t *testing.T) {
	baseline := Stats{Mean: 1.0}
	current := Stats{Mean: 1.03} // 3% regression
	cfg := GateConfig{MaxRegression: 5.0}

	result := Evaluate(baseline, current, cfg)
	if !result.Passed {
		t.Errorf("expected pass, got fail: %s", result.Message)
	}
}

func TestEvaluateFailRegression(t *testing.T) {
	baseline := Stats{Mean: 1.0}
	current := Stats{Mean: 1.10} // 10% regression
	cfg := GateConfig{MaxRegression: 5.0}

	result := Evaluate(baseline, current, cfg)
	if result.Passed {
		t.Errorf("expected fail, got pass: %s", result.Message)
	}
	if result.Regression < 9.9 || result.Regression > 10.1 {
		t.Errorf("expected ~10%% regression, got %.2f%%", result.Regression)
	}
}

func TestEvaluateImprovement(t *testing.T) {
	baseline := Stats{Mean: 1.0}
	current := Stats{Mean: 0.8} // 20% improvement
	cfg := GateConfig{MaxRegression: 5.0}

	result := Evaluate(baseline, current, cfg)
	if !result.Passed {
		t.Errorf("expected pass, got fail: %s", result.Message)
	}
	if result.Regression > -19.9 || result.Regression < -20.1 {
		t.Errorf("expected ~-20%% regression, got %.2f%%", result.Regression)
	}
}

func TestEvaluateMinImprovementFail(t *testing.T) {
	baseline := Stats{Mean: 1.0}
	current := Stats{Mean: 0.95} // 5% improvement, but need 10%
	cfg := GateConfig{MaxRegression: 5.0, MinImprovement: 10.0}

	result := Evaluate(baseline, current, cfg)
	if result.Passed {
		t.Errorf("expected fail for insufficient improvement, got pass: %s", result.Message)
	}
}

func TestEvaluateMinImprovementPass(t *testing.T) {
	baseline := Stats{Mean: 1.0}
	current := Stats{Mean: 0.85} // 15% improvement, need 10%
	cfg := GateConfig{MaxRegression: 5.0, MinImprovement: 10.0}

	result := Evaluate(baseline, current, cfg)
	if !result.Passed {
		t.Errorf("expected pass, got fail: %s", result.Message)
	}
}

func TestEvaluateZeroBaseline(t *testing.T) {
	baseline := Stats{Mean: 0}
	current := Stats{Mean: 1.0}
	cfg := GateConfig{MaxRegression: 5.0}

	result := Evaluate(baseline, current, cfg)
	if !result.Passed {
		t.Errorf("expected pass on zero baseline, got fail: %s", result.Message)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")

	original := &RunResult{
		Config: RunConfig{Cmd: "echo hello", Runs: 3, Warmup: 1},
		Samples: []Sample{
			{Index: 1, Duration: 0.01, ExitCode: 0},
			{Index: 2, Duration: 0.02, ExitCode: 0},
			{Index: 3, Duration: 0.015, ExitCode: 0},
		},
		Stats: Compute([]float64{0.01, 0.02, 0.015}),
	}

	if err := SaveResult(path, original); err != nil {
		t.Fatalf("SaveResult: %v", err)
	}

	loaded, err := LoadResult(path)
	if err != nil {
		t.Fatalf("LoadResult: %v", err)
	}

	if loaded.Config.Cmd != original.Config.Cmd {
		t.Errorf("Cmd: got %q, want %q", loaded.Config.Cmd, original.Config.Cmd)
	}
	if loaded.Stats.N != original.Stats.N {
		t.Errorf("Stats.N: got %d, want %d", loaded.Stats.N, original.Stats.N)
	}
	if loaded.Stats.Mean != original.Stats.Mean {
		t.Errorf("Stats.Mean: got %f, want %f", loaded.Stats.Mean, original.Stats.Mean)
	}
	if len(loaded.Samples) != len(original.Samples) {
		t.Errorf("Samples len: got %d, want %d", len(loaded.Samples), len(original.Samples))
	}
}

func TestLoadResultNotFound(t *testing.T) {
	_, err := LoadResult("/nonexistent/path.json")
	if err == nil {
		t.Error("expected error loading nonexistent file")
	}
}

func TestSaveResultBadPath(t *testing.T) {
	err := SaveResult("/nonexistent/dir/result.json", &RunResult{})
	if err == nil {
		t.Error("expected error saving to bad path")
	}
}

func TestLoadResultInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	os.WriteFile(path, []byte("{invalid"), 0o644)

	_, err := LoadResult(path)
	if err == nil {
		t.Error("expected error loading invalid JSON")
	}
}
