package summary

import (
	"fmt"
	"log"
	"strings"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

// GenerateJiraSummary prints summary statistics for Jira issues to stdout
func GenerateJiraSummary(issues []models.Issue) {
	if len(issues) == 0 {
		log.Println("\n📊 Jira Summary: No issues found")
		return
	}

	// Count by status
	statusCounts := make(map[string]int)
	// Count by assignee
	assigneeCounts := make(map[string]int)
	// Count by project
	projectCounts := make(map[string]int)
	// Count by type
	typeCounts := make(map[string]int)

	for _, issue := range issues {
		status := issue.Fields.Status.Name
		if status == "" {
			status = "Unknown"
		}
		statusCounts[status]++

		assignee := issue.Assignee
		if assignee == "" {
			assignee = "Unassigned"
		}
		assigneeCounts[assignee]++

		project := issue.ProjectKey
		if project == "" {
			project = "Unknown"
		}
		projectCounts[project]++

		issueType := issue.IssueType
		if issueType == "" {
			issueType = "Unknown"
		}
		typeCounts[issueType]++
	}

	// Print summary
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Printf("📊 JIRA SUMMARY - Total Issues: %d\n", len(issues))
	fmt.Println(strings.Repeat("=", 60))

	// By Status
	fmt.Println("\nBy Status:")
	fmt.Println(strings.Repeat("-", 40))
	for status, count := range statusCounts {
		fmt.Printf("  %-30s %5d\n", status, count)
	}

	// By Project
	fmt.Println("\nBy Project:")
	fmt.Println(strings.Repeat("-", 40))
	for project, count := range projectCounts {
		fmt.Printf("  %-30s %5d\n", project, count)
	}

	// By Type
	fmt.Println("\nBy Issue Type:")
	fmt.Println(strings.Repeat("-", 40))
	for issueType, count := range typeCounts {
		fmt.Printf("  %-30s %5d\n", issueType, count)
	}

	// By Assignee
	fmt.Println("\nBy Assignee:")
	fmt.Println(strings.Repeat("-", 40))
	for assignee, count := range assigneeCounts {
		fmt.Printf("  %-30s %5d\n", assignee, count)
	}

	fmt.Println(strings.Repeat("=", 60) + "\n")
}

// GenerateConfluenceSummary prints summary statistics for Confluence articles to stdout
func GenerateConfluenceSummary(articles []models.ConfluenceArticle) {
	if len(articles) == 0 {
		log.Println("\n📊 Confluence Summary: No articles found")
		return
	}

	// Count by space
	spaceCounts := make(map[string]int)
	// Count by creator
	creatorCounts := make(map[string]int)
	// Count by last editor
	editorCounts := make(map[string]int)

	for _, article := range articles {
		space := article.SpaceName
		if space == "" {
			space = article.SpaceKey
		}
		if space == "" {
			space = "Unknown"
		}
		spaceCounts[space]++

		creator := article.Creator
		if creator == "" {
			creator = "Unknown"
		}
		creatorCounts[creator]++

		editor := article.LastEditor
		if editor == "" {
			editor = "Unknown"
		}
		editorCounts[editor]++
	}

	// Print summary
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Printf("📊 CONFLUENCE SUMMARY - Total Articles: %d\n", len(articles))
	fmt.Println(strings.Repeat("=", 60))

	// By Space
	fmt.Println("\nBy Space:")
	fmt.Println(strings.Repeat("-", 40))
	for space, count := range spaceCounts {
		fmt.Printf("  %-30s %5d\n", space, count)
	}

	// By Creator
	fmt.Println("\nBy Creator:")
	fmt.Println(strings.Repeat("-", 40))
	for creator, count := range creatorCounts {
		fmt.Printf("  %-30s %5d\n", creator, count)
	}

	// By Last Editor
	fmt.Println("\nBy Last Editor:")
	fmt.Println(strings.Repeat("-", 40))
	for editor, count := range editorCounts {
		fmt.Printf("  %-30s %5d\n", editor, count)
	}

	fmt.Println(strings.Repeat("=", 60) + "\n")
}

// GenerateGitHubSummary prints summary statistics for GitHub activities to stdout
func GenerateGitHubSummary(activities []models.GitHubActivity) {
	if len(activities) == 0 {
		log.Println("\n📊 GitHub Summary: No activities found")
		return
	}

	// Count by event type
	eventTypeCounts := make(map[string]int)
	// Count by repository
	repoCounts := make(map[string]int)
	// Count public vs private
	publicCount := 0
	privateCount := 0

	for _, activity := range activities {
		eventType := activity.EventType
		if eventType == "" {
			eventType = "Unknown"
		}
		eventTypeCounts[eventType]++

		repo := activity.Repository
		if repo == "" {
			repo = "Unknown"
		}
		repoCounts[repo]++

		if activity.Public {
			publicCount++
		} else {
			privateCount++
		}
	}

	// Print summary
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Printf("📊 GITHUB SUMMARY - Total Activities: %d\n", len(activities))
	fmt.Println(strings.Repeat("=", 60))

	// By Event Type
	fmt.Println("\nBy Event Type:")
	fmt.Println(strings.Repeat("-", 40))
	for eventType, count := range eventTypeCounts {
		fmt.Printf("  %-30s %5d\n", eventType, count)
	}

	// By Repository
	fmt.Println("\nBy Repository:")
	fmt.Println(strings.Repeat("-", 40))
	for repo, count := range repoCounts {
		fmt.Printf("  %-30s %5d\n", repo, count)
	}

	// By Visibility
	fmt.Println("\nBy Visibility:")
	fmt.Println(strings.Repeat("-", 40))
	fmt.Printf("  %-30s %5d\n", "Public", publicCount)
	fmt.Printf("  %-30s %5d\n", "Private", privateCount)

	fmt.Println(strings.Repeat("=", 60) + "\n")
}
