package ai

import (
	"math"
	"strings"
)

// computeMetrics aggregates statistics across all parsed exports.
func computeMetrics(exports []*ParsedExport, dateRange DateRange) PerformanceMetrics {
	pm := PerformanceMetrics{
		DateRange:   dateRange,
		DataQuality: assessDataQuality(exports),
	}

	for _, exp := range exports {
		switch exp.Source {
		case SourceJira:
			pm.Jira = computeJiraMetrics(exp)
		case SourceConfluence:
			pm.Confluence = computeConfluenceMetrics(exp)
		case SourceGitHub:
			pm.GitHub = computeGitHubMetrics(exp)
		}
	}

	return pm
}

// computeJiraMetrics calculates aggregate statistics for Jira issues.
// Returns nil when there are zero issues.
func computeJiraMetrics(exp *ParsedExport) *JiraMetrics {
	if len(exp.Jira) == 0 {
		return nil
	}

	m := &JiraMetrics{
		TotalIssues: len(exp.Jira),
		ByStatus:    make(map[string]int),
		ByType:      make(map[string]int),
		ByProject:   make(map[string]int),
	}

	var totalResolutionDays float64
	var resolvedWithDates int

	for _, issue := range exp.Jira {
		// Status
		status := issue.Fields.Status.Name
		if status == "" {
			status = "Unknown"
		}
		m.ByStatus[status]++

		// Type
		issueType := issue.IssueType
		if issueType == "" {
			issueType = "Unknown"
		}
		m.ByType[issueType]++

		// Project - extract from Key prefix when ProjectKey is empty
		project := issue.ProjectKey
		if project == "" {
			if idx := strings.LastIndex(issue.Key, "-"); idx > 0 {
				project = issue.Key[:idx]
			}
		}
		if project == "" {
			project = "Unknown"
		}
		m.ByProject[project]++

		// Resolution tracking
		if issue.Fields.Resolved != "" {
			m.ResolvedCount++

			if issue.Fields.Created != "" {
				created, errC := parseTimestamp(issue.Fields.Created, jiraTimestampFormats)
				resolved, errR := parseTimestamp(issue.Fields.Resolved, jiraTimestampFormats)
				if errC == nil && errR == nil {
					days := resolved.Sub(created).Hours() / 24
					totalResolutionDays += days
					resolvedWithDates++
				}
			}
		}
	}

	if resolvedWithDates > 0 {
		m.MeanResolutionDays = math.Round(totalResolutionDays/float64(resolvedWithDates)*100) / 100
	}

	return m
}

// computeConfluenceMetrics calculates aggregate statistics for Confluence articles.
// Returns nil when there are zero articles.
func computeConfluenceMetrics(exp *ParsedExport) *ConfluenceMetrics {
	if len(exp.Confluence) == 0 {
		return nil
	}

	m := &ConfluenceMetrics{
		TotalArticles: len(exp.Confluence),
		BySpace:       make(map[string]int),
	}

	for _, article := range exp.Confluence {
		space := article.SpaceName
		if space == "" {
			space = article.SpaceKey
		}
		if space == "" {
			space = "Unknown"
		}
		m.BySpace[space]++

		// Count created vs edited based on whether dates differ
		if article.LastModifiedDate != "" && article.CreatedDate != "" && article.LastModifiedDate != article.CreatedDate {
			m.EditedCount++
		} else {
			m.CreatedCount++
		}
	}

	m.UniqueSpaces = len(m.BySpace)

	return m
}

// computeGitHubMetrics calculates aggregate statistics for GitHub activities.
// Returns nil when there are zero activities.
func computeGitHubMetrics(exp *ParsedExport) *GitHubMetrics {
	if len(exp.GitHub) == 0 {
		return nil
	}

	m := &GitHubMetrics{
		TotalActivities: len(exp.GitHub),
		ByEventType:     make(map[string]int),
		ByRepo:          make(map[string]int),
	}

	uniqueRepos := make(map[string]bool)

	for _, activity := range exp.GitHub {
		eventType := activity.EventType
		if eventType == "" {
			eventType = "Unknown"
		}
		m.ByEventType[eventType]++

		repo := activity.Repository
		if repo == "" {
			repo = "Unknown"
		}
		m.ByRepo[repo]++

		// Track unique repos, excluding synthetic
		isSynthetic := strings.HasPrefix(activity.EventType, "Aggregate")
		if !isSynthetic && repo != "Unknown" {
			uniqueRepos[repo] = true
		}

		// Categorize by event type
		switch eventType {
		case "PullRequestEvent":
			// Count as opened (merged is a subset we can't distinguish from event type alone)
			m.PRsOpened++
		case "PullRequestReviewEvent":
			m.PRsReviewed++
		case "PushEvent":
			m.CommitsCount++
		}

		// Detect merged PRs from description
		if eventType == "PullRequestEvent" && strings.Contains(strings.ToLower(activity.Description), "merged") {
			m.PRsMerged++
		}
	}

	m.UniqueRepos = len(uniqueRepos)

	return m
}

// assessDataQuality evaluates the completeness and reliability of the data.
func assessDataQuality(exports []*ParsedExport) DataQuality {
	dq := DataQuality{}

	sourcePresent := make(map[Source]bool)
	for _, exp := range exports {
		sourcePresent[exp.Source] = true

		// Check for synthetic data in GitHub
		if exp.Source == SourceGitHub {
			for _, a := range exp.GitHub {
				if strings.HasPrefix(a.EventType, "Aggregate") {
					dq.SyntheticData = true
					break
				}
			}
		}
	}

	allSources := []Source{SourceJira, SourceConfluence, SourceGitHub}
	for _, s := range allSources {
		if sourcePresent[s] {
			dq.SourcesPresent = append(dq.SourcesPresent, s)
		} else {
			dq.SourcesMissing = append(dq.SourcesMissing, s)
		}
	}

	dq.PartialData = len(dq.SourcesMissing) > 0

	if dq.PartialData {
		dq.Warnings = append(dq.Warnings, "not all data sources are present")
	}
	if dq.SyntheticData {
		dq.Warnings = append(dq.Warnings, "github data contains synthetic aggregate entries")
	}

	return dq
}
