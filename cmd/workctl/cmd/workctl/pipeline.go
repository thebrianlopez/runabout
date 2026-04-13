package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/api"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/cache"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/config"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/insights"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/pipeline"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/ui"
)

// localActivity holds data from the no-credential local sources (EPIC-015).
type localActivity struct {
	shellCmds        []models.ShellCommand
	auditEvents      []models.AuditEvent     // from EventsClient.ShellEvents (or AuditLogClient fallback for pre-events-era)
	aiActivity       []models.AIActivity     // from ClaudeStatsClient; empty when EventsClient has session summaries
	sessionSummaries []models.SessionSummary // from EventsClient (or AuditLogClient fallback)
}

// eventsEraBoundary is the date from which the automation-metrics events store
// has data. EventsClient is primary for date ranges ending on or after this date.
// AuditLogClient/ClaudeStatsClient are used for earlier date ranges as fallback.
const eventsEraBoundary = "2026-01-23"

// fetchLocalActivity reads fish history and automation-metrics events for the given date range.
// Uses EventsClient (primary) for post-2026-01-23 data; falls back to legacy clients for earlier ranges.
// All sources fail gracefully — absent files produce empty slices, never errors.
func fetchLocalActivity(rc *config.ResolvedConfig) *localActivity {
	la := &localActivity{}

	if rc.Shell {
		cmds, _ := api.NewFishHistoryClient().GetCommands(rc.StartDate, rc.EndDate)
		la.shellCmds = cmds

		// EPIC-021 E2: use EventsClient as primary source for post-events-era data.
		// EventsClient reads each JSONL file once and returns all event types.
		batch, _ := api.NewEventsClient().GetTypedEvents(rc.StartDate, rc.EndDate)
		if batch != nil {
			// Bridge ShellEvents → AuditEvent for signal extraction until E3 lands.
			la.auditEvents = api.ShellEventsToAuditEvents(batch.ShellEvents)
			la.sessionSummaries = batch.SessionSummaries
		}

		// For date ranges that predate the events store, fall back to AuditLogClient.
		// EventsClient returns empty for absent files, so only fall back when both
		// auditEvents and sessionSummaries are empty AND the end date is before the boundary.
		if len(la.auditEvents) == 0 && len(la.sessionSummaries) == 0 && rc.EndDate < eventsEraBoundary {
			auditClient := api.NewAuditLogClient()
			events, _ := auditClient.GetEvents(rc.StartDate, rc.EndDate)
			la.auditEvents = events
			summaries, _ := auditClient.GetSessionSummaries(rc.StartDate, rc.EndDate)
			la.sessionSummaries = summaries
		}
	}

	if rc.AIStats {
		// EventsClient SessionSummaries supersede ClaudeStatsClient for the AI activity signals.
		// Fall back to ClaudeStatsClient only when no session summaries are available from events
		// (pre-events-era date ranges, or events store not yet populated).
		if len(la.sessionSummaries) == 0 {
			activity, _ := api.NewClaudeStatsClient().GetActivity(rc.StartDate, rc.EndDate)
			la.aiActivity = activity
		}
	}

	return la
}

// ReportData holds everything produced by one fetch+extract cycle.
// Commands add domain-specific fields (Delta, TrackResult) before passing to WriteReport.
type ReportData struct {
	ReportType string // "weekly" | "quarterly" | "review"
	Period     string // "2025-11-17 to 2025-11-24"
	Start      time.Time
	End        time.Time
	Generated  time.Time

	Signals *insights.SignalSet

	// Raw source data — retained for the standup publisher (EPIC-014).
	// Not written to report files; used only for HTML rendering.
	Issues     []models.Issue
	Activities []models.GitHubActivity

	// Local activity data (EPIC-015); nil slices when sources are disabled.
	ShellCommands []models.ShellCommand
	AuditEvents   []models.AuditEvent
	AIActivity    []models.AIActivity

	// Domain-specific; only one populated per report type
	Delta        *insights.DeltaReport   // quarterly
	TrackResult  *insights.TrackResult   // review, or single --track on trends
	TrackResults []*insights.TrackResult // --all-tracks on trends

	// Output
	OutputPath string
	Format     reportFormat
}

// FetchReportData runs the full fetch → extract pipeline for a single period.
// rc.StartDate and rc.EndDate must be set before calling.
func FetchReportData(ctx context.Context, rc *config.ResolvedConfig) (*ReportData, error) {
	cfg, err := config.ToQueryConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("configuration error: %w", err)
	}

	spin := ui.NewSpinner()
	issues, articles, activities, err := fetchDataForPeriod(ctx, cfg, rc, spin)
	if err != nil {
		return nil, err
	}

	// Read local sources (no spinner, no cache — all complete in <100ms).
	la := fetchLocalActivity(rc)

	// Redact third-party names before signal extraction and report rendering (opt-in).
	if rc.RedactOthers {
		issues = config.RedactOthersInIssues(issues, rc.Email, rc.AtlassianEmail)
		articles = config.RedactOthersInArticles(articles, rc.Email, rc.AtlassianEmail)
	}

	start, _ := time.Parse("2006-01-02", rc.StartDate)
	end, _ := time.Parse("2006-01-02", rc.EndDate)

	signals := insights.ExtractSignals(issues, articles, activities)
	// EPIC-021 E3: unified local-source signal extraction in one call.
	// ExtractLocalSignals is data-driven: empty slices produce empty (non-nil) structs,
	// so no flags are needed here — the data already reflects what was fetched.
	localSigs := insights.ExtractLocalSignals(la.shellCmds, la.auditEvents, la.aiActivity, la.sessionSummaries)
	if rc.Shell {
		signals.ShellActivity = localSigs.ShellActivity
		signals.SessionSignals = localSigs.SessionSignals
		signals.TopologySignals = localSigs.TopologySignals
	}
	if rc.AIStats {
		signals.AIActivity = localSigs.AIActivity
	}

	return &ReportData{
		Period:        insights.FormatPeriod(rc.StartDate, rc.EndDate),
		Start:         start,
		End:           end,
		Generated:     time.Now(),
		Signals:       signals,
		Issues:        issues,
		Activities:    activities,
		ShellCommands: la.shellCmds,
		AuditEvents:   la.auditEvents,
		AIActivity:    la.aiActivity,
	}, nil
}

// TrendSet holds the result of a multi-period N-period fetch loop.
type TrendSet struct {
	Periods    []*ReportData // one per period, oldest first
	PeriodSize string        // "3m", "1m", "7d" — for display in reports
}

// FetchTrends runs FetchReportData for each Period in order (oldest first),
// logging progress to stdout, and returns the assembled TrendSet.
func FetchTrends(ctx context.Context, rc *config.ResolvedConfig, periods []Period) (*TrendSet, error) {
	ts := &TrendSet{
		Periods: make([]*ReportData, 0, len(periods)),
	}
	spin := ui.NewSpinner()
	for i, p := range periods {
		spin.Start(fmt.Sprintf("Period %d/%d: %s (%s to %s)", i+1, len(periods), p.Label, p.Start, p.End))
		periodRC := *rc
		periodRC.StartDate = p.Start
		periodRC.EndDate = p.End
		rd, err := FetchReportData(ctx, &periodRC)
		if err != nil {
			spin.Stop("")
			return nil, fmt.Errorf("period %s: %w", p.Label, err)
		}
		// Replace the raw date-range period string with the human-readable label.
		rd.Period = p.Label
		spin.Stop(ui.SuccessSprintf("  Period %d/%d: %s ✓", i+1, len(periods), p.Label))
		ts.Periods = append(ts.Periods, rd)
	}
	return ts, nil
}

// WarmStatus describes what happened during a WarmReportData call.
type WarmStatus struct {
	JiraCached      bool
	ConfCached      bool
	GitHubCached    bool
	AnythingFetched bool
}

// WarmReportData is an incremental variant of FetchReportData.
// It checks the cache first and only fetches sources that aren't already cached.
// Returns a status indicating which sources were cached vs fetched.
func WarmReportData(ctx context.Context, rc *config.ResolvedConfig) (*WarmStatus, error) {
	cfg, err := config.ToQueryConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("configuration error: %w", err)
	}

	status := &WarmStatus{}

	// Check which sources are already cached
	if cacheStore != nil {
		if rc.Jira && cfg.Email != "" {
			// For user mode, we need the account ID to build the issue key.
			// Fall back to fetching if we can't determine the key cheaply.
			userKey := cache.JiraUserKey(cfg.Email)
			if cacheStore.HasValid(userKey) {
				// We can check the issue key if we peek the cached account ID
				data, _ := cacheStore.Get(userKey)
				if data != nil {
					var accountID string
					if err := jsonUnmarshal(data, &accountID); err == nil && accountID != "" {
						issuesKey := cache.JiraAssignedIssuesKey(accountID, cfg.StartDate, cfg.EndDate, cfg.JiraStatus, cfg.JiraType, cfg.JiraPriority)
						status.JiraCached = cacheStore.HasValid(issuesKey)
					}
				}
			}
		}
		if rc.Confluence && cfg.Email != "" {
			userKey := cache.ConfluenceUserKey(cfg.Email)
			if cacheStore.HasValid(userKey) {
				data, _ := cacheStore.Get(userKey)
				if data != nil {
					var accountID string
					if err := jsonUnmarshal(data, &accountID); err == nil && accountID != "" {
						articlesKey := cache.ConfluenceArticlesKey(accountID, cfg.StartDate, cfg.EndDate, cfg.ConfluenceType, cfg.ConfluenceHydrate)
						status.ConfCached = cacheStore.HasValid(articlesKey)
					}
				}
			}
		}
		if rc.GitHub && cfg.GitHubUser != "" {
			startDate, _ := time.Parse("2006-01-02", cfg.StartDate)
			strategyOverride := api.APIStrategy(cfg.GitHubAPIStrategy)
			if strategyOverride == "" {
				strategyOverride = api.StrategyAuto
			}
			strategy, err := api.SelectStrategy(startDate, strategyOverride)
			if err == nil {
				cacheKey, _ := cache.GitHubActivityKey(cfg.GitHubUser, string(strategy), cfg.StartDate, cfg.EndDate)
				status.GitHubCached = cacheStore.HasValid(cacheKey)
			}
		}
	}

	// If everything is cached, nothing to do
	if status.JiraCached && status.ConfCached && status.GitHubCached {
		return status, nil
	}
	if !rc.Jira && !rc.Confluence && !rc.GitHub {
		return status, nil
	}

	// Fetch (the normal path will use cache hits internally via GetOrFetch)
	status.AnythingFetched = true
	_, err = FetchReportData(ctx, rc)
	return status, err
}

// jsonUnmarshal is a thin wrapper to avoid importing encoding/json in the switch below.
var jsonUnmarshal = json.Unmarshal

// toPipelineReportData converts the cmd-local ReportData to the pipeline-level
// contract used by Sinks. Only the fields that Sinks need are copied.
func toPipelineReportData(rd *ReportData) *pipeline.ReportData {
	return &pipeline.ReportData{
		ReportType:  rd.ReportType,
		Period:      rd.Period,
		PeriodStart: rd.Start,
		PeriodEnd:   rd.End,
		Generated:   rd.Generated,
		OutputPath:  rd.OutputPath,
		Signals:     rd.Signals,
	}
}

// fetchDataForPeriod is the canonical data fetch function used by all reporting
// and comparison commands. It initialises API clients for the given ResolvedConfig
// and fetches Jira, Confluence, and GitHub data for the period defined by
// cfg.StartDate / cfg.EndDate, using the EPIC-006 cache layer for all sources.
func fetchDataForPeriod(ctx context.Context, cfg *models.QueryConfig, rc *config.ResolvedConfig, spin *ui.Spinner) ([]models.Issue, []models.ConfluenceArticle, []models.GitHubActivity, error) {
	var issues []models.Issue
	var articles []models.ConfluenceArticle
	var activities []models.GitHubActivity

	var atlassianClients *api.AtlassianClients
	var githubClient *api.GitHubClient
	var err error

	if rc.Jira || rc.Confluence {
		domain := envOrConfig(rc.AtlassianDomain, "ATLASSIAN_DOMAIN")
		email := envOrConfig(rc.AtlassianEmail, "ATLASSIAN_EMAIL")
		token := envOrConfig(rc.AtlassianToken, "ATLASSIAN_API_TOKEN")
		if domain != "" && email != "" && token != "" {
			atlassianClients, err = api.NewAtlassianClients(domain, email, token)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("initializing Atlassian: %w", err)
			}
		}
	}

	if rc.GitHub {
		githubToken := envOrConfig(rc.GitHubToken, "GITHUB_TOKEN")
		if githubToken != "" {
			githubClient, err = api.NewGitHubClient(githubToken)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("initializing GitHub: %w", err)
			}
		}
	}

	// Jira
	if rc.Jira && atlassianClients != nil {
		spin.Start("Fetching Jira...")
		switch cfg.Mode {
		case models.UserMode:
			jiraUserTTL := cache.TTLFor(cache.SourceJira, rc.CacheTTL)
			accountID, err := cache.GetOrFetch[string](cacheStore,
				cache.JiraUserKey(cfg.Email), cache.SourceJira, jiraUserTTL, cacheRefresh,
				func() (string, error) {
					return atlassianClients.GetJiraUserAccountID(cfg.Email)
				})
			if err == nil {
				issuesTTL := cache.TTLFor(cache.SourceJira, rc.CacheTTL)
				issues, _ = cache.GetOrFetch[[]models.Issue](cacheStore,
					cache.JiraAssignedIssuesKey(accountID, cfg.StartDate, cfg.EndDate, cfg.JiraStatus, cfg.JiraType, cfg.JiraPriority),
					cache.SourceJira, issuesTTL, cacheRefresh,
					func() ([]models.Issue, error) {
						return atlassianClients.GetAllAssignedIssues(accountID, cfg)
					})
			}
		case models.ProjectMode, models.MixedMode:
			issuesTTL := cache.TTLFor(cache.SourceJira, rc.CacheTTL)
			issues, _ = cache.GetOrFetch[[]models.Issue](cacheStore,
				cache.JiraProjectIssuesKey(cfg.ProjectKeys, cfg.StartDate, cfg.EndDate, cfg.JiraStatus, cfg.JiraType, cfg.JiraPriority),
				cache.SourceJira, issuesTTL, cacheRefresh,
				func() ([]models.Issue, error) {
					return atlassianClients.GetAllIssuesByProjects(cfg)
				})
		}
		if issues != nil {
			spin.Stop(ui.SuccessSprintf("  Fetched %d Jira issues for %s to %s", len(issues), cfg.StartDate, cfg.EndDate))
		}
	}

	// Confluence
	if rc.Confluence && atlassianClients != nil {
		spin.Start("Fetching Confluence...")
		switch cfg.Mode {
		case models.UserMode:
			confUserTTL := cache.TTLFor(cache.SourceConfluence, rc.CacheTTL)
			accountID, err := cache.GetOrFetch[string](cacheStore,
				cache.ConfluenceUserKey(cfg.Email), cache.SourceConfluence, confUserTTL, cacheRefresh,
				func() (string, error) {
					return atlassianClients.GetConfluenceUserAccountID(cfg.Email)
				})
			if err == nil {
				articlesTTL := cache.TTLFor(cache.SourceConfluence, rc.CacheTTL)
				articles, _ = cache.GetOrFetch[[]models.ConfluenceArticle](cacheStore,
					cache.ConfluenceArticlesKey(accountID, cfg.StartDate, cfg.EndDate, cfg.ConfluenceType, cfg.ConfluenceHydrate),
					cache.SourceConfluence, articlesTTL, cacheRefresh,
					func() ([]models.ConfluenceArticle, error) {
						return atlassianClients.GetAllConfluenceArticles(accountID, cfg)
					})
			}
		case models.SpaceMode, models.MixedMode:
			articlesTTL := cache.TTLFor(cache.SourceConfluence, rc.CacheTTL)
			articles, _ = cache.GetOrFetch[[]models.ConfluenceArticle](cacheStore,
				cache.ConfluenceSpacePagesKey(cfg.SpaceKeys, cfg.StartDate, cfg.EndDate, cfg.ConfluenceType, cfg.ConfluenceHydrate),
				cache.SourceConfluence, articlesTTL, cacheRefresh,
				func() ([]models.ConfluenceArticle, error) {
					return atlassianClients.GetAllPagesBySpaces(cfg)
				})
		}
		if articles != nil {
			spin.Stop(ui.SuccessSprintf("  Fetched %d Confluence articles for %s to %s", len(articles), cfg.StartDate, cfg.EndDate))
		}
	}

	// GitHub
	if rc.GitHub && githubClient != nil && cfg.GitHubUser != "" {
		spin.Start("Fetching GitHub...")
		startDate, _ := time.Parse("2006-01-02", cfg.StartDate)
		strategyOverride := api.APIStrategy(cfg.GitHubAPIStrategy)
		if strategyOverride == "" {
			strategyOverride = api.StrategyAuto
		}
		strategy, err := api.SelectStrategy(startDate, strategyOverride)
		if err == nil {
			cacheKey, cacheSource := cache.GitHubActivityKey(cfg.GitHubUser, string(strategy), cfg.StartDate, cfg.EndDate)
			ttl := cache.TTLFor(cacheSource, rc.CacheTTL)
			activities, _ = cache.GetOrFetch[[]models.GitHubActivity](cacheStore,
				cacheKey, cacheSource, ttl, cacheRefresh,
				func() ([]models.GitHubActivity, error) {
					return githubClient.GetUserActivity(ctx, cfg.GitHubUser, cfg)
				})
		}
		if activities != nil {
			spin.Stop(ui.SuccessSprintf("  Fetched %d GitHub activities for %s to %s", len(activities), cfg.StartDate, cfg.EndDate))
		}
	}

	return issues, articles, activities, nil
}
