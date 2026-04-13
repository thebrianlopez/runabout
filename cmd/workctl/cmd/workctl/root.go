package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/api"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/cache"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/config"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/export"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/summary"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/ui"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/version"
)

// resolved holds the resolved config populated by PersistentPreRunE.
var resolved *config.ResolvedConfig

// fileConfig holds the parsed config file (may be nil).
var fileConfig *config.FileConfig

// cacheStore holds the opened cache (nil if disabled or failed to open).
var cacheStore *cache.Store

// openCache opens the cache, enabling age encryption if WORKCTL_CACHE_PASSPHRASE is set.
func openCache(dbPath string) *cache.Store {
	if passphrase := os.Getenv("WORKCTL_CACHE_PASSPHRASE"); passphrase != "" {
		return cache.OpenWithPassphrase(dbPath, config.WorkctlConfigDir(), passphrase)
	}
	return cache.Open(dbPath)
}

// cacheRefresh is true when --refresh flag is set.
var cacheRefresh bool

func rootCmd() *cobra.Command {
	var (
		configPath string
		profile    string
		noCache    bool
	)

	ver := version.Get()

	cmd := &cobra.Command{
		Use:          "workctl",
		Short:        "Fetch and export Atlassian & GitHub work data",
		SilenceUsage: true,
		Version:      ver.String(),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Discover and load config file
			cfgPath, err := config.DiscoverConfigFile(configPath)
			if err != nil {
				return err
			}
			if cfgPath != "" {
				fileConfig, err = config.LoadConfigFile(cfgPath)
				if err != nil {
					return fmt.Errorf("loading config %s: %w", cfgPath, err)
				}
				config.LogDebug("Loaded config from %s", cfgPath)
			}

			// Build FlagValues — resilient to subcommands that don't register all flags
			flags := buildFlagValues(cmd)

			resolved, err = config.Resolve(fileConfig, profile, flags)
			if err != nil {
				return fmt.Errorf("config resolution: %w", err)
			}

			// Ensure XDG state directory exists for output files
			if err := os.MkdirAll(config.WorkctlStateDir(), 0700); err != nil {
				return fmt.Errorf("creating state directory: %w", err)
			}

			// Initialize colored output
			ui.Initialize(getBool(cmd, "no-color"))

			// Set debug state
			config.Debug = resolved.Debug

			// Initialize cache (graceful degradation: nil on failure)
			if resolved.CacheEnabled && !noCache {
				dbPath := filepath.Join(config.WorkctlCacheDir(), "cache.db")
				cacheStore = openCache(dbPath)
				if cacheStore != nil {
					config.LogDebug("Cache opened: %s", dbPath)
				} else {
					config.LogDebug("Cache unavailable (continuing without cache)")
				}
			} else {
				config.LogDebug("Cache disabled")
			}

			// Check --refresh flag
			cacheRefresh = getBool(cmd, "refresh")

			return nil
		},
		RunE: runPipeline,
	}

	// Persistent flags (available to all subcommands)
	cmd.PersistentFlags().StringVar(&configPath, "config", "", "Path to config file")
	cmd.PersistentFlags().StringVar(&profile, "profile", "", "Named profile from config file")
	cmd.PersistentFlags().Bool("debug", false, "Enable debug logging")
	cmd.PersistentFlags().BoolVar(&noCache, "no-cache", false, "Disable result cache")
	cmd.PersistentFlags().Bool("refresh", false, "Force refresh cached results")
	cmd.PersistentFlags().Bool("no-color", false, "Disable colored output")
	cmd.PersistentFlags().String("cache-ttl", "", "Override cache TTL for all sources (e.g. 4h, 30m; 0 disables cache)")
	cmd.PersistentFlags().Bool("redact-others", false, "Redact third-party names and emails from report output")

	// Shared data-fetching flags (definitions in flags.go)
	registerIdentityFlags(cmd)
	registerSourceToggleFlags(cmd)
	registerJiraFilterFlags(cmd)
	registerConfluenceFilterFlags(cmd)
	registerGitHubFilterFlags(cmd)

	// Root-specific flags (custom defaults that differ from subcommands)
	cmd.Flags().String("start", "2025-01-01", "Start date (YYYY-MM-DD)")
	cmd.Flags().String("end", "2025-02-20", "End date (YYYY-MM-DD)")
	cmd.Flags().String("timezone", "America/Chicago", "Time zone (e.g., America/Chicago)")
	cmd.Flags().String("jiraoutput", config.DefaultOutputDir()+"/jira.json", "Path to Jira output JSON file")
	cmd.Flags().String("confluenceoutput", config.DefaultOutputDir()+"/confluence.json", "Path to Confluence output JSON file")
	cmd.Flags().String("githuboutput", config.DefaultOutputDir()+"/github.json", "Path to GitHub output JSON file")
	cmd.Flags().Bool("summary", false, "Generate summary statistics")
	cmd.Flags().String("format", "json", "Output format: json")

	// --version prints the version string without running PersistentPreRunE.
	// Cobra intercepts this flag before any hook runs, so no credentials needed.
	cmd.SetVersionTemplate("{{.Version}}\n")

	// Register subcommands
	cmd.AddCommand(versionCmd())
	cmd.AddCommand(initCmd())
	cmd.AddCommand(configCmd())
	cmd.AddCommand(cacheCmd())
	cmd.AddCommand(insightsCmd())
	cmd.AddCommand(compareCmd())
	cmd.AddCommand(careerCmd())
	cmd.AddCommand(weeklyCmd())
	cmd.AddCommand(quarterlyCmd())
	cmd.AddCommand(reviewCmd())
	cmd.AddCommand(trendsCmd())
	cmd.AddCommand(workspaceCmd())
	cmd.AddCommand(eventsCmd())

	return cmd
}

// runPipeline is the main fetch-and-export pipeline (formerly main.go logic).
func runPipeline(cmd *cobra.Command, args []string) error {
	rc := resolved

	// Validate format
	if rc.Format != "json" {
		return fmt.Errorf("unsupported output format %q: use --format json", rc.Format)
	}

	// Convert to QueryConfig for API layer
	cfg, err := config.ToQueryConfig(rc)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	// Validate inputs
	if err := config.ValidateInputs(cfg.StartDate, cfg.EndDate, cfg.TimeZone); err != nil {
		return fmt.Errorf("input validation failed: %w", err)
	}

	// Validate query config
	if err := config.ValidateQueryConfig(cfg); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Guard: --github-repos requires --github to be enabled
	if len(rc.GitHubRepos) > 0 && !rc.GitHub {
		return fmt.Errorf("--github-repos requires --github to be enabled")
	}

	// Initialize debug logging to XDG state dir
	if rc.Debug {
		logPath := config.DefaultDebugLog()
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return fmt.Errorf("opening debug log at %s: %w", logPath, err)
		}
		config.DebugLogger = log.New(logFile, "DEBUG: ", log.Ldate|log.Ltime|log.Lshortfile)
	}

	// Initialize API clients
	var atlassianClients *api.AtlassianClients
	var githubClient *api.GitHubClient

	if rc.Jira || rc.Confluence {
		domain := envOrConfig(rc.AtlassianDomain, "ATLASSIAN_DOMAIN")
		email := envOrConfig(rc.AtlassianEmail, "ATLASSIAN_EMAIL")
		token := envOrConfig(rc.AtlassianToken, "ATLASSIAN_API_TOKEN")

		if domain == "" || email == "" || token == "" {
			return fmt.Errorf("Atlassian credentials required: set ATLASSIAN_DOMAIN, ATLASSIAN_EMAIL, ATLASSIAN_API_TOKEN or configure in .workctl.yaml")
		}

		atlassianClients, err = api.NewAtlassianClients(domain, email, token)
		if err != nil {
			return fmt.Errorf("failed to initialize Atlassian clients: %w", err)
		}
		config.LogDebug("Atlassian clients initialized (Jira + Confluence)")
	}

	if rc.GitHub {
		githubToken := envOrConfig(rc.GitHubToken, "GITHUB_TOKEN")
		if githubToken == "" {
			return fmt.Errorf("GITHUB_TOKEN must be set when --github is enabled")
		}

		githubClient, err = api.NewGitHubClient(githubToken)
		if err != nil {
			return fmt.Errorf("failed to initialize GitHub client: %w", err)
		}
		config.LogDebug("GitHub client initialized")
	}

	// Fetch data based on query mode
	ctx := context.Background()
	var issues []models.Issue
	var articles []models.ConfluenceArticle
	var githubActivities []models.GitHubActivity

	// Close cache on exit
	if cacheStore != nil {
		defer cacheStore.Close()
	}

	switch cfg.Mode {
	case models.UserMode:
		if rc.Jira {
			jiraUserTTL := cache.TTLFor(cache.SourceJira, rc.CacheTTL)
			accountID, err := cache.GetOrFetch[string](cacheStore,
				cache.JiraUserKey(cfg.Email), cache.SourceJira, jiraUserTTL, cacheRefresh,
				func() (string, error) {
					return atlassianClients.GetJiraUserAccountID(cfg.Email)
				})
			if err != nil {
				return fmt.Errorf("failed to get Jira account ID for %s: %w", cfg.Email, err)
			}
			config.LogDebug("Found Jira account ID: %s", accountID)

			issuesTTL := cache.TTLFor(cache.SourceJira, rc.CacheTTL)
			issues, err = cache.GetOrFetch[[]models.Issue](cacheStore,
				cache.JiraAssignedIssuesKey(accountID, cfg.StartDate, cfg.EndDate, cfg.JiraStatus, cfg.JiraType, cfg.JiraPriority),
				cache.SourceJira, issuesTTL, cacheRefresh,
				func() ([]models.Issue, error) {
					return atlassianClients.GetAllAssignedIssues(accountID, cfg)
				})
			if err != nil {
				return fmt.Errorf("failed to fetch Jira issues: %w", err)
			}
			ui.Successf("Fetched %d Jira issues\n", len(issues))
		}

		if rc.Confluence {
			confUserTTL := cache.TTLFor(cache.SourceConfluence, rc.CacheTTL)
			accountID, err := cache.GetOrFetch[string](cacheStore,
				cache.ConfluenceUserKey(cfg.Email), cache.SourceConfluence, confUserTTL, cacheRefresh,
				func() (string, error) {
					return atlassianClients.GetConfluenceUserAccountID(cfg.Email)
				})
			if err != nil {
				return fmt.Errorf("failed to get Confluence account ID for %s: %w", cfg.Email, err)
			}
			config.LogDebug("Found Confluence account ID: %s", accountID)

			articlesTTL := cache.TTLFor(cache.SourceConfluence, rc.CacheTTL)
			articles, err = cache.GetOrFetch[[]models.ConfluenceArticle](cacheStore,
				cache.ConfluenceArticlesKey(accountID, cfg.StartDate, cfg.EndDate, cfg.ConfluenceType, cfg.ConfluenceHydrate),
				cache.SourceConfluence, articlesTTL, cacheRefresh,
				func() ([]models.ConfluenceArticle, error) {
					return atlassianClients.GetAllConfluenceArticles(accountID, cfg)
				})
			if err != nil {
				return fmt.Errorf("failed to fetch Confluence articles: %w", err)
			}
			ui.Successf("Fetched %d Confluence articles\n", len(articles))
		}

	case models.ProjectMode:
		if rc.Jira {
			issuesTTL := cache.TTLFor(cache.SourceJira, rc.CacheTTL)
			issues, err = cache.GetOrFetch[[]models.Issue](cacheStore,
				cache.JiraProjectIssuesKey(cfg.ProjectKeys, cfg.StartDate, cfg.EndDate, cfg.JiraStatus, cfg.JiraType, cfg.JiraPriority),
				cache.SourceJira, issuesTTL, cacheRefresh,
				func() ([]models.Issue, error) {
					return atlassianClients.GetAllIssuesByProjects(cfg)
				})
			if err != nil {
				return fmt.Errorf("failed to fetch Jira issues: %w", err)
			}
			ui.Successf("Fetched %d Jira issues\n", len(issues))
		}

	case models.SpaceMode:
		if rc.Confluence {
			articlesTTL := cache.TTLFor(cache.SourceConfluence, rc.CacheTTL)
			articles, err = cache.GetOrFetch[[]models.ConfluenceArticle](cacheStore,
				cache.ConfluenceSpacePagesKey(cfg.SpaceKeys, cfg.StartDate, cfg.EndDate, cfg.ConfluenceType, cfg.ConfluenceHydrate),
				cache.SourceConfluence, articlesTTL, cacheRefresh,
				func() ([]models.ConfluenceArticle, error) {
					return atlassianClients.GetAllPagesBySpaces(cfg)
				})
			if err != nil {
				return fmt.Errorf("failed to fetch Confluence pages: %w", err)
			}
			ui.Successf("Fetched %d Confluence pages\n", len(articles))
		}

	case models.MixedMode:
		if rc.Jira {
			issuesTTL := cache.TTLFor(cache.SourceJira, rc.CacheTTL)
			issues, err = cache.GetOrFetch[[]models.Issue](cacheStore,
				cache.JiraProjectIssuesKey(cfg.ProjectKeys, cfg.StartDate, cfg.EndDate, cfg.JiraStatus, cfg.JiraType, cfg.JiraPriority),
				cache.SourceJira, issuesTTL, cacheRefresh,
				func() ([]models.Issue, error) {
					return atlassianClients.GetAllIssuesByProjects(cfg)
				})
			if err != nil {
				return fmt.Errorf("failed to fetch Jira issues: %w", err)
			}
			ui.Successf("Fetched %d Jira issues\n", len(issues))
		}

		if rc.Confluence {
			articlesTTL := cache.TTLFor(cache.SourceConfluence, rc.CacheTTL)
			articles, err = cache.GetOrFetch[[]models.ConfluenceArticle](cacheStore,
				cache.ConfluenceSpacePagesKey(cfg.SpaceKeys, cfg.StartDate, cfg.EndDate, cfg.ConfluenceType, cfg.ConfluenceHydrate),
				cache.SourceConfluence, articlesTTL, cacheRefresh,
				func() ([]models.ConfluenceArticle, error) {
					return atlassianClients.GetAllPagesBySpaces(cfg)
				})
			if err != nil {
				return fmt.Errorf("failed to fetch Confluence pages: %w", err)
			}
			ui.Successf("Fetched %d Confluence pages\n", len(articles))
		}

	case models.GitHubMode:
		if rc.GitHub && cfg.GitHubUser != "" {
			githubActivities, err = fetchGitHubCached(ctx, githubClient, cfg, rc)
			if err != nil {
				return fmt.Errorf("failed to fetch GitHub activities: %w", err)
			}
			ui.Successf("Fetched %d GitHub activities\n", len(githubActivities))
		}

	default:
		return fmt.Errorf("invalid query mode: %v", cfg.Mode)
	}

	// Fetch GitHub data if enabled and not in GitHub-only mode
	if rc.GitHub && cfg.GitHubUser != "" && cfg.Mode != models.GitHubMode {
		githubActivities, err = fetchGitHubCached(ctx, githubClient, cfg, rc)
		if err != nil {
			return fmt.Errorf("failed to fetch GitHub activities: %w", err)
		}
		ui.Successf("Fetched %d GitHub activities\n", len(githubActivities))
	}

	// Redact third-party names before export and summary (opt-in via --redact-others).
	if rc.RedactOthers {
		issues = config.RedactOthersInIssues(issues, rc.Email, rc.AtlassianEmail)
		articles = config.RedactOthersInArticles(articles, rc.Email, rc.AtlassianEmail)
	}

	// Export data to JSON
	if rc.Jira && len(issues) > 0 {
		if err := export.WriteToJSON(issues, rc.JiraOutput, cfg); err != nil {
			return fmt.Errorf("failed to write Jira JSON: %w", err)
		}
		ui.Successf("Wrote %d Jira issues to %s\n", len(issues), rc.JiraOutput)
	}

	if rc.Confluence && len(articles) > 0 {
		if err := export.WriteToJSON(articles, rc.ConfluenceOutput, cfg); err != nil {
			return fmt.Errorf("failed to write Confluence JSON: %w", err)
		}
		ui.Successf("Wrote %d Confluence pages to %s\n", len(articles), rc.ConfluenceOutput)
	}

	if rc.GitHub && len(githubActivities) > 0 {
		if err := export.WriteToJSON(githubActivities, rc.GitHubOutput, cfg); err != nil {
			return fmt.Errorf("failed to write GitHub JSON: %w", err)
		}
		ui.Successf("Wrote %d GitHub activities to %s\n", len(githubActivities), rc.GitHubOutput)
	}

	// Generate summary statistics if requested
	if cfg.Summary {
		ui.Headerf("\n%s\n", strings.Repeat("=", 80))
		ui.Headerf("SUMMARY STATISTICS\n")
		ui.Headerf("%s\n", strings.Repeat("=", 80))

		if len(issues) > 0 {
			ui.Headerf("\nJIRA SUMMARY:\n")
			summary.GenerateJiraSummary(issues)
		}

		if len(articles) > 0 {
			ui.Headerf("\nCONFLUENCE SUMMARY:\n")
			summary.GenerateConfluenceSummary(articles)
		}

		if len(githubActivities) > 0 {
			ui.Headerf("\nGITHUB SUMMARY:\n")
			summary.GenerateGitHubSummary(githubActivities)
		}

		ui.Headerf("\n%s\n", strings.Repeat("=", 80))
	}

	ui.Successf("\nworkctl execution complete\n")
	return nil
}

// fetchGitHubCached wraps GitHub activity fetching with cache, pre-resolving the
// API strategy to select the correct TTL and cache key.
func fetchGitHubCached(ctx context.Context, client *api.GitHubClient, cfg *models.QueryConfig, rc *config.ResolvedConfig) ([]models.GitHubActivity, error) {
	// Pre-resolve strategy so we can pick the right TTL before the cached call.
	startDate, _ := time.Parse("2006-01-02", cfg.StartDate)
	strategyOverride := api.APIStrategy(cfg.GitHubAPIStrategy)
	if strategyOverride == "" {
		strategyOverride = api.StrategyAuto
	}
	strategy, err := api.SelectStrategy(startDate, strategyOverride)
	if err != nil {
		return nil, err
	}

	cacheKey, cacheSource := cache.GitHubActivityKey(cfg.GitHubUser, string(strategy), cfg.StartDate, cfg.EndDate)
	ttl := cache.TTLFor(cacheSource, rc.CacheTTL)

	return cache.GetOrFetch[[]models.GitHubActivity](cacheStore,
		cacheKey, cacheSource, ttl, cacheRefresh,
		func() ([]models.GitHubActivity, error) {
			return client.GetUserActivity(ctx, cfg.GitHubUser, cfg)
		})
}

// envOrConfig returns the config value if set, otherwise falls back to env var.
func envOrConfig(configVal, envKey string) string {
	if configVal != "" {
		return configVal
	}
	return os.Getenv(envKey)
}
