package poller

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-github/v81/github"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/ghwatch/client"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/ghwatch/event"
)

type prState struct {
	State     string
	UpdatedAt time.Time
	Merged    bool
}

// PRPoller polls for pull request activity on a repository.
type PRPoller struct {
	client   *client.Client
	knownPRs map[int]prState
}

// NewPRPoller creates a pull request poller.
func NewPRPoller(c *client.Client) *PRPoller {
	return &PRPoller{
		client:   c,
		knownPRs: make(map[int]prState),
	}
}

func (p *PRPoller) Name() string { return "pr" }

func (p *PRPoller) Poll(ctx context.Context) ([]event.Event, error) {
	opts := &github.PullRequestListOptions{
		State:     "all",
		Sort:      "updated",
		Direction: "desc",
		ListOptions: github.ListOptions{
			PerPage: 50,
		},
	}

	prs, _, err := p.client.ListPullRequests(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("pr poll: %w", err)
	}

	repo := fmt.Sprintf("%s/%s", p.client.Owner(), p.client.Repo())
	var events []event.Event

	for _, pr := range prs {
		num := pr.GetNumber()
		updated := pr.GetUpdatedAt().Time
		merged := pr.GetMerged()
		state := pr.GetState()

		prev, known := p.knownPRs[num]

		if !known {
			// New PR we haven't seen.
			events = append(events, event.Event{
				ID:        fmt.Sprintf("pr-%d-%s", num, state),
				Kind:      event.KindPR,
				Repo:      repo,
				Timestamp: updated,
				PR: &event.PRDetail{
					Number: num,
					Title:  pr.GetTitle(),
					Author: pr.GetUser().GetLogin(),
					Action: actionForNewPR(state, merged),
					URL:    pr.GetHTMLURL(),
				},
			})
		} else if stateChanged(prev, state, merged, updated) {
			action := detectAction(prev, state, merged)
			events = append(events, event.Event{
				ID:        fmt.Sprintf("pr-%d-%s-%d", num, action, updated.Unix()),
				Kind:      event.KindPR,
				Repo:      repo,
				Timestamp: updated,
				PR: &event.PRDetail{
					Number: num,
					Title:  pr.GetTitle(),
					Author: pr.GetUser().GetLogin(),
					Action: action,
					URL:    pr.GetHTMLURL(),
				},
			})
		}

		p.knownPRs[num] = prState{
			State:     state,
			UpdatedAt: updated,
			Merged:    merged,
		}
	}

	return events, nil
}

// KnownPRs returns the current known-PR state (for persistence).
func (p *PRPoller) KnownPRs() map[int]prState {
	return p.knownPRs
}

// SetKnownPRs restores saved PR state.
func (p *PRPoller) SetKnownPRs(m map[int]prState) {
	p.knownPRs = m
}

func actionForNewPR(state string, merged bool) string {
	if merged {
		return "merged"
	}
	if state == "closed" {
		return "closed"
	}
	return "opened"
}

func stateChanged(prev prState, newState string, merged bool, updated time.Time) bool {
	if prev.State != newState {
		return true
	}
	if !prev.Merged && merged {
		return true
	}
	return false
}

func detectAction(prev prState, newState string, merged bool) string {
	if !prev.Merged && merged {
		return "merged"
	}
	if prev.State == "open" && newState == "closed" {
		return "closed"
	}
	if prev.State == "closed" && newState == "open" {
		return "reopened"
	}
	return "updated"
}
