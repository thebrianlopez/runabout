package client

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	cboff "github.com/cenkalti/backoff/v4"
	"github.com/google/go-github/v81/github"
)

// Client wraps the GitHub API client with context-aware retry and rate limiting.
type Client struct {
	gh          *github.Client
	owner       string
	repo        string
	debug       bool
	rateLimiter *time.Ticker
	logger      *log.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithDebug enables debug logging to the provided logger.
func WithDebug(logger *log.Logger) Option {
	return func(c *Client) {
		c.debug = true
		c.logger = logger
	}
}

// NewWithGitHubClient creates a Client from a pre-configured *github.Client.
// Intended for tests that inject an httptest-backed client.
func NewWithGitHubClient(gh *github.Client, owner, repo string) *Client {
	return &Client{
		gh:          gh,
		owner:       owner,
		repo:        repo,
		rateLimiter: time.NewTicker(time.Millisecond),
	}
}

// New creates a GitHub API client for the given owner/repo.
func New(token, owner, repo string, opts ...Option) (*Client, error) {
	if token == "" {
		return nil, errors.New("GitHub token is required")
	}
	if owner == "" || repo == "" {
		return nil, errors.New("owner and repo are required")
	}

	c := &Client{
		gh:          github.NewClient(nil).WithAuthToken(token),
		owner:       owner,
		repo:        repo,
		rateLimiter: time.NewTicker(time.Second),
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

func (c *Client) logf(format string, args ...interface{}) {
	if c.debug && c.logger != nil {
		c.logger.Printf(format, args...)
	}
}

// ListRepoEvents returns repository events via Activity.ListRepositoryEvents.
func (c *Client) ListRepoEvents(ctx context.Context, opts *github.ListOptions) ([]*github.Event, *github.Response, error) {
	var events []*github.Event
	var resp *github.Response
	err := c.withRetry(ctx, func() error {
		<-c.rateLimiter.C
		var err error
		events, resp, err = c.gh.Activity.ListRepositoryEvents(ctx, c.owner, c.repo, opts)
		return err
	})
	return events, resp, err
}

// ListPullRequests returns pull requests via PullRequests.List.
func (c *Client) ListPullRequests(ctx context.Context, opts *github.PullRequestListOptions) ([]*github.PullRequest, *github.Response, error) {
	var prs []*github.PullRequest
	var resp *github.Response
	err := c.withRetry(ctx, func() error {
		<-c.rateLimiter.C
		var err error
		prs, resp, err = c.gh.PullRequests.List(ctx, c.owner, c.repo, opts)
		return err
	})
	return prs, resp, err
}

// ListWorkflowRuns returns workflow runs via Actions.ListRepositoryWorkflowRuns.
func (c *Client) ListWorkflowRuns(ctx context.Context, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
	var runs *github.WorkflowRuns
	var resp *github.Response
	err := c.withRetry(ctx, func() error {
		<-c.rateLimiter.C
		var err error
		runs, resp, err = c.gh.Actions.ListRepositoryWorkflowRuns(ctx, c.owner, c.repo, opts)
		return err
	})
	return runs, resp, err
}

// GetCommit returns a single commit by SHA, including file change details.
func (c *Client) GetCommit(ctx context.Context, sha string) (*github.RepositoryCommit, *github.Response, error) {
	var commit *github.RepositoryCommit
	var resp *github.Response
	err := c.withRetry(ctx, func() error {
		<-c.rateLimiter.C
		var err error
		commit, resp, err = c.gh.Repositories.GetCommit(ctx, c.owner, c.repo, sha, nil)
		return err
	})
	return commit, resp, err
}

// withRetry executes fn with exponential backoff, handling GitHub rate limit errors.
// Uses select-based waits so the context can cancel at any point.
func (c *Client) withRetry(ctx context.Context, fn func() error) error {
	const maxRetries = 5
	boConfig := cboff.NewExponentialBackOff()
	boConfig.InitialInterval = 2 * time.Second
	boConfig.MaxInterval = 60 * time.Second
	boConfig.MaxElapsedTime = 0
	bo := cboff.WithContext(cboff.WithMaxRetries(boConfig, maxRetries-1), ctx)
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			c.logf("retry attempt %d/%d", attempt+1, maxRetries)
		}

		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Primary rate limit — wait until reset.
		var rateLimitErr *github.RateLimitError
		if errors.As(err, &rateLimitErr) {
			wait := time.Until(rateLimitErr.Rate.Reset.Time)
			if wait <= 0 {
				wait = time.Second
			}
			c.logf("rate limit hit, waiting %v", wait)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
				bo.Reset()
				continue
			}
		}

		// Secondary / abuse rate limit.
		var abuseErr *github.AbuseRateLimitError
		if errors.As(err, &abuseErr) {
			wait := 60 * time.Second
			if abuseErr.RetryAfter != nil {
				wait = *abuseErr.RetryAfter
			}
			c.logf("abuse rate limit, waiting %v", wait)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
				bo.Reset()
				continue
			}
		}

		// Generic transient error — exponential backoff.
		if attempt < maxRetries-1 {
			d := bo.NextBackOff()
			if d == cboff.Stop {
				break
			}
			c.logf("request failed: %v, backing off %v", err, d)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
			}
		}
	}
	return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// Owner returns the repository owner.
func (c *Client) Owner() string { return c.owner }

// Repo returns the repository name.
func (c *Client) Repo() string { return c.repo }

// Close stops the internal rate limiter.
func (c *Client) Close() {
	c.rateLimiter.Stop()
}
