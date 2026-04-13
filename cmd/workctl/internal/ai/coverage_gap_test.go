package ai

// coverage_gap_test.go — targeted tests to close specific coverage gaps (EPIC-009 Phase 4).
// Each test corresponds to a branch that was 0% per the coverage report.

import (
	"testing"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

// ---------------------------------------------------------------------------
// parseTimestamp — hits the "all formats fail" error return path
// ---------------------------------------------------------------------------

func TestParseTimestampAllFormatsFail(t *testing.T) {
	formats := []string{"2006-01-02", "2006-01-02T15:04:05Z"}
	_, err := parseTimestamp("not-a-date-at-all", formats)
	if err == nil {
		t.Error("parseTimestamp with bad input should return error")
	}
}

func TestParseTimestampNoFormatsReturnsZero(t *testing.T) {
	// With no formats, the loop never runs; returns zero time, nil error.
	ts, err := parseTimestamp("2025-01-01", nil)
	if err != nil {
		t.Errorf("parseTimestamp with nil formats should return nil error, got %v", err)
	}
	if !ts.IsZero() {
		t.Errorf("parseTimestamp with nil formats should return zero time, got %v", ts)
	}
}

// ---------------------------------------------------------------------------
// jiraToTimelineEntry — hits the "no timestamp fields" error path
// and the "error path when err != nil" branch
// ---------------------------------------------------------------------------

func TestJiraToTimelineEntryNoTimestamps(t *testing.T) {
	issue := models.Issue{Key: "SR-1"}
	// All timestamp fields empty → "no timestamp fields" error
	_, err := jiraToTimelineEntry(&issue)
	if err == nil {
		t.Error("jiraToTimelineEntry with no timestamps should return error")
	}
}

func TestJiraToTimelineEntryBadTimestamp(t *testing.T) {
	issue := models.Issue{Key: "SR-1"}
	issue.Fields.Resolved = "not-a-date"
	// Bad resolved date, no other dates → hits err != nil path
	_, err := jiraToTimelineEntry(&issue)
	if err == nil {
		t.Error("jiraToTimelineEntry with unparseable timestamp should return error")
	}
}

// ---------------------------------------------------------------------------
// confluenceToTimelineEntry — same error patterns
// ---------------------------------------------------------------------------

func TestConfluenceToTimelineEntryNoTimestamps(t *testing.T) {
	article := models.ConfluenceArticle{Title: "Doc"}
	_, err := confluenceToTimelineEntry(&article)
	if err == nil {
		t.Error("confluenceToTimelineEntry with no timestamps should return error")
	}
}

// ---------------------------------------------------------------------------
// computeJiraMetrics — empty Status.Name → "Unknown" status fallback
// ---------------------------------------------------------------------------

func TestComputeJiraMetricsEmptyStatus(t *testing.T) {
	exp := &ParsedExport{
		Source: SourceJira,
		Jira: []models.Issue{
			{
				Key:       "SR-1",
				IssueType: "Story",
				// Status.Name is empty → should fall back to "Unknown"
				Fields: struct {
					Summary  string
					Created  string
					Updated  string
					Resolved string
					Status   struct{ Name string }
				}{
					Summary: "Test issue",
					Created: "2025-01-01T10:00:00.000+0000",
				},
			},
		},
	}

	m := computeJiraMetrics(exp)
	if m == nil {
		t.Fatal("expected non-nil metrics")
	}
	if m.ByStatus["Unknown"] != 1 {
		t.Errorf("ByStatus[Unknown] = %d, want 1 for empty status name", m.ByStatus["Unknown"])
	}
}

// ---------------------------------------------------------------------------
// computeConfluenceMetrics — empty SpaceName AND SpaceKey → "Unknown"
// ---------------------------------------------------------------------------

func TestComputeConfluenceMetricsUnknownSpace(t *testing.T) {
	exp := &ParsedExport{
		Source: SourceConfluence,
		Confluence: []models.ConfluenceArticle{
			{
				Title:       "Orphan doc",
				SpaceName:   "", // empty
				SpaceKey:    "", // empty too → falls through to "Unknown"
				CreatedDate: "2025-01-01T09:00:00.000Z",
			},
		},
	}

	m := computeConfluenceMetrics(exp)
	if m == nil {
		t.Fatal("expected non-nil metrics")
	}
	if m.BySpace["Unknown"] != 1 {
		t.Errorf("BySpace[Unknown] = %d, want 1 for empty space fields", m.BySpace["Unknown"])
	}
}

// ---------------------------------------------------------------------------
// computeGitHubMetrics — empty EventType and empty Repository → "Unknown"
// ---------------------------------------------------------------------------

func TestComputeGitHubMetricsUnknownFallbacks(t *testing.T) {
	exp := &ParsedExport{
		Source: SourceGitHub,
		GitHub: []models.GitHubActivity{
			{
				EventType:  "", // → "Unknown"
				Repository: "", // → "Unknown", also excluded from uniqueRepos
				Timestamp:  time.Now(),
			},
		},
	}

	m := computeGitHubMetrics(exp)
	if m == nil {
		t.Fatal("expected non-nil metrics")
	}
	if m.ByEventType["Unknown"] != 1 {
		t.Errorf("ByEventType[Unknown] = %d, want 1", m.ByEventType["Unknown"])
	}
	if m.ByRepo["Unknown"] != 1 {
		t.Errorf("ByRepo[Unknown] = %d, want 1", m.ByRepo["Unknown"])
	}
	if m.UniqueRepos != 0 {
		t.Errorf("UniqueRepos = %d, want 0 (Unknown excluded)", m.UniqueRepos)
	}
}
