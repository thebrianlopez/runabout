package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// mockJudge creates an httptest server that returns the given judge score (1-5).
// Caller must defer srv.Close().
func mockJudge(t *testing.T, score int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("judge path = %s, want /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": string(rune('0' + score))}}}}) //nolint:errcheck
	}))
	t.Setenv("HUGGINGFACE_API_KEY", "test-key")
	judgeBaseURL = srv.URL
	t.Cleanup(func() { judgeBaseURL = "https://router.huggingface.co/v1" })
	return srv
}

// mockJudgeError creates an httptest server that returns HTTP 500.
func mockJudgeError(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	t.Setenv("HUGGINGFACE_API_KEY", "test-key")
	judgeBaseURL = srv.URL
	t.Cleanup(func() { judgeBaseURL = "https://router.huggingface.co/v1" })
	return srv
}

// CT-11: deterministic FAIL triggers judge; mock returns 4 → score 0.75.
func TestScoreNextAction_DeterministicFailCallsJudge(t *testing.T) {
	srv := mockJudge(t, 4)
	defer srv.Close()

	r := TaskResult{
		Input:  ChainInput{Fixture: "ct11", Expected: ChainExpected{PriorityStep: 6}},
		Output: "No clear next step identified in the pipeline.", // no "step 6"
	}
	score, judgeInvoked := scoreNextAction(context.Background(), r)
	if !judgeInvoked {
		t.Error("CT-11: judge must be invoked when deterministic check fails")
	}
	want := float64(4-1) / 4.0 // 0.75
	if score != want {
		t.Errorf("CT-11: want %.2f (Prometheus 4→0.75), got %.2f", want, score)
	}
}

// CT-12: Prometheus 1→0.0, 3→0.5, 5→1.0 normalization.
func TestJudgeScoreNormalization(t *testing.T) {
	cases := []struct {
		prometheus int
		want       float64
	}{
		{1, 0.0},
		{3, 0.5},
		{5, 1.0},
	}
	for _, tc := range cases {
		srv := mockJudge(t, tc.prometheus)
		got, err := judgeScore(context.Background(), "some output", DimNextAction)
		srv.Close()
		if err != nil {
			t.Fatalf("CT-12 (prometheus=%d): unexpected error: %v", tc.prometheus, err)
		}
		if got != tc.want {
			t.Errorf("CT-12 (prometheus=%d): want %.2f, got %.2f", tc.prometheus, tc.want, got)
		}
	}
}

// CT-13: judge_api_error → score 0.0, non-fatal (no panic, no os.Exit).
func TestScoreNextAction_JudgeAPIError(t *testing.T) {
	srv := mockJudgeError(t)
	defer srv.Close()

	r := TaskResult{
		Input:  ChainInput{Fixture: "ct13", Expected: ChainExpected{PriorityStep: 2}},
		Output: "The chain looks complete already.", // no "step 2" → judge attempted
	}
	score, judgeInvoked := scoreNextAction(context.Background(), r)
	if score != 0.0 {
		t.Errorf("CT-13: judge error must score 0.0, got %.2f", score)
	}
	if !judgeInvoked {
		t.Error("CT-13: judgeInvoked must be true when judge was attempted")
	}
}

// CT-14: hub_push_error is non-fatal  -  hubPushBatch returns error, does not panic or exit.
// Also validates batch contract: N rows produce exactly 1 HTTP request to the Hub API.
func TestHubPush_ErrorNonFatal(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	t.Setenv("HUGGINGFACE_API_KEY", "test-key")
	t.Setenv("HF_DATASET_REPO", "owner/chain-eval-results")
	hubBaseURL = srv.URL
	t.Cleanup(func() { hubBaseURL = "https://huggingface.co" })

	rows := []ResultRow{
		{RunID: "test-run", Fixture: "pomo_pending", Command: "/chain next"},
		{RunID: "test-run", Fixture: "release_gate_violation", Command: "/chain validate"},
		{RunID: "test-run", Fixture: "tdd_approved_no_epic", Command: "/chain next"},
	}
	err := hubPushBatch(context.Background(), rows)
	if err == nil {
		t.Error("CT-14: hub push to failing server should return error")
	}
	// Batch contract: 3 rows must produce exactly 1 HTTP request.
	if requests != 1 {
		t.Errorf("CT-14: want 1 HTTP request for %d rows, got %d", len(rows), requests)
	}
	// Non-fatal contract: error returned, no panic, no os.Exit - test reaches here.
}

// CT-16: FlowBench-adapted fixture converts to valid ChainInput schema.
// Validates: command non-empty, fixture non-empty, at least one expected field set,
// and loadFixture on the fixture dir returns non-empty docs_state.
func TestFlowBenchFixtureSchema(t *testing.T) {
	// Simulate a flowbench_adapted fixture directory with a docs-state file.
	dir := t.TempDir()
	fixtureName := "flowbench_workflow_planning_01"
	fixtureDir := filepath.Join(dir, fixtureName)
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# Chain State\n\nPRD: Approved\nFDD: Approved\nTDD: Approved\nEpic: missing\n"
	if err := os.WriteFile(filepath.Join(fixtureDir, "state.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Build the ChainInput as a flowbench-adapted fixture would.
	input := ChainInput{
		Command: "/chain next",
		Fixture: fixtureName,
		Expected: ChainExpected{
			PriorityStep: 6,
		},
	}

	// Validate schema: non-empty command, fixture name, at least one expected field.
	if input.Command == "" {
		t.Error("CT-16: FlowBench fixture must have non-empty command")
	}
	if input.Fixture == "" {
		t.Error("CT-16: FlowBench fixture must have non-empty fixture name")
	}
	if input.Expected.PriorityStep == 0 && len(input.Expected.Violations) == 0 && len(input.Expected.IconMap) == 0 {
		t.Error("CT-16: FlowBench fixture must have at least one expected field set")
	}

	// Validate docs_state loads non-empty content from fixture dir.
	docsState, err := loadFixture(fixtureDir)
	if err != nil {
		t.Fatalf("CT-16: loadFixture error: %v", err)
	}
	if docsState == "" {
		t.Error("CT-16: docs_state must be non-empty after loading fixture dir")
	}
}

// CT-17: judge parser accepts common labeled and embedded score formats.
// Regression guard: judge parse must not error on realistic LLM response shapes.
func TestExtractJudgeScore_Variants(t *testing.T) {
	cases := []struct {
		input string
		want  int
		desc  string
	}{
		{"5", 5, "bare integer"},
		{"Score: 5", 5, "labeled Score:"},
		{"score=4", 4, "labeled score="},
		{"score: 3", 3, "lowercase score:"},
		{"**4**", 4, "bold markdown"},
		{"The response correctly identifies step 6. Score: 5", 5, "explanatory with trailing Score:"},
		{"I would rate this a 3 out of 5.\n3", 3, "last-line bare integer"},
		{"  5  ", 5, "padded bare integer"},
		{"Score: 4\n\nThe single highest-priority step is step 4.", 4, "score on first line, explanation after"},
		{"**Score:** 4\n\nRationale: ...", 4, "bold score on first line"},
	}
	for _, tc := range cases {
		got, err := extractJudgeScore(tc.input)
		if err != nil {
			t.Errorf("CT-17 [%s] %q: unexpected error: %v", tc.desc, tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("CT-17 [%s] %q: want %d, got %d", tc.desc, tc.input, tc.want, got)
		}
	}
}

// CT-17b: judge parser errors on unparseable content.
func TestExtractJudgeScore_Errors(t *testing.T) {
	cases := []struct {
		input string
		desc  string
	}{
		{"", "empty string"},
		{"N/A", "non-numeric"},
		{"0", "out of range low"},
		{"6", "out of range high"},
		{"The model performed well overall.", "no score at all"},
	}
	for _, tc := range cases {
		_, err := extractJudgeScore(tc.input)
		if err == nil {
			t.Errorf("CT-17b [%s] %q: expected error, got nil", tc.desc, tc.input)
		}
	}
}

// CT-18: threshold boundary — score exactly at threshold is PASS, score 1 ULP below is FAIL.
// Regression guard: displayed margin must be non-negative for PASS, negative for FAIL.
func TestAllPass_ThresholdBoundary(t *testing.T) {
	threshold := 0.85

	passCollector := &scoreCollector{byDim: make(map[string][]float64)}
	for _, dim := range dimensions {
		passCollector.record(dim, threshold) // exactly at threshold
	}
	if !allPass(passCollector, threshold) {
		t.Error("CT-18: score exactly at threshold must PASS")
	}

	failCollector := &scoreCollector{byDim: make(map[string][]float64)}
	for _, dim := range dimensions {
		failCollector.record(dim, threshold-0.0001) // just below threshold
	}
	if allPass(failCollector, threshold) {
		t.Error("CT-18: score just below threshold must FAIL")
	}
}
