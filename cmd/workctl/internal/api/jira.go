package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ctreminiom/go-atlassian/v2/pkg/infra/models"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/config"
	localModels "github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// JiraIssueInfo holds the essential fields of a Jira issue for workspace init.
type JiraIssueInfo struct {
	Key         string
	Summary     string
	Description string // HTML-rendered description from Jira
	URL         string
	Status      string
	Type        string
}

// GetIssueByKey fetches a single Jira issue by its key (e.g. ISRE-1234).
func (a *AtlassianClients) GetIssueByKey(key string) (*JiraIssueInfo, error) {
	var issue *models.IssueScheme
	var response *models.ResponseScheme

	err := a.withRateLimitAndRetry(func() error {
		ctx := context.Background()
		var err error
		issue, response, err = a.Jira.Issue.Get(ctx, key, []string{"summary", "status", "issuetype", "description"}, []string{"renderedFields"})
		return err
	})

	if err != nil {
		if response != nil && config.Debug {
			config.LogDebug("API returned status %d: %s", response.Code, response.Endpoint)
		}
		return nil, fmt.Errorf("failed to fetch issue %s: %w", key, err)
	}

	info := &JiraIssueInfo{
		Key: issue.Key,
		URL: fmt.Sprintf("https://%s/browse/%s", a.domain, issue.Key),
	}
	if issue.Fields != nil {
		info.Summary = issue.Fields.Summary
		if issue.Fields.Status != nil {
			info.Status = issue.Fields.Status.Name
		}
		if issue.Fields.IssueType != nil {
			info.Type = issue.Fields.IssueType.Name
		}
	}

	// Extract HTML-rendered description from renderedFields expand
	if issue.RenderedFields != nil {
		if desc, ok := issue.RenderedFields["description"]; ok {
			if html, ok := desc.(string); ok {
				info.Description = html
			}
		}
	}

	return info, nil
}

// GetAllAssignedIssues retrieves all issues assigned to a specific user within a given date range
func (a *AtlassianClients) GetAllAssignedIssues(accountID string, cfg *localModels.QueryConfig) ([]localModels.Issue, error) {
	jql := fmt.Sprintf(`assignee=%s AND (
		created >= '%s' AND created <= '%s' OR
		updated >= '%s' AND updated <= '%s' OR
		resolutiondate >= '%s' AND resolutiondate <= '%s'
	)`, accountID,
		cfg.StartDate, cfg.EndDate,
		cfg.StartDate, cfg.EndDate,
		cfg.StartDate, cfg.EndDate)

	// Add filter clauses
	jql += config.BuildJQLFilters(cfg)

	// Clean up the JQL by removing newlines and extra spaces
	jql = strings.Join(strings.Fields(jql), " ")

	if config.Debug {
		config.LogDebug("Fetching all issues with JQL: %s", jql)
	}

	var result *models.IssueSearchJQLScheme
	var response *models.ResponseScheme

	err := a.withRateLimitAndRetry(func() error {
		ctx := context.Background()
		var err error
		result, response, err = a.Jira.Issue.Search.SearchJQL(
			ctx,
			jql,
			[]string{"summary", "created", "resolutiondate", "status", "updated"},
			[]string{},
			1000, // maxResults
			"",   // nextPageToken
		)
		return err
	})

	if err != nil {
		if response != nil && config.Debug {
			config.LogDebug("API returned status %d: %s", response.Code, response.Endpoint)
		}
		return nil, fmt.Errorf("failed to fetch issues: %w", err)
	}

	if config.Debug {
		config.LogDebug("Retrieved %d issues in single request", len(result.Issues))
	}

	// Convert from go-atlassian models to our Issue struct for CSV export
	issues := make([]localModels.Issue, len(result.Issues))
	for i, issue := range result.Issues {
		projectKey := ""
		if parts := strings.SplitN(issue.Key, "-", 2); len(parts) == 2 {
			projectKey = parts[0]
		}
		issues[i] = localModels.Issue{
			ID:         issue.ID,
			Key:        issue.Key,
			ProjectKey: projectKey,
			URL:        fmt.Sprintf("https://%s/browse/%s", a.domain, issue.Key),
		}
		issues[i].Fields.Summary = issue.Fields.Summary

		// Extract assignee account ID
		if issue.Fields.Assignee != nil {
			issues[i].AssigneeAccountID = issue.Fields.Assignee.AccountID
		}

		// Extract issue type
		if issue.Fields.IssueType != nil {
			issues[i].IssueType = issue.Fields.IssueType.Name
		}

		// Convert DateTimeScheme to RFC3339 string
		if issue.Fields.Created != nil {
			issues[i].Fields.Created = time.Time(*issue.Fields.Created).Format(time.RFC3339)
		}
		if issue.Fields.Updated != nil {
			issues[i].Fields.Updated = time.Time(*issue.Fields.Updated).Format(time.RFC3339)
		}

		// Handle optional resolutiondate field
		if issue.Fields.Resolutiondate != nil {
			issues[i].Fields.Resolved = time.Time(*issue.Fields.Resolutiondate).Format(time.RFC3339)
		}

		// Handle status
		if issue.Fields.Status != nil {
			issues[i].Fields.Status.Name = issue.Fields.Status.Name
		}
	}

	return issues, nil
}

// GetAllIssuesByProjects retrieves all issues in specified projects within a date range
func (a *AtlassianClients) GetAllIssuesByProjects(cfg *localModels.QueryConfig) ([]localModels.Issue, error) {
	// Build JQL: project in (KEY1, KEY2, KEY3) AND (created OR updated OR resolved)
	projectList := strings.Join(cfg.ProjectKeys, ", ")
	jql := fmt.Sprintf(`project in (%s) AND (
		created >= '%s' AND created <= '%s' OR
		updated >= '%s' AND updated <= '%s' OR
		resolutiondate >= '%s' AND resolutiondate <= '%s'
	)`, projectList,
		cfg.StartDate, cfg.EndDate,
		cfg.StartDate, cfg.EndDate,
		cfg.StartDate, cfg.EndDate)

	// Add filter clauses
	jql += config.BuildJQLFilters(cfg)

	// Clean up the JQL by removing newlines and extra spaces
	jql = strings.Join(strings.Fields(jql), " ")

	if config.Debug {
		config.LogDebug("Fetching issues by projects with JQL: %s", jql)
	}

	var result *models.IssueSearchJQLScheme
	var response *models.ResponseScheme

	err := a.withRateLimitAndRetry(func() error {
		ctx := context.Background()
		var err error
		result, response, err = a.Jira.Issue.Search.SearchJQL(
			ctx,
			jql,
			[]string{"summary", "created", "resolutiondate", "status", "updated", "project", "assignee"},
			[]string{},
			1000, // maxResults
			"",   // nextPageToken
		)
		return err
	})

	if err != nil {
		if response != nil && config.Debug {
			config.LogDebug("API returned status %d: %s", response.Code, response.Endpoint)
		}
		return nil, fmt.Errorf("failed to fetch issues by projects: %w", err)
	}

	if config.Debug {
		config.LogDebug("Retrieved %d issues from %d projects", len(result.Issues), len(cfg.ProjectKeys))
	}

	// Convert to Issue struct with project and assignee information
	issues := make([]localModels.Issue, len(result.Issues))
	for i, issue := range result.Issues {
		issues[i] = localModels.Issue{
			ID:  issue.ID,
			Key: issue.Key,
			URL: fmt.Sprintf("https://%s/browse/%s", a.domain, issue.Key),
		}

		// Extract project key
		if issue.Fields.Project != nil {
			issues[i].ProjectKey = issue.Fields.Project.Key
		}

		// Extract assignee information
		if issue.Fields.Assignee != nil {
			issues[i].Assignee = issue.Fields.Assignee.DisplayName
			issues[i].AssigneeEmail = issue.Fields.Assignee.EmailAddress
			issues[i].AssigneeAccountID = issue.Fields.Assignee.AccountID
		}

		// Extract issue type
		if issue.Fields.IssueType != nil {
			issues[i].IssueType = issue.Fields.IssueType.Name
		}

		issues[i].Fields.Summary = issue.Fields.Summary

		// Convert DateTimeScheme to RFC3339 string
		if issue.Fields.Created != nil {
			issues[i].Fields.Created = time.Time(*issue.Fields.Created).Format(time.RFC3339)
		}
		if issue.Fields.Updated != nil {
			issues[i].Fields.Updated = time.Time(*issue.Fields.Updated).Format(time.RFC3339)
		}

		// Handle optional resolutiondate field
		if issue.Fields.Resolutiondate != nil {
			issues[i].Fields.Resolved = time.Time(*issue.Fields.Resolutiondate).Format(time.RFC3339)
		}

		// Handle status
		if issue.Fields.Status != nil {
			issues[i].Fields.Status.Name = issue.Fields.Status.Name
		}
	}

	return issues, nil
}
