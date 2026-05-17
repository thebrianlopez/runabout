package config

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

var (
	Debug       bool
	DebugLogger *log.Logger
)

// ParseCSV splits a comma-separated string and trims whitespace
func ParseCSV(input string) []string {
	if input == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// ParseGitHubRepo validates and splits an "owner/repo" string.
// Returns an error if the format is invalid (must contain exactly one "/" and non-empty parts).
func ParseGitHubRepo(s string) (owner, repo string, err error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo format %q: expected owner/repo (e.g. myorg/myrepo)", s)
	}
	return parts[0], parts[1], nil
}

// ValidateGitHubRepos validates every entry in a list of "owner/repo" strings.
// Returns the first validation error encountered.
func ValidateGitHubRepos(repos []string) error {
	for _, r := range repos {
		if _, _, err := ParseGitHubRepo(r); err != nil {
			return err
		}
	}
	return nil
}

// DetermineQueryMode analyzes flags and returns the appropriate query mode
func DetermineQueryMode(email, projectKeys, spaceKeys, githubUser string) (models.QueryMode, error) {
	hasEmail := email != ""
	hasProjects := projectKeys != ""
	hasSpaces := spaceKeys != ""
	hasGitHub := githubUser != ""

	// Must specify at least one input mode
	if !hasEmail && !hasProjects && !hasSpaces && !hasGitHub {
		return 0, errors.New("must specify --email, --project-keys, --space-keys, or --github-user")
	}

	// GitHub-only mode
	if hasGitHub && !hasEmail && !hasProjects && !hasSpaces {
		return models.GitHubMode, nil
	}

	// Cannot mix user mode with project/space mode
	if hasEmail && (hasProjects || hasSpaces) {
		return 0, errors.New("cannot use --email with --project-keys or --space-keys")
	}

	// Determine mode (note: GitHub can be combined with other modes)
	if hasEmail {
		return models.UserMode, nil
	}
	if hasProjects && hasSpaces {
		return models.MixedMode, nil
	}
	if hasProjects {
		return models.ProjectMode, nil
	}
	if hasSpaces {
		return models.SpaceMode, nil
	}

	// Should not reach here
	return 0, errors.New("invalid mode configuration")
}

// IsValidProjectKey checks if a project key matches Jira's format
func IsValidProjectKey(key string) bool {
	// Jira project keys: 2-10 uppercase letters
	if len(key) < 2 || len(key) > 10 {
		return false
	}
	for _, c := range key {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// IsValidSpaceKey checks if a space key is valid.
// Only alphanumeric characters, underscores, and hyphens are allowed
// to prevent CQL injection through crafted space key values.
func IsValidSpaceKey(key string) bool {
	if len(key) == 0 || len(key) > 255 {
		return false
	}
	for _, c := range key {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// ValidateQueryConfig validates project keys and space keys format
func ValidateQueryConfig(config *models.QueryConfig) error {
	// Validate project keys format (2-10 uppercase letters)
	for _, key := range config.ProjectKeys {
		if !IsValidProjectKey(key) {
			return fmt.Errorf("invalid project key format: %s (expected 2-10 uppercase letters)", key)
		}
	}

	// Validate space keys format (1-255 alphanumeric characters)
	for _, key := range config.SpaceKeys {
		if !IsValidSpaceKey(key) {
			return fmt.Errorf("invalid space key format: %s", key)
		}
	}

	return nil
}

// escapeJQLValue escapes double quotes in a JQL string value to prevent injection.
func escapeJQLValue(val string) string {
	return strings.ReplaceAll(val, `"`, `\"`)
}

// BuildJQLFilters builds JQL filter clauses from config
func BuildJQLFilters(config *models.QueryConfig) string {
	var filters []string

	// Status filter
	if len(config.JiraStatus) > 0 {
		statusList := make([]string, len(config.JiraStatus))
		for i, status := range config.JiraStatus {
			statusList[i] = fmt.Sprintf("\"%s\"", escapeJQLValue(status))
		}
		filters = append(filters, fmt.Sprintf("status in (%s)", strings.Join(statusList, ", ")))
	}

	// Type filter
	if len(config.JiraType) > 0 {
		typeList := make([]string, len(config.JiraType))
		for i, issueType := range config.JiraType {
			typeList[i] = fmt.Sprintf("\"%s\"", escapeJQLValue(issueType))
		}
		filters = append(filters, fmt.Sprintf("type in (%s)", strings.Join(typeList, ", ")))
	}

	// Priority filter
	if len(config.JiraPriority) > 0 {
		priorityList := make([]string, len(config.JiraPriority))
		for i, priority := range config.JiraPriority {
			priorityList[i] = fmt.Sprintf("\"%s\"", escapeJQLValue(priority))
		}
		filters = append(filters, fmt.Sprintf("priority in (%s)", strings.Join(priorityList, ", ")))
	}

	if len(filters) == 0 {
		return ""
	}

	return " AND " + strings.Join(filters, " AND ")
}

// BuildCQLFilters builds CQL filter clauses from config
func BuildCQLFilters(config *models.QueryConfig) string {
	var filters []string

	// Type filter (page vs blogpost)
	if config.ConfluenceType != "" && config.ConfluenceType != "page" {
		filters = append(filters, fmt.Sprintf("type = \"%s\"", escapeJQLValue(config.ConfluenceType)))
	}

	if len(filters) == 0 {
		return ""
	}

	return " AND " + strings.Join(filters, " AND ")
}

// ValidateInputs validates date and timezone inputs
func ValidateInputs(startDate, endDate, timeZone string) error {
	if _, err := time.LoadLocation(timeZone); err != nil {
		return fmt.Errorf("invalid time zone: %v", timeZone)
	}

	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return fmt.Errorf("invalid start date: %v", err)
	}

	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return fmt.Errorf("invalid end date: %v", err)
	}

	if end.Before(start) {
		return errors.New("end date must be after start date")
	}

	return nil
}

// GetEnvOrDie retrieves an environment variable or exits if not set
func GetEnvOrDie(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalf("Environment variable %s must be set", key)
	}
	return value
}

// RedactEmail masks the local part of an email address for safe debug logging.
// "user@example.com" → "u***@example.com". Returns "[email]" for malformed input.
func RedactEmail(s string) string {
	at := strings.Index(s, "@")
	if at <= 0 {
		return "[email]"
	}
	if at == 1 {
		return s[:1] + "@" + s[at+1:]
	}
	return s[:1] + strings.Repeat("*", at-1) + s[at:]
}

// RedactName masks a display name for safe debug logging.
// Returns "[name]" for any non-empty string, empty string otherwise.
func RedactName(s string) string {
	if s == "" {
		return ""
	}
	return "[name]"
}

// RedactOthersInIssues returns a copy of issues with third-party assignee names and
// emails replaced by opaque placeholders. Any assignee whose email matches one of
// selfEmails (case-insensitive) is left untouched. Stable account IDs are preserved.
// When --redact-others is enabled, this is applied before JSON export and report rendering.
func RedactOthersInIssues(issues []models.Issue, selfEmails ...string) []models.Issue {
	self := make(map[string]bool, len(selfEmails))
	for _, e := range selfEmails {
		if e != "" {
			self[strings.ToLower(e)] = true
		}
	}

	out := make([]models.Issue, len(issues))
	for i, issue := range issues {
		if !self[strings.ToLower(issue.AssigneeEmail)] {
			if issue.Assignee != "" {
				issue.Assignee = RedactName(issue.Assignee)
			}
			if issue.AssigneeEmail != "" {
				issue.AssigneeEmail = RedactEmail(issue.AssigneeEmail)
			}
		}
		out[i] = issue
	}
	return out
}

// RedactOthersInArticles returns a copy of articles with third-party creator and
// last-editor names and emails replaced by opaque placeholders. Any creator whose
// email matches one of selfEmails (case-insensitive) keeps their name. LastEditor
// is always redacted because the API returns no email for that field.
// Stable account IDs are preserved.
func RedactOthersInArticles(articles []models.ConfluenceArticle, selfEmails ...string) []models.ConfluenceArticle {
	self := make(map[string]bool, len(selfEmails))
	for _, e := range selfEmails {
		if e != "" {
			self[strings.ToLower(e)] = true
		}
	}

	out := make([]models.ConfluenceArticle, len(articles))
	for i, article := range articles {
		if !self[strings.ToLower(article.CreatorEmail)] {
			if article.Creator != "" {
				article.Creator = RedactName(article.Creator)
			}
			if article.CreatorEmail != "" {
				article.CreatorEmail = RedactEmail(article.CreatorEmail)
			}
		}
		if article.LastEditor != "" {
			article.LastEditor = "[name]"
		}
		out[i] = article
	}
	return out
}

// LogDebug logs a debug message if debug mode is enabled
func LogDebug(format string, v ...interface{}) {
	if !Debug {
		return
	}

	// Initialize logger if needed
	if DebugLogger == nil {
		logPath := DefaultDebugLog()
		if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
			log.Printf("Failed to create debug log directory: %v", err)
			return
		}
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			log.Printf("Failed to open debug log file: %v", err)
			return
		}
		multiWriter := io.MultiWriter(os.Stdout, logFile)
		DebugLogger = log.New(multiWriter, "DEBUG: ", log.Ldate|log.Ltime|log.Lshortfile)
	}

	DebugLogger.Printf(format, v...)
}
