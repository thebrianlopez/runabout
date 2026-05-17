package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/api"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ui"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/workspace"
)

func workspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"ws"},
		Short:   "Manage issue-based workspaces",
		Long:    `Create and manage workspaces tied to Jira or GitHub issues, with git repos cloned from a local cache.`,
	}

	cmd.AddCommand(workspaceInitCmd())
	return cmd
}

func workspaceInitCmd() *cobra.Command {
	var (
		dryRun  bool
		repos   []string
		force   bool
		verbose bool
		ghRepo  string
	)

	cmd := &cobra.Command{
		Use:   "init <JIRA-KEY | ISSUE-NUMBER>",
		Short: "Initialize a workspace for a Jira or GitHub issue",
		Long: `Create a workspace directory, write issue metadata, and clone repos from cache.

Jira mode (default):
  workctl workspace init ISRE-1234
  workctl workspace init ISRE-1234 -n           # dry run
  workctl workspace init ISRE-1234 -r infra-terraform,my-service

GitHub mode (-R flag):
  workctl workspace init 42 -R owner/repo        # GitHub issue #42
  workctl workspace init 100 -R your-org/my-svc # GitHub issue #100

Common flags:
  workctl ws init ISRE-1234 -f                   # force re-init`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ghRepo != "" {
				return runWorkspaceInitGitHub(args[0], ghRepo, dryRun, repos, force, verbose)
			}
			return runWorkspaceInit(args[0], dryRun, repos, force, verbose)
		},
	}

	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Preview plan without side effects")
	cmd.Flags().StringSliceVarP(&repos, "repos", "r", nil, "Override default repos (comma-separated)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Re-initialize existing workspace")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Detailed output")
	cmd.Flags().StringVarP(&ghRepo, "github-repo", "R", "", "GitHub repo (owner/repo) — switches to GitHub issue mode")

	return cmd
}

func runWorkspaceInit(key string, dryRun bool, repos []string, force, verbose bool) error {
	// Validate Jira key format
	if !workspace.IsValidJiraKey(strings.ToUpper(key)) {
		return fmt.Errorf("invalid Jira key format: %q (expected PROJECT-123)", key)
	}
	key = strings.ToUpper(key)

	rc := resolved

	// Resolve org path: config > env > error
	orgPath := rc.WorkspaceOrgPath
	if orgPath == "" {
		orgPath = os.Getenv("ORG_PATH")
	}
	if orgPath == "" {
		return fmt.Errorf("org path not configured: set workspace.org_path in config or ORG_PATH env var")
	}

	// Expand ~ in orgPath
	if strings.HasPrefix(orgPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("expanding home directory: %w", err)
		}
		orgPath = filepath.Join(home, orgPath[2:])
	}

	// Resolve repos: flag > config
	if len(repos) == 0 {
		repos = rc.WorkspaceDefaultRepos
	}

	// Validate git cache directory exists
	cachePath := filepath.Join(orgPath, rc.WorkspaceGitCacheDir)
	if !dryRun {
		if _, err := os.Stat(cachePath); os.IsNotExist(err) {
			return fmt.Errorf("git cache directory not found: %s", cachePath)
		}
	}

	// Build Atlassian client and fetch issue
	domain := envOrConfig(rc.AtlassianDomain, "ATLASSIAN_DOMAIN")
	email := envOrConfig(rc.AtlassianEmail, "ATLASSIAN_EMAIL")
	token := envOrConfig(rc.AtlassianToken, "ATLASSIAN_API_TOKEN")

	if domain == "" || email == "" || token == "" {
		return fmt.Errorf("Atlassian credentials required: set ATLASSIAN_DOMAIN, ATLASSIAN_EMAIL, ATLASSIAN_API_TOKEN or configure in .workctl.yaml")
	}

	ui.Infof("Fetching %s from Jira...\n", key)

	atlassian, err := api.NewAtlassianClients(domain, email, token)
	if err != nil {
		return fmt.Errorf("initializing Atlassian client: %w", err)
	}

	jiraIssue, err := atlassian.GetIssueByKey(key)
	if err != nil {
		return fmt.Errorf("fetching issue: %w", err)
	}

	ui.Infof("Issue: %s - %s [%s]\n\n", jiraIssue.Key, jiraIssue.Summary, jiraIssue.Status)

	return runInit(orgPath, &workspace.IssueInfo{
		Key:         jiraIssue.Key,
		Summary:     jiraIssue.Summary,
		Description: jiraIssue.Description,
		URL:         jiraIssue.URL,
		Status:      jiraIssue.Status,
		Type:        jiraIssue.Type,
	}, dryRun, repos, force, verbose)
}

func runWorkspaceInitGitHub(arg, ghRepo string, dryRun bool, repos []string, force, verbose bool) error {
	// Parse issue number
	number, err := strconv.Atoi(arg)
	if err != nil || number <= 0 {
		return fmt.Errorf("invalid GitHub issue number: %q (expected a positive integer)", arg)
	}

	// Parse owner/repo
	parts := strings.SplitN(ghRepo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid -R value: %q (expected owner/repo)", ghRepo)
	}
	owner, repo := parts[0], parts[1]

	rc := resolved

	// Resolve org path: config > env > error
	orgPath := rc.WorkspaceOrgPath
	if orgPath == "" {
		orgPath = os.Getenv("ORG_PATH")
	}
	if orgPath == "" {
		return fmt.Errorf("org path not configured: set workspace.org_path in config or ORG_PATH env var")
	}

	// Expand ~ in orgPath
	if strings.HasPrefix(orgPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("expanding home directory: %w", err)
		}
		orgPath = filepath.Join(home, orgPath[2:])
	}

	// Resolve repos: flag > config
	if len(repos) == 0 {
		repos = rc.WorkspaceDefaultRepos
	}

	// Validate git cache directory exists
	cachePath := filepath.Join(orgPath, rc.WorkspaceGitCacheDir)
	if !dryRun {
		if _, err := os.Stat(cachePath); os.IsNotExist(err) {
			return fmt.Errorf("git cache directory not found: %s", cachePath)
		}
	}

	// Build GitHub client and fetch issue
	ghToken := envOrConfig(rc.GitHubToken, "GITHUB_TOKEN")
	if ghToken == "" {
		return fmt.Errorf("GitHub token required: set GITHUB_TOKEN or configure in .workctl.yaml")
	}

	ui.Infof("Fetching %s/%s#%d from GitHub...\n", owner, repo, number)

	ghClient, err := api.NewGitHubClient(ghToken)
	if err != nil {
		return fmt.Errorf("initializing GitHub client: %w", err)
	}

	ghIssue, err := ghClient.GetGitHubIssue(context.Background(), owner, repo, number)
	if err != nil {
		return fmt.Errorf("fetching issue: %w", err)
	}

	ui.Infof("Issue: %s - %s [%s]\n\n", ghIssue.Key, ghIssue.Summary, ghIssue.Status)

	// Use a Jira-like key for folder naming: GH-<number>
	issueKey := fmt.Sprintf("GH-%d", number)

	return runInit(orgPath, &workspace.IssueInfo{
		Key:         issueKey,
		Summary:     ghIssue.Summary,
		Description: ghIssue.Description,
		URL:         ghIssue.URL,
		Status:      ghIssue.Status,
		Type:        ghIssue.Type,
	}, dryRun, repos, force, verbose)
}

// runInit is the shared Init() call used by both Jira and GitHub modes.
func runInit(orgPath string, issue *workspace.IssueInfo, dryRun bool, repos []string, force, verbose bool) error {
	rc := resolved

	opts := &workspace.InitOptions{
		OrgPath:       orgPath,
		WorkspaceDir:  rc.WorkspaceDir,
		GitCacheDir:   rc.WorkspaceGitCacheDir,
		DocsPath:      filepath.Join(orgPath, "docs"),
		Repos:         repos,
		BranchPattern: rc.WorkspaceBranchPattern,
		Issue:         issue,
		DryRun:        dryRun,
		Force:         force,
		Verbose:       verbose,
	}

	result, err := workspace.Init(opts)
	if err != nil {
		return err
	}

	if !dryRun {
		ui.Successf("\nWorkspace ready: %s\n", result.WorkspacePath)
		ui.Infof("Branch: %s\n", result.BranchName)
		fmt.Printf("\n  cd %s\n", result.WorkspacePath)
	}

	return nil
}
