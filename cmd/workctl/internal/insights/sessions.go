package insights

import (
	"sort"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

// SessionSignals captures per-session analysis from session_summary events.
type SessionSignals struct {
	TotalSessions       int         `json:"total_sessions"`      // deduplicated session count
	MultiProjectCount   int         `json:"multi_project_count"` // sessions spanning >1 cwd
	AvgEventsPerSession float64     `json:"avg_events_per_session"`
	AvgToolsPerSession  float64     `json:"avg_tools_per_session"`
	AvgDurationMin      float64     `json:"avg_duration_min"`
	LongestSessionMin   float64     `json:"longest_session_min"`
	ProjectSessions     []FocusItem `json:"project_sessions"` // project → session count (sorted by count)
}

// deduplicatedSession is a session_summary deduplicated by session_id.
// When multiple summaries share a session_id, the one with the highest
// TotalEvents (latest checkpoint) is kept.
type deduplicatedSession struct {
	models.SessionSummary
	cwds map[string]bool // all distinct cwds seen for this session
}

// AnalyzeSessions groups session summaries by session_id, deduplicates
// (keeping the latest checkpoint), and computes per-session metrics.
func AnalyzeSessions(summaries []models.SessionSummary) *SessionSignals {
	s := &SessionSignals{}
	if len(summaries) == 0 {
		return s
	}

	// Group by session_id; keep latest (highest TotalEvents) per session.
	byID := make(map[string]*deduplicatedSession)
	for _, ss := range summaries {
		if ss.SessionID == "" {
			continue
		}
		existing, ok := byID[ss.SessionID]
		if !ok {
			byID[ss.SessionID] = &deduplicatedSession{
				SessionSummary: ss,
				cwds:           map[string]bool{ss.Cwd: true},
			}
			continue
		}
		// Track all cwds for multi-project detection
		if ss.Cwd != "" {
			existing.cwds[ss.Cwd] = true
		}
		// Keep the checkpoint with the most events (latest state)
		if ss.TotalEvents > existing.TotalEvents {
			existing.SessionSummary = ss
		}
	}

	// Compute metrics from deduplicated sessions
	var totalEvents, totalTools int
	var totalDurationMin float64
	projectCounts := make(map[string]int)

	for _, ds := range byID {
		s.TotalSessions++
		totalEvents += ds.TotalEvents
		totalTools += ds.ToolEvents

		if len(ds.cwds) > 1 {
			s.MultiProjectCount++
		}

		// Project from last cwd component
		proj := lastPathComponent(ds.Cwd)
		if proj != "" {
			projectCounts[proj]++
		}

		if !ds.FirstEvent.IsZero() && !ds.LastEvent.IsZero() {
			dur := ds.LastEvent.Sub(ds.FirstEvent).Minutes()
			if dur > 0 {
				totalDurationMin += dur
				if dur > s.LongestSessionMin {
					s.LongestSessionMin = dur
				}
			}
		}
	}

	if s.TotalSessions > 0 {
		s.AvgEventsPerSession = float64(totalEvents) / float64(s.TotalSessions)
		s.AvgToolsPerSession = float64(totalTools) / float64(s.TotalSessions)
		s.AvgDurationMin = totalDurationMin / float64(s.TotalSessions)
	}

	s.ProjectSessions = sortedFocusItems(projectCounts)
	return s
}

// TopologySignals captures crystallization and anti-pattern metrics
// across all sessions.
type TopologySignals struct {
	GraduationDensity float64 `json:"graduation_density"` // graduation_candidates / total_events
	InferenceSessions int     `json:"inference_sessions"` // sessions with 0 tool_events (pure inference)
	ToolSessions      int     `json:"tool_sessions"`      // sessions with >0 tool_events
	AntiPatternRate   float64 `json:"anti_pattern_rate"`  // inference-only sessions / total sessions
}

// AnalyzeTopology computes crystallization and anti-pattern rates from
// deduplicated session summaries.
func AnalyzeTopology(summaries []models.SessionSummary) *TopologySignals {
	t := &TopologySignals{}
	if len(summaries) == 0 {
		return t
	}

	// Deduplicate by session_id (same logic as AnalyzeSessions)
	byID := make(map[string]*models.SessionSummary)
	for i := range summaries {
		ss := &summaries[i]
		if ss.SessionID == "" {
			continue
		}
		existing, ok := byID[ss.SessionID]
		if !ok || ss.TotalEvents > existing.TotalEvents {
			byID[ss.SessionID] = ss
		}
	}

	var totalEvents, totalGrads int
	for _, ss := range byID {
		totalEvents += ss.TotalEvents
		totalGrads += ss.GraduationCandidates
		if ss.ToolEvents == 0 {
			t.InferenceSessions++
		} else {
			t.ToolSessions++
		}
	}

	total := t.InferenceSessions + t.ToolSessions
	if totalEvents > 0 {
		t.GraduationDensity = float64(totalGrads) / float64(totalEvents)
	}
	if total > 0 {
		t.AntiPatternRate = float64(t.InferenceSessions) / float64(total)
	}

	return t
}

// sortedSessionProjects converts a map to descending-sorted FocusItems.
// (reuses sortedFocusItems from signals.go via package scope)
var _ = sort.Slice // ensure sort is used
