package poller

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-github/v81/github"

	"github.com/thebrianlopez/runabout/cmd/ghwatch/internal/client"
	"github.com/thebrianlopez/runabout/cmd/ghwatch/internal/event"
)

type runState struct {
	Status     string
	Conclusion string
}

// WorkflowPoller polls for GitHub Actions workflow run activity.
type WorkflowPoller struct {
	client    *client.Client
	knownRuns map[int64]runState
}

// NewWorkflowPoller creates a workflow run poller.
func NewWorkflowPoller(c *client.Client) *WorkflowPoller {
	return &WorkflowPoller{
		client:    c,
		knownRuns: make(map[int64]runState),
	}
}

func (w *WorkflowPoller) Name() string { return "workflow" }

func (w *WorkflowPoller) Poll(ctx context.Context) ([]event.Event, error) {
	opts := &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{PerPage: 50},
	}

	runs, _, err := w.client.ListWorkflowRuns(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("workflow poll: %w", err)
	}

	repo := fmt.Sprintf("%s/%s", w.client.Owner(), w.client.Repo())
	var events []event.Event

	for _, run := range runs.WorkflowRuns {
		id := run.GetID()
		status := run.GetStatus()
		conclusion := run.GetConclusion()

		prev, known := w.knownRuns[id]
		if known && prev.Status == status && prev.Conclusion == conclusion {
			continue // No state change.
		}

		var dur time.Duration
		if conclusion != "" && run.RunStartedAt != nil {
			dur = run.GetUpdatedAt().Time.Sub(run.RunStartedAt.Time)
		}

		branch := run.GetHeadBranch()
		events = append(events, event.Event{
			ID:        fmt.Sprintf("wf-%d-%s-%s", id, status, conclusion),
			Kind:      event.KindWorkflow,
			Repo:      repo,
			Timestamp: run.GetUpdatedAt().Time,
			Workflow: &event.WorkflowDetail{
				Name:       run.GetName(),
				Status:     status,
				Conclusion: conclusion,
				Branch:     branch,
				Duration:   dur,
				URL:        run.GetHTMLURL(),
				RunID:      id,
			},
		})

		w.knownRuns[id] = runState{Status: status, Conclusion: conclusion}
	}

	return events, nil
}

// KnownRuns returns the current known-run state (for persistence).
func (w *WorkflowPoller) KnownRuns() map[int64]runState {
	return w.knownRuns
}

// SetKnownRuns restores saved workflow run state.
func (w *WorkflowPoller) SetKnownRuns(m map[int64]runState) {
	w.knownRuns = m
}
