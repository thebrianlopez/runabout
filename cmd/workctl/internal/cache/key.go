package cache

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

// hashKey computes a SHA-256 hex digest of the input string.
func hashKey(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}

// JiraUserKey returns a cache key for a Jira user lookup by email.
func JiraUserKey(email string) string {
	return hashKey("jira:user:" + email)
}

// JiraAssignedIssuesKey returns a cache key for assigned issues queries.
func JiraAssignedIssuesKey(accountID string, startDate, endDate string, status, types, priority []string) string {
	parts := []string{
		"jira:assigned",
		"acct=" + accountID,
		"start=" + startDate,
		"end=" + endDate,
		"status=" + sortedJoin(status),
		"type=" + sortedJoin(types),
		"priority=" + sortedJoin(priority),
	}
	return hashKey(strings.Join(parts, "|"))
}

// JiraProjectIssuesKey returns a cache key for project-based issue queries.
func JiraProjectIssuesKey(projectKeys []string, startDate, endDate string, status, types, priority []string) string {
	parts := []string{
		"jira:projects",
		"keys=" + sortedJoin(projectKeys),
		"start=" + startDate,
		"end=" + endDate,
		"status=" + sortedJoin(status),
		"type=" + sortedJoin(types),
		"priority=" + sortedJoin(priority),
	}
	return hashKey(strings.Join(parts, "|"))
}

// ConfluenceUserKey returns a cache key for a Confluence user lookup by email.
func ConfluenceUserKey(email string) string {
	return hashKey("confluence:user:" + email)
}

// ConfluenceArticlesKey returns a cache key for user-based Confluence queries.
func ConfluenceArticlesKey(accountID string, startDate, endDate, contentType string, hydrate bool) string {
	parts := []string{
		"confluence:articles",
		"acct=" + accountID,
		"start=" + startDate,
		"end=" + endDate,
		"type=" + contentType,
		fmt.Sprintf("hydrate=%t", hydrate),
	}
	return hashKey(strings.Join(parts, "|"))
}

// ConfluenceSpacePagesKey returns a cache key for space-based Confluence queries.
func ConfluenceSpacePagesKey(spaceKeys []string, startDate, endDate, contentType string, hydrate bool) string {
	parts := []string{
		"confluence:spaces",
		"keys=" + sortedJoin(spaceKeys),
		"start=" + startDate,
		"end=" + endDate,
		"type=" + contentType,
		fmt.Sprintf("hydrate=%t", hydrate),
	}
	return hashKey(strings.Join(parts, "|"))
}

// GitHubActivityKey returns a cache key and source for GitHub activity queries.
// The source varies by strategy for correct TTL selection.
func GitHubActivityKey(username, strategy, startDate, endDate string) (key, source string) {
	parts := []string{
		"github:activity",
		"user=" + username,
		"strategy=" + strategy,
		"start=" + startDate,
		"end=" + endDate,
	}
	key = hashKey(strings.Join(parts, "|"))

	switch strategy {
	case "events":
		source = SourceGitHubEvents
	case "search":
		source = SourceGitHubSearch
	case "graphql":
		source = SourceGitHubGraphQL
	default:
		source = SourceGitHubEvents
	}
	return key, source
}

// sortedJoin sorts a copy of the slice and joins with commas for deterministic keys.
func sortedJoin(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	cp := make([]string, len(ss))
	copy(cp, ss)
	sort.Strings(cp)
	return strings.Join(cp, ",")
}
