package poller

import (
	"testing"
)

func TestWorkflowPoller_Name(t *testing.T) {
	w := &WorkflowPoller{knownRuns: make(map[int64]runState)}
	if w.Name() != "workflow" {
		t.Errorf("Name: got %q, want %q", w.Name(), "workflow")
	}
}

func TestRunState_Tracking(t *testing.T) {
	w := &WorkflowPoller{knownRuns: make(map[int64]runState)}

	// Simulate tracking.
	w.knownRuns[1] = runState{Status: "queued", Conclusion: ""}
	w.knownRuns[2] = runState{Status: "in_progress", Conclusion: ""}

	// Verify state.
	if w.knownRuns[1].Status != "queued" {
		t.Errorf("run 1 status: got %q, want %q", w.knownRuns[1].Status, "queued")
	}

	// Simulate transition.
	w.knownRuns[1] = runState{Status: "completed", Conclusion: "success"}
	if w.knownRuns[1].Conclusion != "success" {
		t.Errorf("run 1 conclusion: got %q, want %q", w.knownRuns[1].Conclusion, "success")
	}
}

func TestWorkflowPoller_SetKnownRuns(t *testing.T) {
	w := &WorkflowPoller{knownRuns: make(map[int64]runState)}

	runs := map[int64]runState{
		10: {Status: "completed", Conclusion: "failure"},
		20: {Status: "in_progress", Conclusion: ""},
	}
	w.SetKnownRuns(runs)

	got := w.KnownRuns()
	if len(got) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(got))
	}
	if got[10].Conclusion != "failure" {
		t.Errorf("run 10 conclusion: got %q, want %q", got[10].Conclusion, "failure")
	}
}
