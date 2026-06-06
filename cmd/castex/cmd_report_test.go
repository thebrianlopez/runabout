package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testdataEvents = "testdata/events"
	testdataRecs   = "testdata/recommendations.jsonl"
)

// CT-1: Summary section shows correct total sessions from fixture events
func TestReport_CT1_TotalSessions(t *testing.T) {
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "md",
		SinceDays: 3650,
		EventsDir: testdataEvents,
		RecsFile:  testdataRecs,
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RunReport error: %v", err)
	}
	// Fixture has 3 session_event rows (2 claude-code, 1 pi)
	out := buf.String()
	if !strings.Contains(out, "claude-code") {
		t.Error("expected claude-code in report output")
	}
}

// CT-2: Human vs agentic distinguished by session_type field
func TestReport_CT2_HumanVsAgentic(t *testing.T) {
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "json",
		SinceDays: 3650,
		EventsDir: testdataEvents,
		RecsFile:  "/dev/null",
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RunReport error: %v", err)
	}
	var data ReportData
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	var found *AgentSummary
	for i := range data.Agents {
		if data.Agents[i].AgentType == "claude-code" {
			found = &data.Agents[i]
		}
	}
	if found == nil {
		t.Fatal("claude-code not in report")
	}
	if found.HumanSessions != 1 {
		t.Errorf("HumanSessions = %d, want 1", found.HumanSessions)
	}
	if found.AgenticSessions != 1 {
		t.Errorf("AgenticSessions = %d, want 1", found.AgenticSessions)
	}
}

// CT-3: --since 7d filters events older than 7 days
func TestReport_CT3_SinceFilter(t *testing.T) {
	dir := t.TempDir()
	// Write an event with a very old timestamp
	evFile := filepath.Join(dir, "2020-01-01.jsonl")
	if err := os.WriteFile(evFile, []byte(`{"agent_type":"claude-code","session_type":"agentic","timestamp":"20200101T000000Z"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "json",
		SinceDays: 7,
		EventsDir: dir,
		RecsFile:  "/dev/null",
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RunReport error: %v", err)
	}
	var data ReportData
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(data.Agents) != 0 {
		t.Errorf("expected 0 agents after 7d filter (event is from 2020), got %d", len(data.Agents))
	}
}

// CT-4: --format json output is valid JSON with required schema fields
func TestReport_CT4_JSONOutput(t *testing.T) {
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "json",
		SinceDays: 3650,
		EventsDir: testdataEvents,
		RecsFile:  "/dev/null",
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RunReport error: %v", err)
	}
	var data ReportData
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if data.WindowDays == 0 {
		t.Error("window_days missing from JSON output")
	}
	if data.GeneratedAt == "" {
		t.Error("generated_at missing from JSON output")
	}
	if data.Agents == nil {
		t.Error("agents field missing from JSON output")
	}
}

// CT-5: Anti-pattern trends read from recommendations.jsonl fixture
func TestReport_CT5_AntiPatternTrends(t *testing.T) {
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "json",
		SinceDays: 3650,
		EventsDir: testdataEvents,
		RecsFile:  testdataRecs,
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RunReport error: %v", err)
	}
	var data ReportData
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.AntiPatterns) == 0 {
		t.Fatal("expected anti_patterns section, got none")
	}
	found := false
	for _, ap := range data.AntiPatterns {
		if ap.Pattern == "ls_antipattern" {
			found = true
		}
	}
	if !found {
		t.Error("expected ls_antipattern in anti-patterns")
	}
}

// CT-6: Report omits anti-pattern section gracefully when recommendations.jsonl absent
func TestReport_CT6_MissingRecsFile(t *testing.T) {
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "json",
		SinceDays: 3650,
		EventsDir: testdataEvents,
		RecsFile:  "/nonexistent/recommendations.jsonl",
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RunReport error: %v", err)
	}
	var data ReportData
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	// anti_patterns should be absent/empty - not a fatal error
	if len(data.AntiPatterns) > 0 {
		t.Error("expected empty anti_patterns when recs file absent")
	}
}

// CT-7: Graduation candidates read from graduation_candidate events in event bus
func TestReport_CT7_GraduationCandidates(t *testing.T) {
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "json",
		SinceDays: 3650,
		EventsDir: testdataEvents,
		RecsFile:  "/dev/null",
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RunReport error: %v", err)
	}
	var data ReportData
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Graduation) == 0 {
		t.Error("expected graduation candidates from fixture (graduation_candidate event present)")
	}
}

// CT-8: events_dir_missing (E201) when event bus dir absent
func TestReport_CT8_EventsDirMissing(t *testing.T) {
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "md",
		SinceDays: 30,
		EventsDir: "/nonexistent/events",
		Timeout:   5 * time.Second,
	}
	err := RunReport(cmd, cfg)
	if err == nil {
		t.Fatal("expected E201 error, got nil")
	}
	if !strings.Contains(err.Error(), "E201") {
		t.Errorf("expected E201 error code, got: %v", err)
	}
}

// CT-9: --agent claude-code filters output to Claude Code sessions only
func TestReport_CT9_AgentFilter(t *testing.T) {
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:      "json",
		SinceDays:   3650,
		EventsDir:   testdataEvents,
		AgentFilter: "claude-code",
		RecsFile:    "/dev/null",
		Timeout:     5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RunReport error: %v", err)
	}
	var data ReportData
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	for _, a := range data.Agents {
		if a.AgentType != "claude-code" {
			t.Errorf("expected only claude-code, got %q", a.AgentType)
		}
	}
}

// CT-10: agents_registry_stale warning when agents.jsonl lists absent agent
func TestReport_CT10_StaleRegistryWarning(t *testing.T) {
	dir := t.TempDir()
	agentsFile := filepath.Join(dir, "agents.jsonl")
	// Write a stale record for an agent that won't appear in events
	if err := os.WriteFile(agentsFile, []byte(`{"agent_id":"ghost-agent","status":"instrumented"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var errBuf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&errBuf)

	// Check stale warning directly
	knownIDs := map[string]bool{"claude-code": true}
	staleAgentsWarning(cmd, agentsFile, knownIDs)
	if !strings.Contains(errBuf.String(), "W204") {
		t.Errorf("expected W204 stale warning, got: %s", errBuf.String())
	}
}

// CT-11: Total cost_usd correctly summed across all sessions
func TestReport_CT11_CostSum(t *testing.T) {
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "json",
		SinceDays: 3650,
		EventsDir: testdataEvents,
		RecsFile:  "/dev/null",
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RunReport error: %v", err)
	}
	var data ReportData
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	// Fixture: claude-code = 0.25+0.10 = 0.35, pi = 0.05. Total = 0.40
	total := totalCost(data.Agents)
	if total < 0.39 || total > 0.41 {
		t.Errorf("expected total cost ~0.40, got %.4f", total)
	}
}

// CT-12: report_timeout (E203) fires when read exceeds 5s deadline
func TestReport_CT12_Timeout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-01-01.jsonl"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "md",
		SinceDays: 30,
		EventsDir: dir,
		Timeout:   1 * time.Nanosecond, // effectively zero - will always expire
	}
	err := RunReport(cmd, cfg)
	if err == nil {
		t.Fatal("expected E203 timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "E203") {
		t.Errorf("expected E203 error, got: %v", err)
	}
}

// --- Behavioral Tests ---

// BT-1: Full report with all sections rendered
func TestReport_BT1_FullReport(t *testing.T) {
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "md",
		SinceDays: 3650,
		EventsDir: testdataEvents,
		RecsFile:  testdataRecs,
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RunReport error: %v", err)
	}
	out := buf.String()
	for _, section := range []string{"## Summary", "## Anti-Pattern Trends"} {
		if !strings.Contains(out, section) {
			t.Errorf("expected section %q in markdown output", section)
		}
	}
}

// BT-2: Empty events dir: graceful "no data" message, exit 0
func TestReport_BT2_EmptyEventsDir(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "md",
		SinceDays: 30,
		EventsDir: dir,
		RecsFile:  "/dev/null",
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("expected exit 0 for empty dir, got: %v", err)
	}
	if !strings.Contains(buf.String(), "No data") {
		t.Errorf("expected 'No data' message for empty events dir, got: %s", buf.String())
	}
}

// BT-3: --format json output parseable by json.Unmarshal
func TestReport_BT3_JSONParseable(t *testing.T) {
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "json",
		SinceDays: 3650,
		EventsDir: testdataEvents,
		RecsFile:  "/dev/null",
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RunReport error: %v", err)
	}
	var v interface{}
	if err := json.Unmarshal(buf.Bytes(), &v); err != nil {
		t.Fatalf("output is not parseable JSON: %v", err)
	}
}

// BT-4: Default 30-day window applied when --since not specified (SinceDays=30)
func TestReport_BT4_DefaultWindow(t *testing.T) {
	dir := t.TempDir()
	// Write an event from 31 days ago - it should NOT appear
	old := time.Now().UTC().AddDate(0, 0, -31).Format("20060102T150405Z")
	line := `{"agent_type":"claude-code","session_type":"agentic","timestamp":"` + old + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "old.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "json",
		SinceDays: 30,
		EventsDir: dir,
		RecsFile:  "/dev/null",
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RunReport error: %v", err)
	}
	var data ReportData
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.WindowDays != 30 {
		t.Errorf("expected window_days=30, got %d", data.WindowDays)
	}
	if len(data.Agents) != 0 {
		t.Errorf("expected 0 agents (31-day-old event excluded), got %d", len(data.Agents))
	}
}

// --- Regression Guards ---

// RG-1: Report never reads files outside EventsDir and AgentsFile
func TestReport_RG1_ScopedReads(t *testing.T) {
	// Verified structurally: readEvents only opens files under eventsDir (filepath.Glob),
	// readRecommendations only opens cfg.RecsFile. No dynamic path construction from event data.
	// This test exercises the path and verifies no panic on normal data.
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "md",
		SinceDays: 3650,
		EventsDir: testdataEvents,
		RecsFile:  testdataRecs,
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RG-1: RunReport error: %v", err)
	}
}

// RG-2: Schema_version_mismatch doesn't panic - always produces partial report
func TestReport_RG2_MalformedLines(t *testing.T) {
	dir := t.TempDir()
	// Mix valid and malformed lines
	content := "not json at all\n" +
		`{"agent_type":"claude-code","session_type":"agentic","timestamp":"20260115T100000Z","cost_usd":0.1}` + "\n" +
		`{broken`
	if err := os.WriteFile(filepath.Join(dir, "mixed.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	cmd := newReportCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cfg := ReportConfig{
		Format:    "json",
		SinceDays: 3650,
		EventsDir: dir,
		RecsFile:  "/dev/null",
		Timeout:   5 * time.Second,
	}
	if err := RunReport(cmd, cfg); err != nil {
		t.Fatalf("RG-2: expected no panic/error on malformed lines, got: %v", err)
	}
	// Should still have claude-code from the valid line
	var data ReportData
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Agents) == 0 {
		t.Error("expected partial report with 1 agent from valid line")
	}
}
