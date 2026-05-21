package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

// ChainInput is the input for a chain eval case.
// Expected embeds ground-truth values so scorers can access them via r.Input.Expected
// while the task output (R=string) remains the raw Claude response text.
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

func scoreNextAction(_ context.Context, r eval.TaskResult[ChainInput, string]) (eval.Scores, error) {
	exp := r.Input.Expected
	if exp.PriorityStep == 0 {
		return eval.S(1.0), nil // n/a
	}
	if containsStepN(r.Output, exp.PriorityStep) {
		return eval.S(1.0), nil
	}
	return eval.S(0.0), nil
}

func scoreValidateRecall(_ context.Context, r eval.TaskResult[ChainInput, string]) (eval.Scores, error) {
	exp := r.Input.Expected
	if len(exp.Violations) == 0 {
		return eval.S(1.0), nil // n/a
	}
	caught := countCaught(r.Output, exp.Violations)
	return eval.S(float64(caught) / float64(len(exp.Violations))), nil
}

func scoreIconAccuracy(_ context.Context, r eval.TaskResult[ChainInput, string]) (eval.Scores, error) {
	exp := r.Input.Expected
	if len(exp.IconMap) == 0 {
		return eval.S(1.0), nil // n/a
	}
	correct := countCorrectIcons(r.Output, exp.IconMap)
	return eval.S(float64(correct) / float64(len(exp.IconMap))), nil
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
