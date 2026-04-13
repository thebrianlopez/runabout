package poller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/v81/github"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/ghwatch/client"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/ghwatch/event"
)

// PushPoller polls for push events on a repository.
type PushPoller struct {
	client   *client.Client
	lastSeen time.Time
}

// NewPushPoller creates a push event poller.
func NewPushPoller(c *client.Client, since time.Time) *PushPoller {
	return &PushPoller{client: c, lastSeen: since}
}

func (p *PushPoller) Name() string { return "push" }

func (p *PushPoller) Poll(ctx context.Context) ([]event.Event, error) {
	opts := &github.ListOptions{PerPage: 100}
	ghEvents, _, err := p.client.ListRepoEvents(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("push poll: %w", err)
	}

	var events []event.Event
	for _, e := range ghEvents {
		if e.GetType() != "PushEvent" {
			continue
		}
		ts := e.GetCreatedAt().Time
		if !ts.After(p.lastSeen) {
			continue
		}

		payload, err := e.ParsePayload()
		if err != nil {
			continue
		}
		push, ok := payload.(*github.PushEvent)
		if !ok {
			continue
		}

		branch := strings.TrimPrefix(push.GetRef(), "refs/heads/")
		var commits []event.CommitInfo
		for _, c := range push.Commits {
			commits = append(commits, event.CommitInfo{
				SHA:      c.GetSHA(),
				Author:   c.GetAuthor().GetName(),
				Message:  c.GetMessage(),
				Added:    c.Added,
				Removed:  c.Removed,
				Modified: c.Modified,
			})
		}

		// If the Events API returned 0 inline commits (common for repo events
		// endpoint), hydrate from the Commits API using the head SHA.
		if len(commits) == 0 && push.GetHead() != "" {
			if c, err := p.hydrateCommit(ctx, push.GetHead()); err == nil {
				commits = []event.CommitInfo{c}
			}
		}

		repo := fmt.Sprintf("%s/%s", p.client.Owner(), p.client.Repo())
		events = append(events, event.Event{
			ID:        e.GetID(),
			Kind:      event.KindPush,
			Repo:      repo,
			Timestamp: ts,
			Push: &event.PushDetail{
				Branch:  branch,
				HeadSHA: push.GetHead(),
				Size:    push.GetSize(),
				Commits: commits,
			},
		})
	}

	if len(events) > 0 {
		// Update lastSeen to the most recent event timestamp.
		latest := events[0].Timestamp
		for _, ev := range events[1:] {
			if ev.Timestamp.After(latest) {
				latest = ev.Timestamp
			}
		}
		p.lastSeen = latest
	}

	return events, nil
}

// hydrateCommit fetches a single commit from the Commits API, returning
// CommitInfo with file change details (Added/Removed/Modified).
func (p *PushPoller) hydrateCommit(ctx context.Context, sha string) (event.CommitInfo, error) {
	rc, _, err := p.client.GetCommit(ctx, sha)
	if err != nil {
		return event.CommitInfo{}, fmt.Errorf("hydrate commit %s: %w", sha[:7], err)
	}

	ci := event.CommitInfo{
		SHA: rc.GetSHA(),
	}
	if rc.Commit != nil {
		ci.Message = rc.Commit.GetMessage()
		if rc.Commit.Author != nil {
			ci.Author = rc.Commit.Author.GetName()
		}
	}

	for _, f := range rc.Files {
		name := f.GetFilename()
		switch f.GetStatus() {
		case "added":
			ci.Added = append(ci.Added, name)
		case "removed":
			ci.Removed = append(ci.Removed, name)
		case "modified", "changed":
			ci.Modified = append(ci.Modified, name)
		case "renamed":
			ci.Modified = append(ci.Modified, name)
		}
	}

	return ci, nil
}

// SetLastSeen sets the last-seen timestamp (for state hydration).
func (p *PushPoller) SetLastSeen(t time.Time) {
	p.lastSeen = t
}

// LastSeen returns the last-seen timestamp.
func (p *PushPoller) LastSeen() time.Time {
	return p.lastSeen
}
