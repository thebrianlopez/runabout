package ai

import (
	"math"
	"testing"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

func TestComputeJiraMetrics(t *testing.T) {
	exp := &ParsedExport{
		Source: SourceJira,
		Jira: []models.Issue{
			{
				Key:        "PROJ-1",
				ProjectKey: "PROJ",
				IssueType:  "Story",
				Fields: struct {
					Summary  string
					Created  string
					Updated  string
					Resolved string
					Status   struct{ Name string }
				}{
					Summary:  "Feature A",
					Created:  "2025-01-01T10:00:00.000+0000",
					Resolved: "2025-01-11T10:00:00.000+0000",
					Status:   struct{ Name string }{Name: "Done"},
				},
			},
			{
				Key:        "PROJ-2",
				ProjectKey: "PROJ",
				IssueType:  "Bug",
				Fields: struct {
					Summary  string
					Created  string
					Updated  string
					Resolved string
					Status   struct{ Name string }
				}{
					Summary: "Bug fix",
					Created: "2025-02-01T10:00:00.000+0000",
					Status:  struct{ Name string }{Name: "In Progress"},
				},
			},
			{
				Key:       "OTHER-1",
				IssueType: "Task",
				Fields: struct {
					Summary  string
					Created  string
					Updated  string
					Resolved string
					Status   struct{ Name string }
				}{
					Summary:  "Other task",
					Created:  "2025-03-01T10:00:00.000+0000",
					Resolved: "2025-03-06T10:00:00.000+0000",
					Status:   struct{ Name string }{Name: "Done"},
				},
			},
		},
	}

	m := computeJiraMetrics(exp)
	if m == nil {
		t.Fatal("expected non-nil metrics")
	}

	if m.TotalIssues != 3 {
		t.Errorf("TotalIssues = %d, want 3", m.TotalIssues)
	}
	if m.ByStatus["Done"] != 2 {
		t.Errorf("ByStatus[Done] = %d, want 2", m.ByStatus["Done"])
	}
	if m.ByType["Story"] != 1 || m.ByType["Bug"] != 1 || m.ByType["Task"] != 1 {
		t.Errorf("ByType = %v, want 1 of each", m.ByType)
	}
	if m.ByProject["PROJ"] != 2 {
		t.Errorf("ByProject[PROJ] = %d, want 2", m.ByProject["PROJ"])
	}
	if m.ResolvedCount != 2 {
		t.Errorf("ResolvedCount = %d, want 2", m.ResolvedCount)
	}
	// Mean resolution: (10 days + 5 days) / 2 = 7.5 days
	if math.Abs(m.MeanResolutionDays-7.5) > 0.01 {
		t.Errorf("MeanResolutionDays = %f, want 7.5", m.MeanResolutionDays)
	}
}

func TestComputeJiraMetricsProjectKeyExtraction(t *testing.T) {
	exp := &ParsedExport{
		Source: SourceJira,
		Jira: []models.Issue{
			{
				Key: "ABC-123",
				Fields: struct {
					Summary  string
					Created  string
					Updated  string
					Resolved string
					Status   struct{ Name string }
				}{
					Summary: "Test",
					Created: "2025-01-01T10:00:00.000+0000",
					Status:  struct{ Name string }{Name: "Open"},
				},
			},
		},
	}

	m := computeJiraMetrics(exp)
	if m.ByProject["ABC"] != 1 {
		t.Errorf("ByProject = %v, want ABC:1 (extracted from Key)", m.ByProject)
	}
}

func TestComputeJiraMetricsEmpty(t *testing.T) {
	exp := &ParsedExport{Source: SourceJira}
	if m := computeJiraMetrics(exp); m != nil {
		t.Errorf("expected nil for empty, got %+v", m)
	}
}

func TestComputeConfluenceMetrics(t *testing.T) {
	exp := &ParsedExport{
		Source: SourceConfluence,
		Confluence: []models.ConfluenceArticle{
			{
				SpaceKey:         "ENG",
				SpaceName:        "Engineering",
				CreatedDate:      "2025-01-01T09:00:00.000Z",
				LastModifiedDate: "2025-02-01T09:00:00.000Z",
			},
			{
				SpaceKey:    "ENG",
				SpaceName:   "Engineering",
				CreatedDate: "2025-03-01T09:00:00.000Z",
				// No LastModifiedDate → treated as created-only
			},
			{
				SpaceKey:         "HR",
				SpaceName:        "Human Resources",
				CreatedDate:      "2025-04-01T09:00:00.000Z",
				LastModifiedDate: "2025-04-01T09:00:00.000Z", // Same as created → counts as created
			},
		},
	}

	m := computeConfluenceMetrics(exp)
	if m == nil {
		t.Fatal("expected non-nil metrics")
	}

	if m.TotalArticles != 3 {
		t.Errorf("TotalArticles = %d, want 3", m.TotalArticles)
	}
	if m.BySpace["Engineering"] != 2 {
		t.Errorf("BySpace[Engineering] = %d, want 2", m.BySpace["Engineering"])
	}
	if m.UniqueSpaces != 2 {
		t.Errorf("UniqueSpaces = %d, want 2", m.UniqueSpaces)
	}
	if m.EditedCount != 1 {
		t.Errorf("EditedCount = %d, want 1", m.EditedCount)
	}
	if m.CreatedCount != 2 {
		t.Errorf("CreatedCount = %d, want 2", m.CreatedCount)
	}
}

func TestComputeConfluenceMetricsEmpty(t *testing.T) {
	exp := &ParsedExport{Source: SourceConfluence}
	if m := computeConfluenceMetrics(exp); m != nil {
		t.Errorf("expected nil for empty, got %+v", m)
	}
}

func TestComputeGitHubMetrics(t *testing.T) {
	exp := &ParsedExport{
		Source: SourceGitHub,
		GitHub: []models.GitHubActivity{
			{
				EventType:   "PullRequestEvent",
				Repository:  "org/api",
				Description: "Opened PR #1",
				Timestamp:   time.Date(2025, 1, 10, 12, 0, 0, 0, time.UTC),
			},
			{
				EventType:   "PullRequestEvent",
				Repository:  "org/api",
				Description: "Merged PR #2",
				Timestamp:   time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
			},
			{
				EventType:   "PushEvent",
				Repository:  "org/web",
				Description: "Pushed 3 commits",
				Timestamp:   time.Date(2025, 2, 1, 12, 0, 0, 0, time.UTC),
			},
			{
				EventType:   "PullRequestReviewEvent",
				Repository:  "org/api",
				Description: "Reviewed PR #3",
				Timestamp:   time.Date(2025, 2, 5, 12, 0, 0, 0, time.UTC),
			},
			{
				EventType:   "AggregatePushEvent",
				Repository:  "org/legacy",
				Description: "Aggregated 100 push events",
				Timestamp:   time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC),
			},
		},
	}

	m := computeGitHubMetrics(exp)
	if m == nil {
		t.Fatal("expected non-nil metrics")
	}

	if m.TotalActivities != 5 {
		t.Errorf("TotalActivities = %d, want 5", m.TotalActivities)
	}
	if m.PRsOpened != 2 {
		t.Errorf("PRsOpened = %d, want 2", m.PRsOpened)
	}
	if m.PRsMerged != 1 {
		t.Errorf("PRsMerged = %d, want 1", m.PRsMerged)
	}
	if m.PRsReviewed != 1 {
		t.Errorf("PRsReviewed = %d, want 1", m.PRsReviewed)
	}
	if m.CommitsCount != 1 {
		t.Errorf("CommitsCount = %d, want 1", m.CommitsCount)
	}
	// UniqueRepos excludes synthetic: org/api, org/web = 2 (org/legacy excluded)
	if m.UniqueRepos != 2 {
		t.Errorf("UniqueRepos = %d, want 2", m.UniqueRepos)
	}
}

func TestComputeGitHubMetricsEmpty(t *testing.T) {
	exp := &ParsedExport{Source: SourceGitHub}
	if m := computeGitHubMetrics(exp); m != nil {
		t.Errorf("expected nil for empty, got %+v", m)
	}
}

func TestAssessDataQualityAllSources(t *testing.T) {
	exports := []*ParsedExport{
		{Source: SourceJira},
		{Source: SourceConfluence},
		{Source: SourceGitHub},
	}

	dq := assessDataQuality(exports)
	if dq.PartialData {
		t.Error("expected PartialData=false with all sources")
	}
	if len(dq.SourcesPresent) != 3 {
		t.Errorf("SourcesPresent = %v, want 3", dq.SourcesPresent)
	}
	if len(dq.SourcesMissing) != 0 {
		t.Errorf("SourcesMissing = %v, want empty", dq.SourcesMissing)
	}
}

func TestAssessDataQualityPartial(t *testing.T) {
	exports := []*ParsedExport{
		{Source: SourceJira},
	}

	dq := assessDataQuality(exports)
	if !dq.PartialData {
		t.Error("expected PartialData=true with one source")
	}
	if len(dq.SourcesMissing) != 2 {
		t.Errorf("SourcesMissing = %v, want 2", dq.SourcesMissing)
	}
}

func TestAssessDataQualitySynthetic(t *testing.T) {
	exports := []*ParsedExport{
		{
			Source: SourceGitHub,
			GitHub: []models.GitHubActivity{
				{EventType: "AggregatePushEvent"},
			},
		},
	}

	dq := assessDataQuality(exports)
	if !dq.SyntheticData {
		t.Error("expected SyntheticData=true")
	}
	if len(dq.Warnings) < 1 {
		t.Error("expected at least one warning")
	}
}
