package poller

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-github/v81/github"

	"github.com/thebrianlopez/runabout/cmd/ghwatch/internal/event"
)

func TestWorkflowPoller_Poll_NewRun(t *testing.T) {
	now := time.Now()
	started := now.Add(-5 * time.Minute)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		runs := &github.WorkflowRuns{
			TotalCount: github.Ptr(1),
			WorkflowRuns: []*github.WorkflowRun{
				{
					ID:           github.Ptr(int64(1001)),
					Name:         github.Ptr("CI"),
					Status:       github.Ptr("completed"),
					Conclusion:   github.Ptr("success"),
					HeadBranch:   github.Ptr("main"),
					HTMLURL:      github.Ptr("https://github.com/testowner/testrepo/actions/runs/1001"),
					UpdatedAt:    &github.Timestamp{Time: now},
					RunStartedAt: &github.Timestamp{Time: started},
				},
			},
		}
		json.NewEncoder(w).Encode(runs)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	w := NewWorkflowPoller(c)
	events, err := w.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Kind != event.KindWorkflow {
		t.Errorf("kind = %q, want workflow", ev.Kind)
	}
	if ev.Workflow.Name != "CI" {
		t.Errorf("name = %q, want CI", ev.Workflow.Name)
	}
	if ev.Workflow.Status != "completed" {
		t.Errorf("status = %q, want completed", ev.Workflow.Status)
	}
	if ev.Workflow.Conclusion != "success" {
		t.Errorf("conclusion = %q, want success", ev.Workflow.Conclusion)
	}
	if ev.Workflow.RunID != 1001 {
		t.Errorf("runID = %d, want 1001", ev.Workflow.RunID)
	}
	if ev.Workflow.Duration <= 0 {
		t.Errorf("duration = %v, want > 0", ev.Workflow.Duration)
	}
}

func TestWorkflowPoller_Poll_StateChange(t *testing.T) {
	now := time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		runs := &github.WorkflowRuns{
			TotalCount: github.Ptr(1),
			WorkflowRuns: []*github.WorkflowRun{
				{
					ID:         github.Ptr(int64(2002)),
					Name:       github.Ptr("Deploy"),
					Status:     github.Ptr("completed"),
					Conclusion: github.Ptr("failure"),
					HeadBranch: github.Ptr("release"),
					HTMLURL:    github.Ptr("https://github.com/testowner/testrepo/actions/runs/2002"),
					UpdatedAt:  &github.Timestamp{Time: now},
				},
			},
		}
		json.NewEncoder(w).Encode(runs)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	wp := NewWorkflowPoller(c)
	// Pre-seed: was "in_progress" with no conclusion.
	wp.knownRuns[2002] = runState{Status: "in_progress", Conclusion: ""}

	events, err := wp.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (state changed)", len(events))
	}
	if events[0].Workflow.Status != "completed" {
		t.Errorf("status = %q, want completed", events[0].Workflow.Status)
	}
	if events[0].Workflow.Conclusion != "failure" {
		t.Errorf("conclusion = %q, want failure", events[0].Workflow.Conclusion)
	}
}

func TestWorkflowPoller_Poll_NoChange(t *testing.T) {
	now := time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		runs := &github.WorkflowRuns{
			TotalCount: github.Ptr(1),
			WorkflowRuns: []*github.WorkflowRun{
				{
					ID:         github.Ptr(int64(3003)),
					Name:       github.Ptr("Lint"),
					Status:     github.Ptr("in_progress"),
					Conclusion: github.Ptr(""),
					HeadBranch: github.Ptr("main"),
					HTMLURL:    github.Ptr("https://github.com/testowner/testrepo/actions/runs/3003"),
					UpdatedAt:  &github.Timestamp{Time: now},
				},
			},
		}
		json.NewEncoder(w).Encode(runs)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	wp := NewWorkflowPoller(c)
	// Pre-seed with same state.
	wp.knownRuns[3003] = runState{Status: "in_progress", Conclusion: ""}

	events, err := wp.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0 (no state change)", len(events))
	}
}

func TestWorkflowPoller_Poll_Duration(t *testing.T) {
	now := time.Now()
	started := now.Add(-10 * time.Minute)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		runs := &github.WorkflowRuns{
			TotalCount: github.Ptr(1),
			WorkflowRuns: []*github.WorkflowRun{
				{
					ID:           github.Ptr(int64(4004)),
					Name:         github.Ptr("Build"),
					Status:       github.Ptr("completed"),
					Conclusion:   github.Ptr("success"),
					HeadBranch:   github.Ptr("main"),
					HTMLURL:      github.Ptr("https://github.com/testowner/testrepo/actions/runs/4004"),
					UpdatedAt:    &github.Timestamp{Time: now},
					RunStartedAt: &github.Timestamp{Time: started},
				},
			},
		}
		json.NewEncoder(w).Encode(runs)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	wp := NewWorkflowPoller(c)
	events, err := wp.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	// Duration should be ~10 minutes.
	dur := events[0].Workflow.Duration
	if dur < 9*time.Minute || dur > 11*time.Minute {
		t.Errorf("duration = %v, want ~10m", dur)
	}
}

func TestWorkflowPoller_Poll_NoDurationWhenInProgress(t *testing.T) {
	now := time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		runs := &github.WorkflowRuns{
			TotalCount: github.Ptr(1),
			WorkflowRuns: []*github.WorkflowRun{
				{
					ID:           github.Ptr(int64(5005)),
					Name:         github.Ptr("Test"),
					Status:       github.Ptr("in_progress"),
					Conclusion:   github.Ptr(""),
					HeadBranch:   github.Ptr("feature"),
					HTMLURL:      github.Ptr("https://github.com/testowner/testrepo/actions/runs/5005"),
					UpdatedAt:    &github.Timestamp{Time: now},
					RunStartedAt: &github.Timestamp{Time: now.Add(-5 * time.Minute)},
				},
			},
		}
		json.NewEncoder(w).Encode(runs)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	wp := NewWorkflowPoller(c)
	events, err := wp.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	// No conclusion → duration should be 0.
	if events[0].Workflow.Duration != 0 {
		t.Errorf("duration = %v, want 0 (no conclusion yet)", events[0].Workflow.Duration)
	}
}

func TestWorkflowPoller_Poll_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	wp := NewWorkflowPoller(c)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := wp.Poll(ctx)
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
}

func TestWorkflowPoller_Poll_StateTracking(t *testing.T) {
	now := time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/testowner/testrepo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		runs := &github.WorkflowRuns{
			TotalCount: github.Ptr(1),
			WorkflowRuns: []*github.WorkflowRun{
				{
					ID:         github.Ptr(int64(6006)),
					Name:       github.Ptr("Check"),
					Status:     github.Ptr("queued"),
					Conclusion: github.Ptr(""),
					HeadBranch: github.Ptr("main"),
					HTMLURL:    github.Ptr("https://github.com/testowner/testrepo/actions/runs/6006"),
					UpdatedAt:  &github.Timestamp{Time: now},
				},
			},
		}
		json.NewEncoder(w).Encode(runs)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	defer c.Close()

	wp := NewWorkflowPoller(c)
	_, _ = wp.Poll(context.Background())

	known := wp.KnownRuns()
	if _, ok := known[6006]; !ok {
		t.Error("run 6006 should be in known state after poll")
	}
	if known[6006].Status != "queued" {
		t.Errorf("known status = %q, want queued", known[6006].Status)
	}
}
