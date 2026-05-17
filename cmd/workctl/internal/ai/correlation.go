package ai

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// resolveIdentity merges explicit identity config with metadata from parsed exports.
// Priority: explicit ProcessOptions.Identity > metadata fields > empty.
func resolveIdentity(opts ProcessOptions, exports []*ParsedExport) UserIdentity {
	var id UserIdentity
	if opts.Identity != nil {
		id = *opts.Identity
	}

	// Fill gaps from metadata
	for _, exp := range exports {
		if id.Email == "" && exp.Metadata.Query.Email != "" {
			id.Email = exp.Metadata.Query.Email
		}
		if id.GitHubLogin == "" && exp.Metadata.Query.GitHubUser != "" {
			id.GitHubLogin = exp.Metadata.Query.GitHubUser
		}
	}

	// Derive display name from email if unset
	if id.DisplayName == "" && id.Email != "" {
		if parts := strings.SplitN(id.Email, "@", 2); len(parts) > 0 {
			id.DisplayName = parts[0]
		}
	}

	return id
}

// Jira timestamp formats to try in order.
var jiraTimestampFormats = []string{
	"2006-01-02T15:04:05.000-0700",
	"2006-01-02T15:04:05.000Z",
	time.RFC3339,
	"2006-01-02T15:04:05.000+0000",
}

// Confluence timestamp formats to try in order.
var confluenceTimestampFormats = []string{
	"2006-01-02T15:04:05.000Z",
	"2006-01-02T15:04:05.000-0700",
	time.RFC3339,
}

// parseTimestamp tries multiple formats to parse a timestamp string.
func parseTimestamp(s string, formats []string) (time.Time, error) {
	var lastErr error
	for _, fmt := range formats {
		t, err := time.Parse(fmt, s)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

// jiraToTimelineEntry converts a Jira issue into a single timeline entry.
// Timestamp priority: Resolved > Updated > Created.
func jiraToTimelineEntry(issue *models.Issue) (TimelineEntry, error) {
	// Pick best timestamp and event type
	var ts time.Time
	var eventType string
	var err error

	candidates := []struct {
		value     string
		eventType string
	}{
		{issue.Fields.Resolved, "Resolved"},
		{issue.Fields.Updated, "Updated"},
		{issue.Fields.Created, "Created"},
	}

	for _, c := range candidates {
		if c.value == "" {
			continue
		}
		ts, err = parseTimestamp(c.value, jiraTimestampFormats)
		if err == nil {
			eventType = c.eventType
			break
		}
	}

	if eventType == "" {
		if err != nil {
			return TimelineEntry{}, err
		}
		return TimelineEntry{}, fmt.Errorf("jira issue has no timestamp fields")
	}

	return TimelineEntry{
		Timestamp: ts,
		Source:    SourceJira,
		EventType: eventType,
		Title:     issue.Fields.Summary,
		URL:       issue.URL,
		JiraIssue: issue,
	}, nil
}

// confluenceToTimelineEntry converts a Confluence article into a timeline entry.
// Timestamp priority: LastModifiedDate > CreatedDate.
func confluenceToTimelineEntry(article *models.ConfluenceArticle) (TimelineEntry, error) {
	var ts time.Time
	var eventType string
	var err error

	candidates := []struct {
		value     string
		eventType string
	}{
		{article.LastModifiedDate, "Edited"},
		{article.CreatedDate, "Created"},
	}

	for _, c := range candidates {
		if c.value == "" {
			continue
		}
		ts, err = parseTimestamp(c.value, confluenceTimestampFormats)
		if err == nil {
			eventType = c.eventType
			break
		}
	}

	if eventType == "" {
		if err != nil {
			return TimelineEntry{}, err
		}
		return TimelineEntry{}, fmt.Errorf("confluence article has no timestamp fields")
	}

	return TimelineEntry{
		Timestamp:         ts,
		Source:            SourceConfluence,
		EventType:         eventType,
		Title:             article.Title,
		URL:               article.URL,
		ConfluenceArticle: article,
	}, nil
}

// githubToTimelineEntry converts a GitHub activity into a timeline entry.
// Detects synthetic entries via "Aggregate" prefix on EventType.
func githubToTimelineEntry(activity *models.GitHubActivity) TimelineEntry {
	return TimelineEntry{
		Timestamp:      activity.Timestamp,
		Source:         SourceGitHub,
		EventType:      activity.EventType,
		Title:          activity.Description,
		URL:            activity.URL,
		IsSynthetic:    strings.HasPrefix(activity.EventType, "Aggregate"),
		GitHubActivity: activity,
	}
}

// buildTimeline converts all parsed exports into a unified, sorted timeline.
// Entries are sorted newest-first. Items with unparseable timestamps are skipped.
func buildTimeline(exports []*ParsedExport) UnifiedTimeline {
	var entries []TimelineEntry
	sourceSet := make(map[Source]bool)

	for _, exp := range exports {
		switch exp.Source {
		case SourceJira:
			for i := range exp.Jira {
				entry, err := jiraToTimelineEntry(&exp.Jira[i])
				if err == nil {
					entries = append(entries, entry)
				}
			}
			if len(exp.Jira) > 0 {
				sourceSet[SourceJira] = true
			}

		case SourceConfluence:
			for i := range exp.Confluence {
				entry, err := confluenceToTimelineEntry(&exp.Confluence[i])
				if err == nil {
					entries = append(entries, entry)
				}
			}
			if len(exp.Confluence) > 0 {
				sourceSet[SourceConfluence] = true
			}

		case SourceGitHub:
			for i := range exp.GitHub {
				entries = append(entries, githubToTimelineEntry(&exp.GitHub[i]))
			}
			if len(exp.GitHub) > 0 {
				sourceSet[SourceGitHub] = true
			}
		}
	}

	// Sort newest-first
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})

	// Collect sources
	var sources []Source
	for _, s := range []Source{SourceJira, SourceConfluence, SourceGitHub} {
		if sourceSet[s] {
			sources = append(sources, s)
		}
	}

	// Compute date bounds from entries
	var dr DateRange
	if len(entries) > 0 {
		dr.Start = entries[len(entries)-1].Timestamp // oldest (last after newest-first sort)
		dr.End = entries[0].Timestamp                // newest
	}

	return UnifiedTimeline{
		Entries:   entries,
		DateRange: dr,
		Sources:   sources,
	}
}
