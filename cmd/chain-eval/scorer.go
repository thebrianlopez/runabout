package main

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// ChainInput is the input for a chain eval case.
type ChainInput struct {
	Command  string        `json:"command"`
	Fixture  string        `json:"fixture"`
	Expected ChainExpected `json:"expected"`
}

// ChainExpected holds ground-truth values for a fixture.
type ChainExpected struct {
	// PriorityStep is the step number Claude should recommend as next action.
	// Zero means this dimension is n/a for this fixture (scorer returns 1.0).
	PriorityStep int `json:"priority_step,omitempty"`

	// Violations lists keywords that must appear in Claude's validate output.
	// Empty means this dimension is n/a (scorer returns 1.0).
	Violations []string `json:"violations,omitempty"`

	// IconMap maps artifact names to expected icon strings in Claude's output.
	// Empty means this dimension is n/a (scorer returns 1.0).
	IconMap map[string]string `json:"icon_map,omitempty"`
}

// TaskResult pairs a fixture input with Claude's raw output text.
type TaskResult struct {
	Input  ChainInput
	Output string
}

// scoreNextAction uses a two-tier approach: deterministic string match first (zero cost),
// Prometheus judge only on deterministic fail. Returns score and whether judge was invoked.
// Judge errors score 0.0 - never silently pass (RG-4).
func scoreNextAction(ctx context.Context, r TaskResult) (score float64, judgeInvoked bool) {
	exp := r.Input.Expected
	if exp.PriorityStep == 0 {
		return 1.0, false // n/a for this fixture
	}
	if containsStepN(r.Output, exp.PriorityStep) {
		return 1.0, false // deterministic pass - skip judge
	}
	s, err := judgeScore(ctx, r.Output, DimNextAction)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ judge error (next_action fixture=%s): %v\n", r.Input.Fixture, err)
		return 0.0, true
	}
	return s, true
}

// scoreValidateRecall uses keyword recall as the fast path, judge on partial/zero recall.
func scoreValidateRecall(ctx context.Context, r TaskResult) (score float64, judgeInvoked bool) {
	exp := r.Input.Expected
	if len(exp.Violations) == 0 {
		return 1.0, false // n/a
	}
	caught := countCaught(r.Output, exp.Violations)
	ratio := float64(caught) / float64(len(exp.Violations))
	if ratio >= 1.0 {
		return 1.0, false // all keywords found - deterministic pass
	}
	// Partial or zero recall: judge may find semantically equivalent phrasing
	s, err := judgeScore(ctx, r.Output, DimValidateRecall)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ judge error (validate_recall fixture=%s): %v\n", r.Input.Fixture, err)
		return ratio, true // fall back to deterministic ratio, not 0.0
	}
	// Take the max: judge may confirm the partial recall is actually adequate
	if s > ratio {
		return s, true
	}
	return ratio, true
}

// scoreIconAccuracy is deterministic only - emoji+artifact co-occurrence is reliable.
func scoreIconAccuracy(_ context.Context, r TaskResult) (score float64, judgeInvoked bool) {
	exp := r.Input.Expected
	if len(exp.IconMap) == 0 {
		return 1.0, false // n/a
	}
	correct := countCorrectIcons(r.Output, exp.IconMap)
	return float64(correct) / float64(len(exp.IconMap)), false
}

func containsStepN(output string, step int) bool {
	return strings.Contains(strings.ToLower(output), fmt.Sprintf("step %d", step))
}

func countCaught(output string, violations []string) int {
	lower := strings.ToLower(output)
	count := 0
	for _, v := range violations {
		if strings.Contains(lower, strings.ToLower(v)) {
			count++
		}
	}
	return count
}

func countCorrectIcons(output string, iconMap map[string]string) int {
	count := 0
	for artifact, icon := range iconMap {
		if strings.Contains(output, icon) && strings.Contains(output, artifact) {
			count++
		}
	}
	return count
}
