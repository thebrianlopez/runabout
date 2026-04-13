package ai

import (
	"testing"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/export"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

func TestResolveIdentityExplicitTakesPriority(t *testing.T) {
	opts := ProcessOptions{
		Identity: &UserIdentity{
			Email:       "explicit@example.com",
			GitHubLogin: "explicit-gh",
			DisplayName: "Explicit User",
		},
	}
	exports := []*ParsedExport{
		{
			Source: SourceJira,
			Metadata: export.Metadata{
				Query: export.QueryMetadata{Email: "metadata@example.com"},
			},
		},
		{
			Source: SourceGitHub,
			Metadata: export.Metadata{
				Query: export.QueryMetadata{GitHubUser: "metadata-gh"},
			},
		},
	}

	id := resolveIdentity(opts, exports)
	if id.Email != "explicit@example.com" {
		t.Errorf("email = %q, want explicit@example.com", id.Email)
	}
	if id.GitHubLogin != "explicit-gh" {
		t.Errorf("github = %q, want explicit-gh", id.GitHubLogin)
	}
	if id.DisplayName != "Explicit User" {
		t.Errorf("name = %q, want Explicit User", id.DisplayName)
	}
}

func TestResolveIdentityFromMetadata(t *testing.T) {
	opts := ProcessOptions{}
	exports := []*ParsedExport{
		{
			Source: SourceJira,
			Metadata: export.Metadata{
				Query: export.QueryMetadata{Email: "alice@example.com"},
			},
		},
		{
			Source: SourceGitHub,
			Metadata: export.Metadata{
				Query: export.QueryMetadata{GitHubUser: "alice-gh"},
			},
		},
	}

	id := resolveIdentity(opts, exports)
	if id.Email != "alice@example.com" {
		t.Errorf("email = %q, want alice@example.com", id.Email)
	}
	if id.GitHubLogin != "alice-gh" {
		t.Errorf("github = %q, want alice-gh", id.GitHubLogin)
	}
	if id.DisplayName != "alice" {
		t.Errorf("name = %q, want alice (derived from email)", id.DisplayName)
	}
}

func TestResolveIdentityEmpty(t *testing.T) {
	id := resolveIdentity(ProcessOptions{}, nil)
	if id.Email != "" || id.GitHubLogin != "" || id.DisplayName != "" {
		t.Errorf("expected empty identity, got %+v", id)
	}
}

func TestJiraToTimelineEntryTimestampPriority(t *testing.T) {
	tests := []struct {
		name      string
		issue     models.Issue
		wantEvent string
	}{
		{
			name: "resolved takes priority",
			issue: models.Issue{
				Fields: struct {
					Summary  string
					Created  string
					Updated  string
					Resolved string
					Status   struct{ Name string }
				}{
					Summary:  "Test",
					Created:  "2025-01-01T10:00:00.000+0000",
					Updated:  "2025-02-01T10:00:00.000+0000",
					Resolved: "2025-03-01T10:00:00.000+0000",
				},
			},
			wantEvent: "Resolved",
		},
		{
			name: "updated when no resolved",
			issue: models.Issue{
				Fields: struct {
					Summary  string
					Created  string
					Updated  string
					Resolved string
					Status   struct{ Name string }
				}{
					Summary: "Test",
					Created: "2025-01-01T10:00:00.000+0000",
					Updated: "2025-02-01T10:00:00.000+0000",
				},
			},
			wantEvent: "Updated",
		},
		{
			name: "created when nothing else",
			issue: models.Issue{
				Fields: struct {
					Summary  string
					Created  string
					Updated  string
					Resolved string
					Status   struct{ Name string }
				}{
					Summary: "Test",
					Created: "2025-01-01T10:00:00.000+0000",
				},
			},
			wantEvent: "Created",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := jiraToTimelineEntry(&tt.issue)
			if err != nil {
				t.Fatalf("jiraToTimelineEntry: %v", err)
			}
			if entry.EventType != tt.wantEvent {
				t.Errorf("EventType = %q, want %q", entry.EventType, tt.wantEvent)
			}
			if entry.Source != SourceJira {
				t.Errorf("Source = %q, want %q", entry.Source, SourceJira)
			}
		})
	}
}

func TestJiraToTimelineEntryNoTimestamp(t *testing.T) {
	issue := models.Issue{}
	_, err := jiraToTimelineEntry(&issue)
	if err == nil {
		t.Fatal("expected error for issue with no timestamps")
	}
}

func TestConfluenceToTimelineEntry(t *testing.T) {
	article := models.ConfluenceArticle{
		Title:            "Design Doc",
		URL:              "https://wiki.example.com/pages/1",
		CreatedDate:      "2025-01-01T09:00:00.000Z",
		LastModifiedDate: "2025-02-15T14:00:00.000Z",
	}

	entry, err := confluenceToTimelineEntry(&article)
	if err != nil {
		t.Fatalf("confluenceToTimelineEntry: %v", err)
	}
	if entry.EventType != "Edited" {
		t.Errorf("EventType = %q, want Edited", entry.EventType)
	}
	if entry.Title != "Design Doc" {
		t.Errorf("Title = %q, want Design Doc", entry.Title)
	}
	// Should pick LastModifiedDate (Feb 15)
	if entry.Timestamp.Month() != 2 || entry.Timestamp.Day() != 15 {
		t.Errorf("Timestamp = %v, want Feb 15", entry.Timestamp)
	}
}

func TestConfluenceToTimelineEntryCreatedOnly(t *testing.T) {
	article := models.ConfluenceArticle{
		Title:       "New Page",
		CreatedDate: "2025-03-01T09:00:00.000Z",
	}
	entry, err := confluenceToTimelineEntry(&article)
	if err != nil {
		t.Fatalf("confluenceToTimelineEntry: %v", err)
	}
	if entry.EventType != "Created" {
		t.Errorf("EventType = %q, want Created", entry.EventType)
	}
}

func TestConfluenceToTimelineEntryNoTimestamp(t *testing.T) {
	article := models.ConfluenceArticle{Title: "Empty"}
	_, err := confluenceToTimelineEntry(&article)
	if err == nil {
		t.Fatal("expected error for article with no timestamps")
	}
}

func TestGitHubToTimelineEntry(t *testing.T) {
	ts := time.Date(2025, 4, 10, 16, 0, 0, 0, time.UTC)
	activity := models.GitHubActivity{
		EventType:   "PullRequestEvent",
		ActorLogin:  "alice",
		Repository:  "org/repo",
		Timestamp:   ts,
		Description: "Opened PR #42",
		URL:         "https://github.com/org/repo/pull/42",
	}

	entry := githubToTimelineEntry(&activity)
	if entry.IsSynthetic {
		t.Error("expected non-synthetic for PullRequestEvent")
	}
	if entry.EventType != "PullRequestEvent" {
		t.Errorf("EventType = %q, want PullRequestEvent", entry.EventType)
	}
}

func TestGitHubToTimelineEntrySynthetic(t *testing.T) {
	activity := models.GitHubActivity{
		EventType:   "AggregatePushEvent",
		Description: "Aggregated 50 push events",
		Timestamp:   time.Now(),
	}

	entry := githubToTimelineEntry(&activity)
	if !entry.IsSynthetic {
		t.Error("expected synthetic for AggregatePushEvent")
	}
}

func TestBuildTimelineSortOrder(t *testing.T) {
	exports := []*ParsedExport{
		{
			Source: SourceJira,
			Jira: []models.Issue{
				{
					Fields: struct {
						Summary  string
						Created  string
						Updated  string
						Resolved string
						Status   struct{ Name string }
					}{
						Summary: "Old Issue",
						Created: "2025-01-01T10:00:00.000+0000",
					},
				},
			},
		},
		{
			Source: SourceGitHub,
			GitHub: []models.GitHubActivity{
				{
					EventType:   "PushEvent",
					Description: "Newest",
					Timestamp:   time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
				},
				{
					EventType:   "PullRequestEvent",
					Description: "Middle",
					Timestamp:   time.Date(2025, 3, 15, 12, 0, 0, 0, time.UTC),
				},
			},
		},
	}

	tl := buildTimeline(exports)

	if len(tl.Entries) != 3 {
		t.Fatalf("len(Entries) = %d, want 3", len(tl.Entries))
	}

	// Newest first
	if tl.Entries[0].Title != "Newest" {
		t.Errorf("first entry = %q, want Newest", tl.Entries[0].Title)
	}
	if tl.Entries[2].Title != "Old Issue" {
		t.Errorf("last entry = %q, want Old Issue", tl.Entries[2].Title)
	}

	// Sources should include both
	if len(tl.Sources) != 2 {
		t.Errorf("len(Sources) = %d, want 2", len(tl.Sources))
	}
}

func TestBuildTimelineSingleSource(t *testing.T) {
	exports := []*ParsedExport{
		{
			Source: SourceConfluence,
			Confluence: []models.ConfluenceArticle{
				{Title: "Doc", CreatedDate: "2025-05-01T09:00:00.000Z"},
			},
		},
	}

	tl := buildTimeline(exports)
	if len(tl.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(tl.Entries))
	}
	if len(tl.Sources) != 1 || tl.Sources[0] != SourceConfluence {
		t.Errorf("Sources = %v, want [confluence]", tl.Sources)
	}
}

func TestBuildTimelineEmpty(t *testing.T) {
	tl := buildTimeline(nil)
	if len(tl.Entries) != 0 {
		t.Errorf("len(Entries) = %d, want 0", len(tl.Entries))
	}
}
