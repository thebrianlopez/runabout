package main

import (
	"context"
	"testing"

	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

// CT-1: next_action returns 1.0 when expected step present in output.
func TestScoreNextAction_Hit(t *testing.T) {
	r := eval.TaskResult[ChainInput, string]{
		Input:  ChainInput{Expected: ChainExpected{PriorityStep: 6}},
		Output: "The next priority action is step 6: create an epic from the approved TDD.",
	}
	scores, err := scoreNextAction(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if scores[0].Score != 1.0 {
		t.Errorf("CT-1: want 1.0, got %f", scores[0].Score)
	}
}

// CT-2: next_action returns 0.0 when expected step absent from output.
func TestScoreNextAction_Miss(t *testing.T) {
	r := eval.TaskResult[ChainInput, string]{
		Input:  ChainInput{Expected: ChainExpected{PriorityStep: 6}},
		Output: "Everything looks complete, no pending actions found.",
	}
	scores, err := scoreNextAction(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if scores[0].Score != 0.0 {
		t.Errorf("CT-2: want 0.0, got %f", scores[0].Score)
	}
}

// CT-3: validate_recall returns ratio of caught violations (2 expected, 1 caught = 0.5).
func TestScoreValidateRecall_Partial(t *testing.T) {
	r := eval.TaskResult[ChainInput, string]{
		Input: ChainInput{Expected: ChainExpected{
			Violations: []string{"missing tdd", "no epic created"},
		}},
		Output: "Gate blocked: missing TDD detected. The design phase is incomplete.",
	}
	scores, err := scoreValidateRecall(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if scores[0].Score != 0.5 {
		t.Errorf("CT-3: want 0.5, got %f", scores[0].Score)
	}
}

// CT-4: icon_accuracy returns ratio of correct icons (4 nodes, 3 correct = 0.75).
func TestScoreIconAccuracy_Partial(t *testing.T) {
	r := eval.TaskResult[ChainInput, string]{
		Input: ChainInput{Expected: ChainExpected{
			IconMap: map[string]string{
				"PRD":  "📋",
				"FDD":  "🔧",
				"TDD":  "📐",
				"Epic": "🚀",
			},
		}},
		Output: "📋 PRD → 🔧 FDD → 📐 TDD → Epic (icon missing)",
	}
	scores, err := scoreIconAccuracy(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if scores[0].Score != 0.75 {
		t.Errorf("CT-4: want 0.75, got %f", scores[0].Score)
	}
}

// Regression guard RG-3: n/a scorer returns 1.0, not 0.0.
func TestScoreNextAction_NA(t *testing.T) {
	r := eval.TaskResult[ChainInput, string]{
		Input:  ChainInput{Expected: ChainExpected{PriorityStep: 0}},
		Output: "Pipeline complete. No next action.",
	}
	scores, err := scoreNextAction(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if scores[0].Score != 1.0 {
		t.Errorf("RG-3: n/a should return 1.0, got %f", scores[0].Score)
	}
}
