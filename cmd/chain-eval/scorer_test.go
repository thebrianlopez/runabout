package main

import (
	"context"
	"testing"
)

// CT-1: next_action returns 1.0 when expected step present in output.
func TestScoreNextAction_Hit(t *testing.T) {
	r := TaskResult{
		Input:  ChainInput{Expected: ChainExpected{PriorityStep: 6}},
		Output: "The next priority action is step 6: create an epic from the approved TDD.",
	}
	score, judgeInvoked := scoreNextAction(context.Background(), r)
	if score != 1.0 {
		t.Errorf("CT-1: want 1.0, got %f", score)
	}
	if judgeInvoked {
		t.Error("CT-1: judge should not be invoked on deterministic hit")
	}
}

// CT-2: next_action returns 0.0 when expected step absent (no judge key set).
func TestScoreNextAction_Miss(t *testing.T) {
	r := TaskResult{
		Input:  ChainInput{Expected: ChainExpected{PriorityStep: 6}},
		Output: "Everything looks complete, no pending actions found.",
	}
	// HUGGINGFACE_API_KEY not set in test env - judge disabled, falls back to 0.0
	score, judgeInvoked := scoreNextAction(context.Background(), r)
	if score != 0.0 {
		t.Errorf("CT-2: want 0.0 (no judge key), got %f", score)
	}
	if !judgeInvoked {
		t.Error("CT-2: judgeInvoked should be true even on error (judge was attempted)")
	}
}

// CT-3: validate_recall returns ratio of caught violations (2 expected, 1 caught).
func TestScoreValidateRecall_Partial(t *testing.T) {
	r := TaskResult{
		Input: ChainInput{Expected: ChainExpected{
			Violations: []string{"missing tdd", "no epic created"},
		}},
		Output: "Gate blocked: missing TDD detected. The design phase is incomplete.",
	}
	// 1/2 caught deterministically; judge disabled in test → returns deterministic ratio
	score, _ := scoreValidateRecall(context.Background(), r)
	if score != 0.5 {
		t.Errorf("CT-3: want 0.5, got %f", score)
	}
}

// CT-4: icon_accuracy returns ratio of correct icons (4 nodes, 3 correct = 0.75).
func TestScoreIconAccuracy_Partial(t *testing.T) {
	r := TaskResult{
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
	score, judgeInvoked := scoreIconAccuracy(context.Background(), r)
	if score != 0.75 {
		t.Errorf("CT-4: want 0.75, got %f", score)
	}
	if judgeInvoked {
		t.Error("CT-4: icon_accuracy never invokes judge")
	}
}

// RG-3: n/a scorer returns 1.0, not 0.0.
func TestScoreNextAction_NA(t *testing.T) {
	r := TaskResult{
		Input:  ChainInput{Expected: ChainExpected{PriorityStep: 0}},
		Output: "Pipeline complete. No next action.",
	}
	score, judgeInvoked := scoreNextAction(context.Background(), r)
	if score != 1.0 {
		t.Errorf("RG-3: n/a should return 1.0, got %f", score)
	}
	if judgeInvoked {
		t.Error("RG-3: judge should not be invoked for n/a fixtures")
	}
}

// CT-10: deterministic pass skips judge (scoreNextAction hit path).
func TestScoreNextAction_DeterministicPassSkipsJudge(t *testing.T) {
	r := TaskResult{
		Input:  ChainInput{Expected: ChainExpected{PriorityStep: 4}},
		Output: "I recommend step 4: approve the TDD before proceeding.",
	}
	score, judgeInvoked := scoreNextAction(context.Background(), r)
	if score != 1.0 {
		t.Errorf("CT-10: want 1.0, got %f", score)
	}
	if judgeInvoked {
		t.Error("CT-10: judge must not be invoked when deterministic check passes")
	}
}

// CT-15: missing HUGGINGFACE_API_KEY → judge disabled, score 0.0 on miss (never silent pass).
func TestScoreNextAction_NoKeyNeverSilentPass(t *testing.T) {
	r := TaskResult{
		Input:  ChainInput{Expected: ChainExpected{PriorityStep: 3}},
		Output: "The next step is to review the PR.", // no "step 3" → deterministic miss
	}
	// No API key in test env
	score, _ := scoreNextAction(context.Background(), r)
	if score >= 1.0 {
		t.Errorf("CT-15 (RG-4): missing key must never produce silent pass, got %f", score)
	}
}
