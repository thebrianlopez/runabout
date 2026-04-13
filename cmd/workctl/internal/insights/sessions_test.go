package insights

import (
	"testing"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

func TestAnalyzeSessions_Empty(t *testing.T) {
	s := AnalyzeSessions(nil)
	if s == nil {
		t.Fatal("AnalyzeSessions(nil) returned nil")
	}
	if s.TotalSessions != 0 {
		t.Errorf("TotalSessions = %d, want 0", s.TotalSessions)
	}
}

func TestAnalyzeSessions_Deduplication(t *testing.T) {
	// Same session_id with incremental summaries — should deduplicate to 1.
	summaries := []models.SessionSummary{
		{
			SessionID:   "sess-1",
			Cwd:         "/Users/brian/code/proj",
			TotalEvents: 13,
			ToolEvents:  12,
			FirstEvent:  time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC),
			LastEvent:   time.Date(2026, 3, 2, 14, 5, 0, 0, time.UTC),
		},
		{
			SessionID:   "sess-1",
			Cwd:         "/Users/brian/code/proj",
			TotalEvents: 18, // later checkpoint — should win
			ToolEvents:  14,
			FirstEvent:  time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC),
			LastEvent:   time.Date(2026, 3, 2, 14, 8, 0, 0, time.UTC),
		},
	}

	s := AnalyzeSessions(summaries)
	if s.TotalSessions != 1 {
		t.Errorf("TotalSessions = %d, want 1 (deduplicated)", s.TotalSessions)
	}
	// Should use the latest checkpoint's values
	if s.AvgEventsPerSession < 17.9 || s.AvgEventsPerSession > 18.1 {
		t.Errorf("AvgEventsPerSession = %.1f, want 18.0", s.AvgEventsPerSession)
	}
}

func TestAnalyzeSessions_MultiProject(t *testing.T) {
	summaries := []models.SessionSummary{
		{
			SessionID:   "sess-1",
			Cwd:         "/Users/brian/code/proj-a",
			TotalEvents: 10,
			ToolEvents:  8,
			FirstEvent:  time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC),
			LastEvent:   time.Date(2026, 3, 2, 14, 5, 0, 0, time.UTC),
		},
		{
			SessionID:   "sess-1",
			Cwd:         "/Users/brian/code/proj-b", // different cwd
			TotalEvents: 15,
			ToolEvents:  12,
			FirstEvent:  time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC),
			LastEvent:   time.Date(2026, 3, 2, 14, 10, 0, 0, time.UTC),
		},
		{
			SessionID:   "sess-2",
			Cwd:         "/Users/brian/code/proj-a",
			TotalEvents: 5,
			ToolEvents:  4,
			FirstEvent:  time.Date(2026, 3, 2, 15, 0, 0, 0, time.UTC),
			LastEvent:   time.Date(2026, 3, 2, 15, 3, 0, 0, time.UTC),
		},
	}

	s := AnalyzeSessions(summaries)
	if s.TotalSessions != 2 {
		t.Errorf("TotalSessions = %d, want 2", s.TotalSessions)
	}
	if s.MultiProjectCount != 1 {
		t.Errorf("MultiProjectCount = %d, want 1 (sess-1 spans two cwds)", s.MultiProjectCount)
	}
	if s.LongestSessionMin < 9.9 || s.LongestSessionMin > 10.1 {
		t.Errorf("LongestSessionMin = %.1f, want ~10", s.LongestSessionMin)
	}
}

func TestAnalyzeTopology_Empty(t *testing.T) {
	ts := AnalyzeTopology(nil)
	if ts == nil {
		t.Fatal("AnalyzeTopology(nil) returned nil")
	}
	if ts.GraduationDensity != 0 {
		t.Errorf("GraduationDensity = %f, want 0", ts.GraduationDensity)
	}
}

func TestAnalyzeTopology_Metrics(t *testing.T) {
	summaries := []models.SessionSummary{
		{
			SessionID:            "sess-1",
			TotalEvents:          50,
			ToolEvents:           48,
			GraduationCandidates: 10,
		},
		{
			SessionID:            "sess-2",
			TotalEvents:          10,
			ToolEvents:           0, // inference-only
			GraduationCandidates: 0,
		},
		{
			SessionID:            "sess-3",
			TotalEvents:          40,
			ToolEvents:           38,
			GraduationCandidates: 5,
		},
	}

	ts := AnalyzeTopology(summaries)
	if ts.ToolSessions != 2 {
		t.Errorf("ToolSessions = %d, want 2", ts.ToolSessions)
	}
	if ts.InferenceSessions != 1 {
		t.Errorf("InferenceSessions = %d, want 1", ts.InferenceSessions)
	}
	// AntiPatternRate = 1/3 ≈ 33%
	if ts.AntiPatternRate < 0.32 || ts.AntiPatternRate > 0.34 {
		t.Errorf("AntiPatternRate = %.3f, want ~0.333", ts.AntiPatternRate)
	}
	// GraduationDensity = 15/100 = 0.15
	if ts.GraduationDensity < 0.14 || ts.GraduationDensity > 0.16 {
		t.Errorf("GraduationDensity = %.3f, want ~0.15", ts.GraduationDensity)
	}
}
