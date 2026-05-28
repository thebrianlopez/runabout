// Package poller implements the main Jira polling loop. It wires together the
// jiraclient, dedupe store, and publisher to poll for status transitions on a
// configurable interval. Shutdown is graceful: any in-flight poll cycle
// completes before Run returns.
package poller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/config"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/dedupe"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/jiraclient"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/publisher"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/types"
)

// Sentinel errors.
var (
	// ErrPollOverlap is returned by PollOnce when a previous cycle is still
	// running. The new tick is discarded; the running cycle completes normally.
	ErrPollOverlap = errors.New("poller: poll overlap — previous cycle still running")

	// ErrJiraFetch wraps errors returned by jiraclient.Client.SearchTransitions.
	ErrJiraFetch = errors.New("poller: jira fetch failed")

	// ErrCredRefresh wraps auth.ErrRefreshFailed; stale credentials are still
	// used and the poll proceeds.
	ErrCredRefresh = errors.New("poller: credential refresh failed")

	// ErrPublish wraps errors from publisher.Publisher.Publish.
	ErrPublish = errors.New("poller: publish failed")

	// ErrDedupe wraps errors from dedupe.DedupeStore.Mark (post-publish).
	ErrDedupe = errors.New("poller: dedupe mark failed")
)

// Poller is the main service loop. Safe for concurrent use via Run/PollOnce.
type Poller struct {
	cfg    config.Config
	jira   jiraclient.Client
	store  dedupe.DedupeStore
	pub    publisher.Publisher
	nowFn  func() time.Time
	logger *slog.Logger

	mu           sync.Mutex
	lastPollTime atomic.Value // stores time.Time; zero until first successful poll
}

// New constructs a Poller. All dependencies must be non-nil except logger
// (which falls back to slog.Default).
func New(
	cfg config.Config,
	jira jiraclient.Client,
	store dedupe.DedupeStore,
	pub publisher.Publisher,
	nowFn func() time.Time,
	logger *slog.Logger,
) *Poller {
	if logger == nil {
		logger = slog.Default()
	}
	return &Poller{
		cfg:    cfg,
		jira:   jira,
		store:  store,
		pub:    pub,
		nowFn:  nowFn,
		logger: logger,
	}
}

// Run blocks until ctx is cancelled. On each tick it launches a poll cycle in
// a goroutine; concurrent ticks are dropped with ErrPollOverlap. On shutdown,
// Run waits for any in-flight cycle to finish before returning nil.
func (p *Poller) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Acquire the mutex to wait for any in-flight poll cycle to finish.
			p.mu.Lock()
			p.mu.Unlock()
			return nil
		case <-ticker.C:
			go p.PollOnce(ctx)
		}
	}
}

// PollOnce runs a single poll cycle. Exported for deterministic unit testing.
// Returns ErrPollOverlap if a cycle is already running (non-blocking check).
func (p *Poller) PollOnce(ctx context.Context) error {
	if !p.mu.TryLock() {
		p.logger.Warn(
			"poll skipped: previous cycle still running",
			"reason", "overlap",
			"error", ErrPollOverlap,
		)
		return ErrPollOverlap
	}
	defer p.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("poll panic recovered", "panic", r)
		}
	}()

	p.runCycle(ctx)
	return nil
}

// LastPollTime returns the wall-clock time of the last successful poll
// completion. Returns zero time before the first successful cycle.
func (p *Poller) LastPollTime() time.Time {
	if raw := p.lastPollTime.Load(); raw != nil {
		return raw.(time.Time)
	}
	return time.Time{}
}

// runCycle executes one complete poll-filter-publish-mark iteration.
func (p *Poller) runCycle(ctx context.Context) {
	pollID := newPollID()
	now := p.nowFn()
	lookbackMinutes := int(p.cfg.LookbackWindow.Minutes())

	p.logger.Debug(
		"poll cycle started",
		"poll_id", pollID,
		"projects", p.cfg.JiraProjects,
		"lookback_s", p.cfg.LookbackWindow.Seconds(),
	)

	// ── Fetch all pages from Jira ────────────────────────────────────────────
	var allIssues []jiraclient.Issue
	nextToken := ""
	for {
		result, err := p.jira.SearchTransitions(ctx, jiraclient.SearchRequest{
			Projects:        p.cfg.JiraProjects,
			LookbackMinutes: lookbackMinutes,
			MaxResults:      100,
			NextToken:       nextToken,
		})
		if err != nil {
			p.logger.Error(
				"poll: jira fetch failed",
				"poll_id", pollID,
				"error_class", "jira",
				"error", err,
			)
			return // skip cycle; LastPollTime not updated
		}
		allIssues = append(allIssues, result.Issues...)
		if result.NextToken == "" {
			break
		}
		nextToken = result.NextToken
	}

	// ── Filter changelog entries by lookback window ──────────────────────────
	cutoff := now.Add(-p.cfg.LookbackWindow)
	var events []types.TransitionEvent
	for _, issue := range allIssues {
		for _, entry := range issue.Changelog {
			if entry.Created.Before(cutoff) {
				continue
			}
			events = append(events, buildEvent(issue, entry))
		}
	}

	// ── Publish then mark each event (FDD §6: publish-first ordering) ────────
	// Publishing to the outbox first ensures events are durable. Mark is only
	// called for events that were actually inserted (result.Succeeded), so a
	// publish failure does not permanently drop the event — the next poll will
	// see the same changelog entry and retry.
	published, deduped := 0, 0
	for _, ev := range events {
		result, err := p.pub.Publish(ctx, []types.TransitionEvent{ev})
		if err != nil {
			p.logger.Error(
				"poll: publish error",
				"poll_id", pollID,
				"event_id", ev.ChangelogID,
				"error_class", "publish",
				"error", err,
			)
			continue
		}

		// Only mark events that were newly inserted into the outbox.
		// Events in result.Failed are already in the outbox (UNIQUE constraint);
		// marking them again would be redundant and incorrect.
		for _, id := range result.Succeeded {
			isNew, merr := p.store.Mark(ctx, id, p.cfg.LookbackWindow*2)
			if merr != nil {
				p.logger.Error(
					"poll: dedupe mark error",
					"poll_id", pollID,
					"event_id", id,
					"error_class", "dedupe",
					"error", merr,
				)
				continue
			}
			if isNew {
				published++
			} else {
				deduped++
			}
		}
		if len(result.Failed) > 0 {
			deduped += len(result.Failed)
		}
	}

	// ── Update last-success timestamp ────────────────────────────────────────
	t := p.nowFn()
	p.lastPollTime.Store(t)

	p.logger.Info(
		"poll cycle complete",
		"poll_id", pollID,
		"issues_returned", len(allIssues),
		"transitions_found", len(events),
		"published", published,
		"deduped", deduped,
	)
}

// buildEvent constructs a TransitionEvent from a Jira issue and changelog entry.
func buildEvent(issue jiraclient.Issue, entry jiraclient.ChangelogEntry) types.TransitionEvent {
	ev := types.TransitionEvent{
		IssueKey:       issue.Key,
		ProjectKey:     extractProjectKey(issue.Key),
		IssueType:      issue.IssueType,
		Summary:        issue.Summary,
		FromStatus:     entry.FromStatus,
		ToStatus:       entry.ToStatus,
		TransitionedAt: entry.Created,
		TransitionedBy: types.UserRef{
			AccountID:   entry.Author.AccountID,
			DisplayName: entry.Author.DisplayName,
			Email:       entry.Author.Email,
		},
		Labels:      issue.Labels,
		ChangelogID: issue.Key + ":" + entry.HistoryID,
		Self:        issue.Self,
	}
	if issue.Assignee != nil {
		ev.Assignee = &types.UserRef{
			AccountID:   issue.Assignee.AccountID,
			DisplayName: issue.Assignee.DisplayName,
			Email:       issue.Assignee.Email,
		}
	}
	if ev.Labels == nil {
		ev.Labels = []string{}
	}
	return ev
}

// extractProjectKey returns the project prefix from an issue key.
// "INFRA-1234" → "INFRA"
func extractProjectKey(issueKey string) string {
	if idx := strings.Index(issueKey, "-"); idx >= 0 {
		return issueKey[:idx]
	}
	return issueKey
}

// newPollID generates an 8-character hex string for log correlation.
func newPollID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
