package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// singleDayEventsFile writes content to dir/YYYY-MM-DD.jsonl and returns the dir.
func singleDayEventsFile(t *testing.T, date, content string) string {
	t.Helper()
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, date+".jsonl"), []byte(content), 0o600)
	require.NoError(t, err)
	return dir
}

func TestGetTypedEvents_AbsentDir(t *testing.T) {
	c := newEventsClientAt("/nonexistent/events")
	batch, err := c.GetTypedEvents("2026-03-01", "2026-03-01")
	require.NoError(t, err, "absent directory should not error")
	assert.Empty(t, batch.ShellEvents)
	assert.Empty(t, batch.SessionSummaries)
	assert.Empty(t, batch.InferenceEvents)
	assert.Empty(t, batch.ToolCallEvents)
}

func TestGetTypedEvents_ShellEvent(t *testing.T) {
	content := `{"schema_version":"2","timestamp":"20260306T100000Z","layer":"fish","event_type":"shell_command","command":"kubectl get pods","session_id":"s1","cwd":"/home/user"}` + "\n"
	dir := singleDayEventsFile(t, "2026-03-06", content)

	c := newEventsClientAt(dir)
	batch, err := c.GetTypedEvents("2026-03-06", "2026-03-06")
	require.NoError(t, err)
	require.Len(t, batch.ShellEvents, 1)

	e := batch.ShellEvents[0]
	assert.Equal(t, "kubectl get pods", e.Command)
	assert.Equal(t, "fish", e.Layer)
	assert.Equal(t, "s1", e.SessionID)
	assert.Equal(t, "/home/user", e.Cwd)
}

func TestGetTypedEvents_LegacyV1ShellEvent(t *testing.T) {
	// v1 schema: no event_type, source field, space-separated timestamp
	content := `{"version":"1.0","timestamp":"2025-04-01 11:59:01","command":"ls -la","source":"interactive_shell"}` + "\n"
	dir := singleDayEventsFile(t, "2025-04-01", content)

	c := newEventsClientAt(dir)
	batch, err := c.GetTypedEvents("2025-04-01", "2025-04-01")
	require.NoError(t, err)
	require.Len(t, batch.ShellEvents, 1)

	e := batch.ShellEvents[0]
	assert.Equal(t, "ls -la", e.Command)
	assert.Equal(t, "interactive_shell", e.Layer, "v1 source=interactive_shell should map to layer=interactive_shell")
}

func TestGetTypedEvents_LegacyV2CloudLLM(t *testing.T) {
	// v2 schema: source="claude_code" should map to layer="cloud_llm"
	content := `{"version":"1.0","timestamp":"2026-02-23T09:26:35-0600","event_type":"tool_call","command":"git status","session_id":"s2","cwd":"/home","source":"claude_code"}` + "\n"
	dir := singleDayEventsFile(t, "2026-02-23", content)

	c := newEventsClientAt(dir)
	batch, err := c.GetTypedEvents("2026-02-23", "2026-02-23")
	require.NoError(t, err)
	require.Len(t, batch.ShellEvents, 1)
	assert.Equal(t, "cloud_llm", batch.ShellEvents[0].Layer, "v2 source=claude_code should map to layer=cloud_llm")
}

func TestGetTypedEvents_ToolResultEvent(t *testing.T) {
	content := `{"schema_version":"2","timestamp":"20260306T100000Z","layer":"cloud_llm","event_type":"tool_result","command":"ls -la","session_id":"s1","cwd":"/home","tool_name":"Bash","metadata":{"graduation_candidate":true,"first_word":"ls","tool_name":"Bash"}}` + "\n"
	dir := singleDayEventsFile(t, "2026-03-06", content)

	c := newEventsClientAt(dir)
	batch, err := c.GetTypedEvents("2026-03-06", "2026-03-06")
	require.NoError(t, err)
	require.Len(t, batch.ToolCallEvents, 1)

	e := batch.ToolCallEvents[0]
	assert.Equal(t, "Bash", e.ToolName)
	assert.Equal(t, "ls -la", e.Command)
	assert.Equal(t, "ls", e.FirstWord)
	assert.True(t, e.GraduationCandidate)
	assert.Equal(t, "s1", e.SessionID)
}

func TestGetTypedEvents_InferenceEvent(t *testing.T) {
	content := `{"schema_version":"2","timestamp":"20260306T100000Z","layer":"cloud_llm","event_type":"inference","command":"claude_prompt","session_id":"s1","cwd":"/home","metadata":{"cost_estimate_usd":0.0150,"token_estimate":47}}` + "\n"
	dir := singleDayEventsFile(t, "2026-03-06", content)

	c := newEventsClientAt(dir)
	batch, err := c.GetTypedEvents("2026-03-06", "2026-03-06")
	require.NoError(t, err)
	require.Len(t, batch.InferenceEvents, 1)

	e := batch.InferenceEvents[0]
	assert.InDelta(t, 0.015, e.CostEstimateUSD, 0.0001)
	assert.Equal(t, 47, e.TokenEstimate)
	assert.Equal(t, "s1", e.SessionID)
}

func TestGetTypedEvents_SessionSummaryWithCostAttachment(t *testing.T) {
	// session_summary + two inference events for the same session → cost accumulated
	content := `{"schema_version":"2","timestamp":"20260306T142229Z","layer":"claude_code","event_type":"session_summary","command":"session_stop","session_id":"sess-1","cwd":"/home","metadata":{"total_events":10,"tool_events":8,"prompt_count":2,"unique_commands":5,"tool_distribution":{"Bash":8},"graduation_candidates":3,"first_event":"20260306T141000Z","last_event":"20260306T141500Z"}}` + "\n" +
		`{"schema_version":"2","timestamp":"20260306T141100Z","layer":"cloud_llm","event_type":"inference","command":"claude_prompt","session_id":"sess-1","metadata":{"cost_estimate_usd":0.0100,"token_estimate":30}}` + "\n" +
		`{"schema_version":"2","timestamp":"20260306T141200Z","layer":"cloud_llm","event_type":"inference","command":"claude_prompt","session_id":"sess-1","metadata":{"cost_estimate_usd":0.0050,"token_estimate":15}}` + "\n"
	dir := singleDayEventsFile(t, "2026-03-06", content)

	c := newEventsClientAt(dir)
	batch, err := c.GetTypedEvents("2026-03-06", "2026-03-06")
	require.NoError(t, err)
	require.Len(t, batch.SessionSummaries, 1)
	require.Len(t, batch.InferenceEvents, 2)

	s := batch.SessionSummaries[0]
	assert.Equal(t, "sess-1", s.SessionID)
	assert.Equal(t, 10, s.TotalEvents)
	assert.Equal(t, 3, s.GraduationCandidates)
	assert.InDelta(t, 0.015, s.CostEstimateUSD, 0.0001, "cost from two inference events should be accumulated")
	assert.Equal(t, 8, s.ToolDistribution["Bash"])
	assert.False(t, s.FirstEvent.IsZero())
	assert.False(t, s.LastEvent.IsZero())
}

func TestGetTypedEvents_MixedEventTypes(t *testing.T) {
	content := `{"schema_version":"2","timestamp":"20260306T100000Z","layer":"fish","event_type":"shell_command","command":"git status","session_id":"s1","cwd":"/home"}` + "\n" +
		`{"schema_version":"2","timestamp":"20260306T100100Z","layer":"cloud_llm","event_type":"tool_result","command":"git diff","session_id":"s1","cwd":"/home","tool_name":"Bash","metadata":{"graduation_candidate":false,"first_word":"git"}}` + "\n" +
		`{"schema_version":"2","timestamp":"20260306T100200Z","layer":"cloud_llm","event_type":"inference","session_id":"s1","metadata":{"cost_estimate_usd":0.005,"token_estimate":20}}` + "\n" +
		`{"schema_version":"2","timestamp":"20260306T100300Z","layer":"claude_code","event_type":"session_summary","session_id":"s1","cwd":"/home","metadata":{"total_events":3,"tool_events":1,"prompt_count":1,"unique_commands":2,"tool_distribution":{"Bash":1},"graduation_candidates":0}}` + "\n"
	dir := singleDayEventsFile(t, "2026-03-06", content)

	c := newEventsClientAt(dir)
	batch, err := c.GetTypedEvents("2026-03-06", "2026-03-06")
	require.NoError(t, err)

	assert.Len(t, batch.ShellEvents, 1, "one shell_command event")
	assert.Len(t, batch.ToolCallEvents, 1, "one tool_result event")
	assert.Len(t, batch.InferenceEvents, 1, "one inference event")
	assert.Len(t, batch.SessionSummaries, 1, "one session_summary event")
}

func TestGetTypedEvents_SkipsMalformedLines(t *testing.T) {
	content := "not valid json\n" +
		`{"schema_version":"2","timestamp":"20260306T100000Z","layer":"fish","event_type":"shell_command","command":"good_cmd","session_id":"s1"}` + "\n" +
		"also not json\n"
	dir := singleDayEventsFile(t, "2026-03-06", content)

	c := newEventsClientAt(dir)
	batch, err := c.GetTypedEvents("2026-03-06", "2026-03-06")
	require.NoError(t, err, "malformed lines should be skipped, not error")
	assert.Len(t, batch.ShellEvents, 1, "only the valid line should be parsed")
}

func TestGetTypedEvents_SkipsCommandlessEvents(t *testing.T) {
	// Events with no command and no recognized event_type should be dropped from ShellEvents
	content := `{"schema_version":"2","timestamp":"20260306T100000Z","layer":"fish","event_type":"unknown_type","command":"","session_id":"s1"}` + "\n"
	dir := singleDayEventsFile(t, "2026-03-06", content)

	c := newEventsClientAt(dir)
	batch, err := c.GetTypedEvents("2026-03-06", "2026-03-06")
	require.NoError(t, err)
	assert.Empty(t, batch.ShellEvents, "commandless events should be dropped")
}

func TestGetTypedEvents_MultiDayRange(t *testing.T) {
	dir := t.TempDir()
	for date, cmd := range map[string]string{
		"2026-03-04": "day1_cmd",
		"2026-03-05": "day2_cmd",
		"2026-03-06": "day3_cmd",
	} {
		// compact UTC format: YYYYMMDDTHHMMSSZ
		compact := strings.ReplaceAll(date, "-", "") + "T100000Z"
		line := `{"schema_version":"2","timestamp":"` + compact + `","layer":"fish","event_type":"shell_command","command":"` + cmd + `","session_id":"s1"}` + "\n"
		err := os.WriteFile(filepath.Join(dir, date+".jsonl"), []byte(line), 0o600)
		require.NoError(t, err)
	}

	c := newEventsClientAt(dir)
	batch, err := c.GetTypedEvents("2026-03-04", "2026-03-06")
	require.NoError(t, err)
	assert.Len(t, batch.ShellEvents, 3, "one shell event per day")
}

func TestGetTypedEvents_InvalidDate(t *testing.T) {
	c := newEventsClientAt(t.TempDir())
	_, err := c.GetTypedEvents("not-a-date", "2026-03-06")
	assert.Error(t, err)
}

func TestResolveLayer_V3Layer(t *testing.T) {
	assert.Equal(t, "fish", resolveLayer(eventsRaw{Layer: "fish"}))
	assert.Equal(t, "cloud_llm", resolveLayer(eventsRaw{Layer: "cloud_llm"}))
	assert.Equal(t, "go_cli", resolveLayer(eventsRaw{Layer: "go_cli"}))
}

func TestResolveLayer_V2Source(t *testing.T) {
	assert.Equal(t, "cloud_llm", resolveLayer(eventsRaw{Source: "claude_code"}))
	assert.Equal(t, "interactive_shell", resolveLayer(eventsRaw{Source: "interactive_shell"}))
}

func TestResolveLayer_LayerTakesPrecedenceOverSource(t *testing.T) {
	// When both layer and source are present (shouldn't happen, but be safe)
	result := resolveLayer(eventsRaw{Layer: "fish", Source: "interactive_shell"})
	assert.Equal(t, "fish", result, "layer field takes precedence over source")
}
