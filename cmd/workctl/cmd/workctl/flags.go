package main

import (
	"github.com/spf13/cobra"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/config"
)

// --- Low-level flag helpers ---

// getString safely gets a string flag value (returns "" for unregistered flags).
func getString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

// getBool safely gets a bool flag value (returns false for unregistered flags).
func getBool(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}

// getStringIfExists returns the flag value and whether it was explicitly set.
// For unregistered flags it returns ("", false), making PersistentPreRunE
// resilient to subcommands that don't register all data-fetching flags.
func getStringIfExists(cmd *cobra.Command, name string) (string, bool) {
	if cmd.Flags().Lookup(name) == nil {
		return "", false
	}
	v, _ := cmd.Flags().GetString(name)
	return v, cmd.Flags().Changed(name)
}

// getBoolIfExists returns the flag value and whether it was explicitly set.
// For unregistered flags it returns (false, false).
func getBoolIfExists(cmd *cobra.Command, name string) (bool, bool) {
	if cmd.Flags().Lookup(name) == nil {
		return false, false
	}
	v, _ := cmd.Flags().GetBool(name)
	return v, cmd.Flags().Changed(name)
}

// --- Shared flag registration helpers ---
//
// Flag descriptions and defaults match root.go. Commands that need
// different defaults (e.g. root's --start "2025-01-01") register
// those flags inline instead of using the shared helper.

// registerIdentityFlags registers: --email, --project-keys, --space-keys,
// --github-user, --github-api.
func registerIdentityFlags(cmd *cobra.Command) {
	cmd.Flags().String("email", "", "Jira user email address")
	cmd.Flags().String("project-keys", "", "Comma-separated Jira project keys (e.g., SR,ISRE)")
	cmd.Flags().String("space-keys", "", "Comma-separated Confluence space keys (e.g., ENG,INFRA)")
	cmd.Flags().String("github-user", "", "GitHub username")
	cmd.Flags().String("github-api", "auto", "GitHub API strategy: auto|events|search|graphql")
}

// registerDateFlags registers: --start, --timezone.
// NOTE: --end is NOT included because several commands use custom descriptions
// for it (e.g. "default: today", "most recent period"). Each command
// registers --end itself.
func registerDateFlags(cmd *cobra.Command) {
	cmd.Flags().String("start", "", "Start date (YYYY-MM-DD)")
	cmd.Flags().String("timezone", "America/Chicago", "Time zone (e.g., America/Chicago)")
}

// registerSourceToggleFlags registers: --jira, --confluence, --github, --shell, --ai-stats.
func registerSourceToggleFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("jira", true, "Enable Jira issues")
	cmd.Flags().Bool("confluence", true, "Enable Confluence articles")
	cmd.Flags().Bool("github", true, "Enable GitHub activity")
	cmd.Flags().Bool("shell", true, "Enable fish history + terminal audit log (local, no credentials)")
	cmd.Flags().Bool("ai-stats", true, "Enable Claude Code stats cache (local, no credentials)")
}

// registerJiraFilterFlags registers: --jira-status, --jira-type, --jira-priority.
func registerJiraFilterFlags(cmd *cobra.Command) {
	cmd.Flags().String("jira-status", "", "Filter Jira issues by status (e.g., Done,In Progress)")
	cmd.Flags().String("jira-type", "", "Filter Jira issues by type (e.g., Story,Bug)")
	cmd.Flags().String("jira-priority", "", "Filter Jira issues by priority (e.g., High,Critical)")
}

// registerConfluenceFilterFlags registers: --confluence-type, --confluence-hydrate.
func registerConfluenceFilterFlags(cmd *cobra.Command) {
	cmd.Flags().String("confluence-type", "page", "Filter Confluence content type (page, blogpost)")
	cmd.Flags().Bool("confluence-hydrate", false, "Enable metadata hydration (slower but accurate)")
}

// registerGitHubFilterFlags registers: --github-repos, --github-enrich.
func registerGitHubFilterFlags(cmd *cobra.Command) {
	cmd.Flags().String("github-repos", "", "Comma-separated repos for commit history (e.g. org/repo1,org/repo2)")
	cmd.Flags().Bool("github-enrich", false, "Hydrate commits with per-file diff stats (slower)")
}

// registerPrivacyFlags registers: --redact-others.
func registerPrivacyFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("redact-others", false, "Redact third-party names and emails from report output")
}

// registerAllFetchFlags is a convenience that registers all shared data-fetching
// flags (identity, date, source toggles, and all filter flags).
// NOTE: Does NOT register --end — each command must add --end itself.
func registerAllFetchFlags(cmd *cobra.Command) {
	registerIdentityFlags(cmd)
	registerDateFlags(cmd)
	registerSourceToggleFlags(cmd)
	registerJiraFilterFlags(cmd)
	registerConfluenceFilterFlags(cmd)
	registerGitHubFilterFlags(cmd)
	registerPrivacyFlags(cmd)
}

// registerReportOutputFlags registers: --output, --format.
// The defaultFormat parameter sets the format flag's default value
// (e.g. "md" for report subcommands, "json" for root/career/compare/insights).
func registerReportOutputFlags(cmd *cobra.Command, defaultFormat string) {
	cmd.Flags().String("output", "", "Output file path")
	cmd.Flags().String("format", defaultFormat, "Output format: md|json|pdf")
}

// --- FlagValues builder ---

// buildFlagValues constructs a FlagValues struct from the command's registered
// flags. Flags that are not registered on the command are safely skipped
// (value stays zero, Set stays false). This makes PersistentPreRunE resilient
// to subcommands that don't register all flags (e.g. version, cache, init).
func buildFlagValues(cmd *cobra.Command) *config.FlagValues {
	fv := &config.FlagValues{}

	// Identity
	fv.Email, fv.EmailSet = getStringIfExists(cmd, "email")
	fv.ProjectKeys, fv.ProjectKeysSet = getStringIfExists(cmd, "project-keys")
	fv.SpaceKeys, fv.SpaceKeysSet = getStringIfExists(cmd, "space-keys")
	fv.GitHubUser, fv.GitHubUserSet = getStringIfExists(cmd, "github-user")
	fv.GitHubAPIStrategy, fv.GitHubAPISet = getStringIfExists(cmd, "github-api")

	// Dates
	fv.StartDate, fv.StartDateSet = getStringIfExists(cmd, "start")
	fv.EndDate, fv.EndDateSet = getStringIfExists(cmd, "end")
	fv.TimeZone, fv.TimeZoneSet = getStringIfExists(cmd, "timezone")

	// Output paths (root-only)
	fv.JiraOutput, fv.JiraOutputSet = getStringIfExists(cmd, "jiraoutput")
	fv.ConfluenceOutput, fv.ConfOutputSet = getStringIfExists(cmd, "confluenceoutput")
	fv.GitHubOutput, fv.GitHubOutputSet = getStringIfExists(cmd, "githuboutput")

	// Format & summary
	fv.Format, fv.FormatSet = getStringIfExists(cmd, "format")
	fv.Summary, fv.SummarySet = getBoolIfExists(cmd, "summary")

	// Debug (persistent flag — always available)
	fv.Debug, fv.DebugSet = getBoolIfExists(cmd, "debug")

	// Source toggles
	fv.Jira, fv.JiraSet = getBoolIfExists(cmd, "jira")
	fv.Confluence, fv.ConfluenceSet = getBoolIfExists(cmd, "confluence")
	fv.GitHub, fv.GitHubSet = getBoolIfExists(cmd, "github")
	fv.Shell, fv.ShellSet = getBoolIfExists(cmd, "shell")
	fv.AIStats, fv.AIStatsSet = getBoolIfExists(cmd, "ai-stats")

	// Jira filters
	fv.JiraStatus, fv.JiraStatusSet = getStringIfExists(cmd, "jira-status")
	fv.JiraType, fv.JiraTypeSet = getStringIfExists(cmd, "jira-type")
	fv.JiraPriority, fv.JiraPrioritySet = getStringIfExists(cmd, "jira-priority")

	// Confluence filters
	fv.ConfluenceType, fv.ConfTypeSet = getStringIfExists(cmd, "confluence-type")
	fv.ConfluenceHydrate, fv.ConfHydrateSet = getBoolIfExists(cmd, "confluence-hydrate")

	// GitHub filters
	fv.GitHubRepos, fv.GitHubReposSet = getStringIfExists(cmd, "github-repos")
	fv.GitHubEnrich, fv.GitHubEnrichSet = getBoolIfExists(cmd, "github-enrich")

	// Cache
	fv.CacheTTLOverride, fv.CacheTTLSet = getStringIfExists(cmd, "cache-ttl")

	// Privacy
	fv.RedactOthers, fv.RedactOthersSet = getBoolIfExists(cmd, "redact-others")

	return fv
}
