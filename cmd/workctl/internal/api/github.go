package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	cboff "github.com/cenkalti/backoff/v4"
	"github.com/google/go-github/v81/github"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/config"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// GitHubClient wraps the go-github client with rate limiting
type GitHubClient struct {
	Client      *github.Client
	token       string
	rateLimiter *time.Ticker
}

// NewGitHubClient creates a new GitHub client with authentication
func NewGitHubClient(token string) (*GitHubClient, error) {
	if token == "" {
		return nil, errors.New("GitHub token is required")
	}

	// Create client with authentication
	client := github.NewClient(nil).WithAuthToken(token)

	if config.Debug {
		config.LogDebug("GitHub client initialized successfully")
	}

	return &GitHubClient{
		Client:      client,
		token:       token,
		rateLimiter: time.NewTicker(time.Second), // 1 request per second
	}, nil
}

// GitHubIssueInfo holds the essential fields of a GitHub issue for workspace init.
type GitHubIssueInfo struct {
	Key         string // "owner/repo#123"
	Summary     string
	Description string // Markdown body
	URL         string
	Status      string // "open" or "closed"
	Type        string // "Issue" or "Pull Request"
}

// GetGitHubIssue fetches a single GitHub issue (or PR) by owner, repo, and number.
func (g *GitHubClient) GetGitHubIssue(ctx context.Context, owner, repo string, number int) (*GitHubIssueInfo, error) {
	<-g.rateLimiter.C

	var issue *github.Issue
	err := g.withRetry(func() error {
		var fetchErr error
		issue, _, fetchErr = g.Client.Issues.Get(ctx, owner, repo, number)
		return fetchErr
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch issue %s/%s#%d: %w", owner, repo, number, err)
	}

	info := &GitHubIssueInfo{
		Key:     fmt.Sprintf("%s/%s#%d", owner, repo, number),
		Summary: issue.GetTitle(),
		URL:     issue.GetHTMLURL(),
		Status:  issue.GetState(),
	}

	if issue.GetBody() != "" {
		info.Description = issue.GetBody()
	}

	if issue.IsPullRequest() {
		info.Type = "Pull Request"
	} else {
		info.Type = "Issue"
	}

	return info, nil
}

// GetUserActivity fetches GitHub events for a user within a date range
func (g *GitHubClient) GetUserActivity(ctx context.Context, username string, cfg *models.QueryConfig) ([]models.GitHubActivity, error) {
	if username == "" {
		return nil, errors.New("GitHub username is required")
	}

	if config.Debug {
		config.LogDebug("Fetching GitHub activity for user: %s", username)
		config.LogDebug("Date range: %s to %s", cfg.StartDate, cfg.EndDate)
	}

	// Parse date range for filtering
	startDate, err := time.Parse("2006-01-02", cfg.StartDate)
	if err != nil {
		return nil, fmt.Errorf("invalid start date: %w", err)
	}

	endDate, err := time.Parse("2006-01-02", cfg.EndDate)
	if err != nil {
		return nil, fmt.Errorf("invalid end date: %w", err)
	}
	// Add 24 hours to include the end date
	endDate = endDate.Add(24 * time.Hour)

	// Select API strategy based on date range and user preference
	strategyOverride := APIStrategy(cfg.GitHubAPIStrategy)
	if strategyOverride == "" {
		strategyOverride = StrategyAuto
	}

	strategy, err := SelectStrategy(startDate, strategyOverride)
	if err != nil {
		return nil, fmt.Errorf("failed to select API strategy: %w", err)
	}

	// Calculate date range age for warnings
	daysAgo := CalculateDateRangeAge(startDate)

	if config.Debug {
		config.LogDebug("Date range age: %d days ago", daysAgo)
		config.LogDebug("Selected strategy: %s", strategy)
		config.LogDebug("Strategy description: %s", GetStrategyDescription(strategy))
	}

	// Warn user about API limitations
	WarnAboutLimitations(strategy, daysAgo, false) // TODO: respect --quiet flag

	var allActivities []models.GitHubActivity

	// Route to appropriate API implementation
	switch strategy {
	case StrategyEvents:
		// Use Events API (current implementation)
		allActivities, err = g.fetchViaEventsAPI(ctx, username, startDate, endDate)
	case StrategySearch:
		// Use Search API for historical data (Phase 2 - IMPLEMENTED)
		allActivities, err = g.fetchViaSearchAPI(ctx, username, startDate, endDate)
	case StrategyGraphQL:
		// Use GraphQL API for aggregate stats (Phase 3 - IMPLEMENTED)
		allActivities, err = g.fetchViaGraphQLAPI(ctx, username, startDate, endDate)
	default:
		return nil, fmt.Errorf("unknown API strategy: %s", strategy)
	}

	if err != nil {
		return nil, err
	}

	// Fetch repo-targeted commit history in parallel with strategy-based fetch
	if len(cfg.GitHubRepos) > 0 {
		commitActivities, err := g.fetchViaCommitsAPI(ctx, username, cfg.GitHubRepos, startDate, endDate, cfg.GitHubEnrich)
		if err != nil {
			return nil, fmt.Errorf("fetching commit history: %w", err)
		}
		allActivities = append(allActivities, commitActivities...)
	}

	return allActivities, nil
}

// fetchViaCommitsAPI fetches commit history for specified repos using the Commits API.
// Per-repo errors are non-fatal (warned to stderr) to avoid aborting the full run.
func (g *GitHubClient) fetchViaCommitsAPI(ctx context.Context, username string, repos []string, startDate, endDate time.Time, enrich bool) ([]models.GitHubActivity, error) {
	var all []models.GitHubActivity

	for _, repoSlug := range repos {
		owner, repo, err := config.ParseGitHubRepo(repoSlug)
		if err != nil {
			// Already validated at parse time; log and skip
			fmt.Printf("⚠️  Skipping invalid repo %q: %v\n", repoSlug, err)
			continue
		}

		activities, err := g.listCommitsForRepo(ctx, username, owner, repo, startDate, endDate, enrich)
		if err != nil {
			// Non-fatal: 404 (no access / repo missing) should not abort the run
			fmt.Printf("⚠️  Skipping repo %s/%s: %v\n", owner, repo, err)
			continue
		}

		if config.Debug {
			config.LogDebug("fetchViaCommitsAPI: %d commits from %s/%s", len(activities), owner, repo)
		}

		all = append(all, activities...)
	}

	return all, nil
}

// listCommitsForRepo lists commits authored by username in owner/repo within the date range.
// Paginates until all commits in range are retrieved.
func (g *GitHubClient) listCommitsForRepo(ctx context.Context, username, owner, repo string, startDate, endDate time.Time, enrich bool) ([]models.GitHubActivity, error) {
	var activities []models.GitHubActivity

	opts := &github.CommitsListOptions{
		Author: username,
		Since:  startDate,
		Until:  endDate,
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
	}

	for {
		<-g.rateLimiter.C

		var commits []*github.RepositoryCommit
		var resp *github.Response

		err := g.withRetry(func() error {
			var fetchErr error
			commits, resp, fetchErr = g.Client.Repositories.ListCommits(ctx, owner, repo, opts)
			return fetchErr
		})
		if err != nil {
			return nil, fmt.Errorf("listing commits for %s/%s: %w", owner, repo, err)
		}

		if config.Debug {
			config.LogDebug("listCommitsForRepo: fetched %d commits from %s/%s (page %d)",
				len(commits), owner, repo, opts.Page)
		}

		for _, c := range commits {
			activity := g.convertCommitToActivity(c, owner, repo, username)
			if enrich {
				g.enrichCommit(ctx, &activity, owner, repo)
			}
			activities = append(activities, activity)
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return activities, nil
}

// convertCommitToActivity maps a RepositoryCommit to a GitHubActivity with EventType "CommitEvent".
func (g *GitHubClient) convertCommitToActivity(c *github.RepositoryCommit, owner, repo, username string) models.GitHubActivity {
	sha := c.GetSHA()
	repoSlug := owner + "/" + repo

	// Extract first line of commit message
	msg := ""
	if c.GetCommit() != nil {
		full := c.GetCommit().GetMessage()
		if idx := strings.IndexByte(full, '\n'); idx >= 0 {
			msg = full[:idx]
		} else {
			msg = full
		}
	}

	// Prefer author timestamp; fall back to committer timestamp
	var ts time.Time
	if c.GetCommit() != nil && c.GetCommit().GetAuthor() != nil && c.GetCommit().GetAuthor().Date != nil {
		ts = c.GetCommit().GetAuthor().Date.Time
	} else if c.GetCommit() != nil && c.GetCommit().GetCommitter() != nil && c.GetCommit().GetCommitter().Date != nil {
		ts = c.GetCommit().GetCommitter().Date.Time
	}

	// Determine actor (prefer commit author login, fall back to provided username)
	actor := username
	if c.GetAuthor() != nil && c.GetAuthor().GetLogin() != "" {
		actor = c.GetAuthor().GetLogin()
	}

	return models.GitHubActivity{
		EventID:       sha,
		EventType:     "CommitEvent",
		ActorLogin:    actor,
		Repository:    repoSlug,
		Timestamp:     ts,
		Description:   msg,
		URL:           fmt.Sprintf("https://github.com/%s/commit/%s", repoSlug, sha),
		Public:        false, // visibility unknown without extra API call
		CommitSHA:     sha,
		CommitMessage: msg,
	}
}

// enrichCommit fetches per-file diff stats for an activity and populates LinesAdded/Removed/FilesChanged.
// Errors are non-fatal: a warning is printed but the activity is still included.
func (g *GitHubClient) enrichCommit(ctx context.Context, activity *models.GitHubActivity, owner, repo string) {
	<-g.rateLimiter.C

	var commit *github.RepositoryCommit
	err := g.withRetry(func() error {
		var fetchErr error
		commit, _, fetchErr = g.Client.Repositories.GetCommit(ctx, owner, repo, activity.CommitSHA, nil)
		return fetchErr
	})
	if err != nil {
		if config.Debug {
			config.LogDebug("enrichCommit: failed to fetch stats for %s/%s@%s: %v",
				owner, repo, activity.CommitSHA[:8], err)
		}
		fmt.Printf("⚠️  Could not enrich commit %s: %v\n", activity.CommitSHA[:8], err)
		return
	}

	if commit.Stats != nil {
		activity.LinesAdded = commit.Stats.GetAdditions()
		activity.LinesRemoved = commit.Stats.GetDeletions()
	}

	files := make([]string, 0, len(commit.Files))
	for _, f := range commit.Files {
		files = append(files, f.GetFilename())
	}
	activity.FilesChanged = files
	activity.Enriched = true
}

// fetchViaEventsAPI fetches GitHub events using the Events API (current implementation)
func (g *GitHubClient) fetchViaEventsAPI(ctx context.Context, username string, startDate, endDate time.Time) ([]models.GitHubActivity, error) {
	var allActivities []models.GitHubActivity

	// Fetch events with pagination
	opt := &github.ListOptions{PerPage: 100}

	for {
		var events []*github.Event
		var resp *github.Response

		// Apply rate limiting
		<-g.rateLimiter.C

		// Fetch events
		err := g.withRetry(func() error {
			var fetchErr error
			events, resp, fetchErr = g.Client.Activity.ListEventsPerformedByUser(
				ctx,
				username,
				false, // publicOnly = false (fetch all events user has access to)
				opt,
			)
			return fetchErr
		})
		if err != nil {
			return nil, fmt.Errorf("failed to fetch GitHub events: %w", err)
		}

		if config.Debug {
			config.LogDebug("Fetched %d events (page %d)", len(events), opt.Page)
			if resp != nil && resp.Rate.Limit > 0 {
				config.LogDebug("Rate limit: %d/%d remaining, resets at %v",
					resp.Rate.Remaining, resp.Rate.Limit, resp.Rate.Reset)
			}
		}

		// Process events and filter by date range
		for _, event := range events {
			// Skip if event is outside date range
			if event.CreatedAt == nil {
				continue
			}

			eventTime := event.CreatedAt.Time
			if eventTime.Before(startDate) || eventTime.After(endDate) {
				continue
			}

			// Convert GitHub event to our model
			activity, err := g.convertEvent(event)
			if err != nil {
				if config.Debug {
					config.LogDebug("Skipping event %s: %v", event.GetID(), err)
				}
				continue
			}

			allActivities = append(allActivities, activity)
		}

		// Check if we've gone past the start date (events are returned newest first)
		if len(events) > 0 {
			lastEvent := events[len(events)-1]
			if lastEvent.CreatedAt != nil && lastEvent.CreatedAt.Time.Before(startDate) {
				if config.Debug {
					config.LogDebug("Reached events before start date, stopping pagination")
				}
				break
			}
		}

		// Check for more pages
		if resp.NextPage == 0 {
			break
		}

		opt.Page = resp.NextPage
	}

	if config.Debug {
		config.LogDebug("Total GitHub activities found via Events API: %d", len(allActivities))
	}

	return allActivities, nil
}

// convertEvent converts a GitHub Event to our GitHubActivity model
func (g *GitHubClient) convertEvent(event *github.Event) (models.GitHubActivity, error) {
	activity := models.GitHubActivity{
		EventID:    event.GetID(),
		EventType:  event.GetType(),
		ActorLogin: event.GetActor().GetLogin(),
		Repository: event.GetRepo().GetName(),
		Timestamp:  event.GetCreatedAt().Time,
		Public:     event.GetPublic(),
	}

	// Parse payload to generate description and URL
	payload, err := event.ParsePayload()
	if err != nil {
		return activity, fmt.Errorf("failed to parse event payload: %w", err)
	}

	// Generate description and URL based on event type
	activity.Description, activity.URL = g.generateDescriptionAndURL(event, payload)

	return activity, nil
}

// generateDescriptionAndURL creates human-readable description and URL for an event
func (g *GitHubClient) generateDescriptionAndURL(event *github.Event, payload interface{}) (string, string) {
	repoName := event.GetRepo().GetName()
	baseURL := fmt.Sprintf("https://github.com/%s", repoName)

	switch p := payload.(type) {
	case *github.PushEvent:
		commitCount := len(p.Commits)
		branch := strings.TrimPrefix(p.GetRef(), "refs/heads/")
		desc := fmt.Sprintf("Pushed %d commit(s) to %s", commitCount, branch)
		url := fmt.Sprintf("%s/commits/%s", baseURL, branch)
		return desc, url

	case *github.PullRequestEvent:
		prNumber := p.GetNumber()
		action := p.GetAction()
		desc := fmt.Sprintf("%s PR #%d: %s", strings.Title(action), prNumber, p.GetPullRequest().GetTitle())
		url := p.GetPullRequest().GetHTMLURL()
		return desc, url

	case *github.PullRequestReviewEvent:
		prNumber := p.GetPullRequest().GetNumber()
		action := p.GetAction() // submitted, edited, dismissed
		desc := fmt.Sprintf("%s review on PR #%d", strings.Title(action), prNumber)
		url := p.GetReview().GetHTMLURL()
		return desc, url

	case *github.IssuesEvent:
		issueNumber := p.GetIssue().GetNumber()
		action := p.GetAction()
		desc := fmt.Sprintf("%s issue #%d: %s", strings.Title(action), issueNumber, p.GetIssue().GetTitle())
		url := p.GetIssue().GetHTMLURL()
		return desc, url

	case *github.IssueCommentEvent:
		issueNumber := p.GetIssue().GetNumber()
		action := p.GetAction()
		desc := fmt.Sprintf("%s comment on issue #%d", strings.Title(action), issueNumber)
		url := p.GetComment().GetHTMLURL()
		return desc, url

	case *github.CreateEvent:
		refType := p.GetRefType()
		ref := p.GetRef()
		desc := fmt.Sprintf("Created %s: %s", refType, ref)
		url := baseURL
		if refType == "branch" {
			url = fmt.Sprintf("%s/tree/%s", baseURL, ref)
		} else if refType == "tag" {
			url = fmt.Sprintf("%s/releases/tag/%s", baseURL, ref)
		}
		return desc, url

	case *github.DeleteEvent:
		refType := p.GetRefType()
		ref := p.GetRef()
		desc := fmt.Sprintf("Deleted %s: %s", refType, ref)
		url := baseURL
		return desc, url

	case *github.CommitCommentEvent:
		desc := fmt.Sprintf("Commented on commit")
		url := p.GetComment().GetHTMLURL()
		return desc, url

	case *github.WatchEvent:
		action := p.GetAction()
		desc := fmt.Sprintf("%s repository", strings.Title(action))
		url := baseURL
		return desc, url

	default:
		// Generic handling for unknown event types
		desc := fmt.Sprintf("%s event", event.GetType())
		return desc, baseURL
	}
}

// withRetry executes fn with exponential backoff, handling GitHub rate limit errors.
func (g *GitHubClient) withRetry(fn func() error) error {
	bo := cboff.NewExponentialBackOff()
	bo.InitialInterval = 2 * time.Second
	bo.MaxInterval = 60 * time.Second
	bo.MaxElapsedTime = 0 // use attempt count, not elapsed time
	boWithMax := cboff.WithMaxRetries(bo, maxRetries-1)

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 && config.Debug {
			config.LogDebug("Retry attempt %d of %d", attempt+1, maxRetries)
		}

		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		// Primary rate limit — wait until reset time, then retry immediately.
		var rateLimitErr *github.RateLimitError
		if errors.As(err, &rateLimitErr) {
			wait := time.Until(rateLimitErr.Rate.Reset.Time)
			if wait <= 0 {
				wait = time.Second
			}
			config.LogDebug("Rate limit hit: %d/%d used, waiting %v", rateLimitErr.Rate.Used, rateLimitErr.Rate.Limit, wait)
			time.Sleep(wait)
			boWithMax.Reset()
			continue
		}

		// Secondary / abuse rate limit.
		var abuseErr *github.AbuseRateLimitError
		if errors.As(err, &abuseErr) {
			wait := 60 * time.Second
			if abuseErr.RetryAfter != nil {
				wait = *abuseErr.RetryAfter
			}
			config.LogDebug("Abuse rate limit hit, waiting %v", wait)
			time.Sleep(wait)
			boWithMax.Reset()
			continue
		}

		// Generic transient error — exponential backoff.
		if attempt < maxRetries-1 {
			d := boWithMax.NextBackOff()
			if d == cboff.Stop {
				break
			}
			config.LogDebug("Request failed: %v, retrying in %v", err, d)
			time.Sleep(d)
		}
	}

	return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// fetchViaSearchAPI fetches GitHub PRs and issues using the Search API
// This API has a longer retention period (~1 year) but less detail than Events API.
// Rate limit: 30 requests/minute (slower than Events API)
// Result limit: 1000 results per query (may need to split date ranges)
func (g *GitHubClient) fetchViaSearchAPI(ctx context.Context, username string, startDate, endDate time.Time) ([]models.GitHubActivity, error) {
	if config.Debug {
		config.LogDebug("Using Search API for historical data (>90 days)")
		config.LogDebug("Search API limitations: PRs/issues only (no push events), 30 req/min")
	}

	var allActivities []models.GitHubActivity

	// Search for PRs created by user
	prs, err := g.searchPullRequests(ctx, username, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("failed to search pull requests: %w", err)
	}

	// Convert PR search results to activities
	for _, pr := range prs {
		activity := g.convertSearchResultToActivity(pr, "PullRequestEvent")
		allActivities = append(allActivities, activity)
	}

	// Search for issues created by user
	issues, err := g.searchIssues(ctx, username, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("failed to search issues: %w", err)
	}

	// Convert issue search results to activities
	for _, issue := range issues {
		activity := g.convertSearchResultToActivity(issue, "IssuesEvent")
		allActivities = append(allActivities, activity)
	}

	if config.Debug {
		config.LogDebug("Total GitHub activities found via Search API: %d (%d PRs, %d issues)",
			len(allActivities), len(prs), len(issues))
	}

	return allActivities, nil
}

// searchPullRequests searches for pull requests created by a user within a date range
func (g *GitHubClient) searchPullRequests(ctx context.Context, username string, startDate, endDate time.Time) ([]*github.Issue, error) {
	// Build search query: author:username type:pr created:DATE_RANGE
	query := fmt.Sprintf("author:%s type:pr created:%s..%s",
		username,
		startDate.Format("2006-01-02"),
		endDate.Format("2006-01-02"))

	if config.Debug {
		config.LogDebug("Search query (PRs): %s", query)
	}

	return g.executeSearchQuery(ctx, query, "pull requests")
}

// searchIssues searches for issues created by a user within a date range
func (g *GitHubClient) searchIssues(ctx context.Context, username string, startDate, endDate time.Time) ([]*github.Issue, error) {
	// Build search query: author:username type:issue created:DATE_RANGE
	// Note: This excludes PRs (PRs are technically issues in GitHub's data model)
	query := fmt.Sprintf("author:%s type:issue created:%s..%s",
		username,
		startDate.Format("2006-01-02"),
		endDate.Format("2006-01-02"))

	if config.Debug {
		config.LogDebug("Search query (issues): %s", query)
	}

	return g.executeSearchQuery(ctx, query, "issues")
}

// executeSearchQuery executes a GitHub search query with pagination
func (g *GitHubClient) executeSearchQuery(ctx context.Context, query, resultType string) ([]*github.Issue, error) {
	var allResults []*github.Issue

	// GitHub Search API options
	opt := &github.SearchOptions{
		Sort:  "created",
		Order: "desc",
		ListOptions: github.ListOptions{
			PerPage: 100, // Max 100 per page
		},
	}

	// Search API has stricter rate limits (30 req/min vs 5000 req/hour)
	// Use 2-second delay between requests to stay under limit
	searchRateLimiter := time.NewTicker(2 * time.Second)
	defer searchRateLimiter.Stop()

	for {
		// Apply rate limiting (Search API: 30 requests/minute = 1 every 2 seconds)
		<-searchRateLimiter.C

		// Execute search
		var results *github.IssuesSearchResult
		var resp *github.Response
		err := g.withRetry(func() error {
			var fetchErr error
			results, resp, fetchErr = g.Client.Search.Issues(ctx, query, opt)
			return fetchErr
		})
		if err != nil {
			return nil, fmt.Errorf("search query failed: %w", err)
		}

		if config.Debug {
			config.LogDebug("Search API: fetched %d %s (page %d, total: %d)",
				len(results.Issues), resultType, opt.Page, results.GetTotal())
			if resp != nil && resp.Rate.Limit > 0 {
				config.LogDebug("Search API rate limit: %d/%d remaining, resets at %v",
					resp.Rate.Remaining, resp.Rate.Limit, resp.Rate.Reset)
			}
		}

		// Append results
		allResults = append(allResults, results.Issues...)

		// Check if we've hit the 1000 result limit
		if len(allResults) >= 1000 {
			if config.Debug {
				config.LogDebug("WARNING: Hit Search API 1000 result limit. Results may be incomplete.")
			}
			fmt.Printf("⚠️  Warning: Search API returned 1000+ results (API limit reached)\n")
			fmt.Printf("   Consider narrowing date range for complete results\n\n")
			break
		}

		// Check for more pages
		if resp.NextPage == 0 {
			break
		}

		opt.Page = resp.NextPage
	}

	return allResults, nil
}

// convertSearchResultToActivity converts a GitHub Issue (from Search API) to GitHubActivity
// Note: GitHub's Search API returns both PRs and issues as "Issue" objects
func (g *GitHubClient) convertSearchResultToActivity(issue *github.Issue, eventType string) models.GitHubActivity {
	// Extract repository name from repository URL
	// issue.GetRepositoryURL() returns: https://api.github.com/repos/org/repo
	repoURL := issue.GetRepositoryURL()
	repoName := extractRepoNameFromURL(repoURL)

	activity := models.GitHubActivity{
		EventID:    fmt.Sprintf("%d", issue.GetID()),
		EventType:  eventType,
		ActorLogin: issue.GetUser().GetLogin(),
		Repository: repoName,
		Timestamp:  issue.GetCreatedAt().Time,
		URL:        issue.GetHTMLURL(),
		Public:     !issue.GetRepository().GetPrivate(), // Invert private flag
	}

	// Generate description based on event type
	switch eventType {
	case "PullRequestEvent":
		state := issue.GetState()
		merged := issue.PullRequestLinks != nil // PRs have this field
		if merged && state == "closed" {
			activity.Description = fmt.Sprintf("Merged PR #%d: %s", issue.GetNumber(), issue.GetTitle())
		} else {
			activity.Description = fmt.Sprintf("%s PR #%d: %s", strings.Title(state), issue.GetNumber(), issue.GetTitle())
		}

	case "IssuesEvent":
		state := issue.GetState()
		activity.Description = fmt.Sprintf("%s issue #%d: %s", strings.Title(state), issue.GetNumber(), issue.GetTitle())
	}

	return activity
}

// extractRepoNameFromURL extracts "org/repo" from a GitHub API repository URL
// Example: "https://api.github.com/repos/grindrllc/infra-terraform" -> "grindrllc/infra-terraform"
func extractRepoNameFromURL(url string) string {
	// URL format: https://api.github.com/repos/org/repo
	parts := strings.Split(url, "/repos/")
	if len(parts) == 2 {
		return parts[1]
	}
	return url // Fallback: return original URL if parsing fails
}

// fetchViaGraphQLAPI fetches aggregate GitHub contribution statistics using the GraphQL API
// This API has full retention but only returns aggregate counts (not detailed events).
// Best for multi-year queries where detailed event data isn't needed.
func (g *GitHubClient) fetchViaGraphQLAPI(ctx context.Context, username string, startDate, endDate time.Time) ([]models.GitHubActivity, error) {
	if config.Debug {
		config.LogDebug("Using GraphQL API for aggregate contribution stats (>1 year)")
		config.LogDebug("GraphQL API limitations: Aggregate counts only (no detailed events or URLs)")
	}

	// Fetch contribution statistics
	stats, err := g.queryContributionStats(ctx, username, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("failed to query contribution stats: %w", err)
	}

	// Create synthetic activity records from aggregate counts
	activities := g.createSyntheticActivities(stats, username, startDate, endDate)

	if config.Debug {
		config.LogDebug("Total GitHub activities found via GraphQL API: %d synthetic records", len(activities))
		config.LogDebug("Stats: %d commits, %d PRs, %d reviews, %d restricted",
			stats.TotalCommits, stats.TotalPRs, stats.TotalReviews, stats.RestrictedContributions)
	}

	return activities, nil
}

// ContributionStats holds aggregate contribution statistics from GraphQL API
type ContributionStats struct {
	TotalCommits            int
	TotalIssues             int
	TotalPRs                int
	TotalReviews            int
	RestrictedContributions int
	Username                string
}

// queryContributionStats queries the GitHub GraphQL API for aggregate contribution statistics
func (g *GitHubClient) queryContributionStats(ctx context.Context, username string, startDate, endDate time.Time) (*ContributionStats, error) {
	// Build GraphQL query
	// Note: GraphQL requires RFC3339 format with timezone
	query := `query($username: String!, $from: DateTime!, $to: DateTime!) {
		user(login: $username) {
			contributionsCollection(from: $from, to: $to) {
				totalCommitContributions
				totalIssueContributions
				totalPullRequestContributions
				totalPullRequestReviewContributions
				restrictedContributionsCount
			}
		}
	}`

	// Build variables
	variables := map[string]interface{}{
		"username": username,
		"from":     startDate.Format(time.RFC3339),
		"to":       endDate.Format(time.RFC3339),
	}

	if config.Debug {
		config.LogDebug("GraphQL query variables: username=%s, from=%s, to=%s",
			username, startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))
	}

	// Apply rate limiting
	<-g.rateLimiter.C

	// Execute GraphQL query
	var result struct {
		User struct {
			ContributionsCollection struct {
				TotalCommitContributions            int `json:"totalCommitContributions"`
				TotalIssueContributions             int `json:"totalIssueContributions"`
				TotalPullRequestContributions       int `json:"totalPullRequestContributions"`
				TotalPullRequestReviewContributions int `json:"totalPullRequestReviewContributions"`
				RestrictedContributionsCount        int `json:"restrictedContributionsCount"`
			} `json:"contributionsCollection"`
		} `json:"user"`
	}

	err := g.withRetry(func() error {
		// Build GraphQL request body
		requestBody := map[string]interface{}{
			"query":     query,
			"variables": variables,
		}

		// Marshal to JSON
		jsonData, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("failed to marshal GraphQL request: %w", err)
		}

		// Create HTTP request
		req, err := http.NewRequestWithContext(ctx, "POST", "https://api.github.com/graphql", bytes.NewBuffer(jsonData))
		if err != nil {
			return fmt.Errorf("failed to create GraphQL request: %w", err)
		}

		// Set headers
		req.Header.Set("Authorization", "Bearer "+g.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		// Execute request
		httpClient := &http.Client{Timeout: 30 * time.Second}
		resp, err := httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("GraphQL request failed: %w", err)
		}
		defer resp.Body.Close()

		// Check HTTP status
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GraphQL API returned status %d", resp.StatusCode)
		}

		// Parse response
		var graphqlResponse struct {
			Data   json.RawMessage `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&graphqlResponse); err != nil {
			return fmt.Errorf("failed to decode GraphQL response: %w", err)
		}

		// Check for GraphQL errors
		if len(graphqlResponse.Errors) > 0 {
			return fmt.Errorf("GraphQL query error: %s", graphqlResponse.Errors[0].Message)
		}

		// Parse the data portion into our result struct
		if err := json.Unmarshal(graphqlResponse.Data, &result); err != nil {
			return fmt.Errorf("failed to parse GraphQL data: %w", err)
		}

		if config.Debug {
			config.LogDebug("GraphQL query executed successfully")
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("GraphQL query failed: %w", err)
	}

	// Parse result
	stats := &ContributionStats{
		TotalCommits:            result.User.ContributionsCollection.TotalCommitContributions,
		TotalIssues:             result.User.ContributionsCollection.TotalIssueContributions,
		TotalPRs:                result.User.ContributionsCollection.TotalPullRequestContributions,
		TotalReviews:            result.User.ContributionsCollection.TotalPullRequestReviewContributions,
		RestrictedContributions: result.User.ContributionsCollection.RestrictedContributionsCount,
		Username:                username,
	}

	return stats, nil
}

// createSyntheticActivities converts aggregate contribution stats into synthetic GitHubActivity records
// These are not real events but represent aggregate counts for the date range
func (g *GitHubClient) createSyntheticActivities(stats *ContributionStats, username string, startDate, endDate time.Time) []models.GitHubActivity {
	var activities []models.GitHubActivity

	// Use the start date as the timestamp for all synthetic activities
	timestamp := startDate

	// Date range string for descriptions
	dateRange := fmt.Sprintf("%s to %s", startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))

	// Create synthetic activity for commits
	if stats.TotalCommits > 0 {
		activities = append(activities, models.GitHubActivity{
			EventID:     fmt.Sprintf("synthetic-commits-%s-%d", username, startDate.Unix()),
			EventType:   "AggregateCommits",
			ActorLogin:  username,
			Repository:  "aggregate-stats",
			Timestamp:   timestamp,
			Description: fmt.Sprintf("%d commits (%s)", stats.TotalCommits, dateRange),
			URL:         fmt.Sprintf("https://github.com/%s", username),
			Public:      false, // Unknown, mark as private
		})
	}

	// Create synthetic activity for PRs
	if stats.TotalPRs > 0 {
		activities = append(activities, models.GitHubActivity{
			EventID:     fmt.Sprintf("synthetic-prs-%s-%d", username, startDate.Unix()),
			EventType:   "AggregatePullRequests",
			ActorLogin:  username,
			Repository:  "aggregate-stats",
			Timestamp:   timestamp,
			Description: fmt.Sprintf("%d pull requests (%s)", stats.TotalPRs, dateRange),
			URL:         fmt.Sprintf("https://github.com/%s", username),
			Public:      false,
		})
	}

	// Create synthetic activity for reviews
	if stats.TotalReviews > 0 {
		activities = append(activities, models.GitHubActivity{
			EventID:     fmt.Sprintf("synthetic-reviews-%s-%d", username, startDate.Unix()),
			EventType:   "AggregatePullRequestReviews",
			ActorLogin:  username,
			Repository:  "aggregate-stats",
			Timestamp:   timestamp,
			Description: fmt.Sprintf("%d pull request reviews (%s)", stats.TotalReviews, dateRange),
			URL:         fmt.Sprintf("https://github.com/%s", username),
			Public:      false,
		})
	}

	// Create synthetic activity for issues
	if stats.TotalIssues > 0 {
		activities = append(activities, models.GitHubActivity{
			EventID:     fmt.Sprintf("synthetic-issues-%s-%d", username, startDate.Unix()),
			EventType:   "AggregateIssues",
			ActorLogin:  username,
			Repository:  "aggregate-stats",
			Timestamp:   timestamp,
			Description: fmt.Sprintf("%d issues (%s)", stats.TotalIssues, dateRange),
			URL:         fmt.Sprintf("https://github.com/%s", username),
			Public:      false,
		})
	}

	// Create synthetic activity for restricted contributions (private repos)
	if stats.RestrictedContributions > 0 {
		activities = append(activities, models.GitHubActivity{
			EventID:     fmt.Sprintf("synthetic-restricted-%s-%d", username, startDate.Unix()),
			EventType:   "AggregateRestrictedContributions",
			ActorLogin:  username,
			Repository:  "private-repos",
			Timestamp:   timestamp,
			Description: fmt.Sprintf("%d restricted contributions in private repositories (%s)", stats.RestrictedContributions, dateRange),
			URL:         fmt.Sprintf("https://github.com/%s", username),
			Public:      false,
		})
	}

	return activities
}
