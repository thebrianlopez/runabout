package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseAuditTimestamp(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
		wantUTC string // expected UTC formatted as "2006-01-02 15:04:05"
	}{
		// v3: compact UTC from automation-metrics events pipeline
		{"20260228T231954Z", false, "2026-02-28 23:19:54"},
		// 2026 format: ISO 8601 with numeric TZ offset (no colon)
		{"2026-02-23T09:26:35-0600", false, "2026-02-23 15:26:35"},
		// 2025 format: space-separated, treated as UTC
		{"2025-04-01 11:59:01", false, "2025-04-01 11:59:01"},
		// RFC3339 with colon — future-proof
		{"2026-02-23T15:26:35Z", false, "2026-02-23 15:26:35"},
		// Invalid
		{"not-a-timestamp", true, ""},
		{"", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseAuditTimestamp(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got none", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tt.input, err)
				return
			}
			gotUTC := got.UTC().Format("2006-01-02 15:04:05")
			if gotUTC != tt.wantUTC {
				t.Errorf("parseAuditTimestamp(%q) UTC = %q, want %q", tt.input, gotUTC, tt.wantUTC)
			}
		})
	}
}

func TestParseAuditLogFile_V1Schema(t *testing.T) {
	// 2025 schema: no session_id, no cwd, space-separated timestamp
	content := `{"version":"1.0","timestamp":"2025-04-01 11:59:01","event_type":"interactive_shell","command":"ls -la","source":"interactive_shell"}` + "\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "2025-04-01.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	start := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(2025, 4, 1, 23, 59, 59, 0, time.UTC).Unix()

	events, err := parseAuditLogFile(path, start, end)
	if err != nil {
		t.Fatalf("parseAuditLogFile error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	e := events[0]
	if e.Command != "ls -la" {
		t.Errorf("Command = %q, want %q", e.Command, "ls -la")
	}
	if e.Source != "interactive_shell" {
		t.Errorf("Source = %q, want interactive_shell", e.Source)
	}
	if e.SessionID != "" {
		t.Errorf("SessionID should be empty for v1 schema, got %q", e.SessionID)
	}
	if e.Cwd != "" {
		t.Errorf("Cwd should be empty for v1 schema, got %q", e.Cwd)
	}
}

func TestParseAuditLogFile_V3EventsPipelineSchema(t *testing.T) {
	// v3 schema from ~/.automation-metrics/events/: schema_version, layer, compact UTC timestamp
	content := `{"schema_version":"2","timestamp":"20260228T231954Z","layer":"fish","event_type":"test","command":"smoke_test","session_id":"480ac319-6f4d-4fd6-8fd1-e69f07d80a55","user":"brian","cwd":"/Users/brian/.config/fish"}` + "\n" +
		`{"schema_version":"2","timestamp":"20260228T231958Z","layer":"cloud_llm","event_type":"inference","command":"claude_prompt","session_id":"35726d0b-894a-4027-a645-059ee22b95ed","user":"brian","cwd":"/Users/brian/.config/fish"}` + "\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "2026-02-28.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	start := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(2026, 2, 28, 23, 59, 59, 0, time.UTC).Unix()

	events, err := parseAuditLogFile(path, start, end)
	if err != nil {
		t.Fatalf("parseAuditLogFile error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	// Verify layer→source mapping
	if events[0].Source != "interactive_shell" {
		t.Errorf("events[0].Source = %q, want interactive_shell (mapped from layer=fish)", events[0].Source)
	}
	if events[1].Source != "claude_code" {
		t.Errorf("events[1].Source = %q, want claude_code (mapped from layer=cloud_llm)", events[1].Source)
	}
	if events[0].SessionID != "480ac319-6f4d-4fd6-8fd1-e69f07d80a55" {
		t.Errorf("events[0].SessionID = %q, want UUID", events[0].SessionID)
	}
	if events[0].Command != "smoke_test" {
		t.Errorf("events[0].Command = %q, want smoke_test", events[0].Command)
	}
}

func TestParseAuditLogFile_V2Schema(t *testing.T) {
	// 2026 schema: full fields including session_id, cwd, user
	content := `{"version":"1.0","timestamp":"2026-02-23T09:26:35-0600","event_type":"interactive_shell","command":"kubectl get pods","session_id":"9b03d2af-22a4-4629-960c-a6f828ad95e3","cwd":"/Users/brian","user":"brian","source":"interactive_shell"}` + "\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "2026-02-23.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	start := time.Date(2026, 2, 23, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(2026, 2, 23, 23, 59, 59, 0, time.UTC).Unix()

	events, err := parseAuditLogFile(path, start, end)
	if err != nil {
		t.Fatalf("parseAuditLogFile error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	e := events[0]
	if e.Command != "kubectl get pods" {
		t.Errorf("Command = %q, want %q", e.Command, "kubectl get pods")
	}
	if e.SessionID != "9b03d2af-22a4-4629-960c-a6f828ad95e3" {
		t.Errorf("SessionID = %q, want UUID", e.SessionID)
	}
	if e.Cwd != "/Users/brian" {
		t.Errorf("Cwd = %q, want /Users/brian", e.Cwd)
	}
	if e.Source != "interactive_shell" {
		t.Errorf("Source = %q, want interactive_shell", e.Source)
	}
}

func TestParseAuditLogFile_FileAbsent(t *testing.T) {
	events, err := parseAuditLogFile("/nonexistent/audit.jsonl", 0, 9999999)
	if err != nil {
		t.Errorf("should not error on absent file, got: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("should return empty slice on absent file, got %d events", len(events))
	}
}

func TestParseAuditLogFile_SkipMalformedLines(t *testing.T) {
	content := "not valid json\n" +
		`{"version":"1.0","timestamp":"2025-04-01 11:59:01","command":"ls","source":"interactive_shell"}` + "\n" +
		"also not json\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	events, err := parseAuditLogFile(path, 0, 9999999999)
	if err != nil {
		t.Fatalf("parseAuditLogFile error: %v", err)
	}
	// Only the valid JSON line should be parsed
	if len(events) != 1 {
		t.Errorf("len(events) = %d, want 1 (malformed lines skipped)", len(events))
	}
}

func TestParseAuditLogFile_LeadingSpaceInCommand(t *testing.T) {
	// Real 2025 audit files show leading space in command field
	content := `{"version":"1.0","timestamp":"2025-04-01 11:59:01","command":" ls","source":"interactive_shell"}` + "\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "spaced.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	events, err := parseAuditLogFile(path, 0, 9999999999)
	if err != nil {
		t.Fatalf("parseAuditLogFile error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	// TrimSpace should strip the leading space
	if events[0].Command != "ls" {
		t.Errorf("Command = %q, want %q (leading space trimmed)", events[0].Command, "ls")
	}
}

func TestGetEvents_DirectoryAbsent(t *testing.T) {
	c := newAuditLogClientAt("/nonexistent/terminal-history")
	events, err := c.GetEvents("2026-01-01", "2026-01-07")
	if err != nil {
		t.Errorf("GetEvents should not error on absent directory, got: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("GetEvents should return empty slice on absent directory, got %d events", len(events))
	}
}

func TestGetEvents_PrimaryDirTakesPrecedence(t *testing.T) {
	primary := t.TempDir()
	fallback := t.TempDir()

	// Both dirs have data for the same day, with different commands
	primaryContent := `{"schema_version":"2","timestamp":"20260228T100000Z","layer":"fish","event_type":"test","command":"primary_cmd","session_id":"aaa","user":"brian","cwd":"/tmp"}` + "\n"
	fallbackContent := `{"version":"1.0","timestamp":"2026-02-28 10:00:00","command":"fallback_cmd","source":"interactive_shell"}` + "\n"

	if err := os.WriteFile(filepath.Join(primary, "2026-02-28.jsonl"), []byte(primaryContent), 0o600); err != nil {
		t.Fatalf("write primary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fallback, "2026-02-28.jsonl"), []byte(fallbackContent), 0o600); err != nil {
		t.Fatalf("write fallback: %v", err)
	}

	c := &AuditLogClient{dirs: []string{primary, fallback}}
	events, err := c.GetEvents("2026-02-28", "2026-02-28")
	if err != nil {
		t.Fatalf("GetEvents error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1 (primary only)", len(events))
	}
	if events[0].Command != "primary_cmd" {
		t.Errorf("Command = %q, want primary_cmd (primary dir should win)", events[0].Command)
	}
}

func TestGetEvents_FallbackWhenPrimaryEmpty(t *testing.T) {
	primary := t.TempDir() // empty — no files
	fallback := t.TempDir()

	fallbackContent := `{"version":"1.0","timestamp":"2026-02-28 10:00:00","command":"fallback_cmd","source":"interactive_shell"}` + "\n"
	if err := os.WriteFile(filepath.Join(fallback, "2026-02-28.jsonl"), []byte(fallbackContent), 0o600); err != nil {
		t.Fatalf("write fallback: %v", err)
	}

	c := &AuditLogClient{dirs: []string{primary, fallback}}
	events, err := c.GetEvents("2026-02-28", "2026-02-28")
	if err != nil {
		t.Fatalf("GetEvents error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1 (fallback)", len(events))
	}
	if events[0].Command != "fallback_cmd" {
		t.Errorf("Command = %q, want fallback_cmd", events[0].Command)
	}
}

func TestGetSessionSummaries_ParsesMetadata(t *testing.T) {
	content := `{"schema_version":"2","timestamp":"20260302T142229Z","layer":"claude_code","event_type":"session_summary","command":"session_stop","session_id":"sess-1","user":"brian","cwd":"/Users/brian/code/proj","metadata":{"total_events":54,"tool_events":53,"prompt_count":0,"unique_commands":27,"tool_distribution":{"Bash":53},"event_types":{"inference":1,"tool_result":26,"tool_use":27},"exit_codes":{},"graduation_candidates":10,"first_event":"20260302T141710Z","last_event":"20260302T142208Z"}}` + "\n" +
		`{"schema_version":"2","timestamp":"20260302T141710Z","layer":"cloud_llm","event_type":"inference","command":"claude_prompt","session_id":"sess-1","user":"brian","cwd":"/Users/brian/code/proj","metadata":{"task_type":"help_me","token_estimate":47,"cost_estimate_usd":0.0150,"cost_note":"estimate only"}}` + "\n" +
		`{"schema_version":"2","timestamp":"20260302T141800Z","layer":"cloud_llm","event_type":"inference","command":"claude_prompt","session_id":"sess-1","user":"brian","cwd":"/Users/brian/code/proj","metadata":{"task_type":"help_more","token_estimate":22,"cost_estimate_usd":0.0050,"cost_note":"estimate only"}}` + "\n"

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-03-02.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c := newAuditLogClientAt(dir)
	summaries, err := c.GetSessionSummaries("2026-03-02", "2026-03-02")
	if err != nil {
		t.Fatalf("GetSessionSummaries error: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}

	s := summaries[0]
	if s.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", s.SessionID)
	}
	if s.TotalEvents != 54 {
		t.Errorf("TotalEvents = %d, want 54", s.TotalEvents)
	}
	if s.ToolEvents != 53 {
		t.Errorf("ToolEvents = %d, want 53", s.ToolEvents)
	}
	if s.GraduationCandidates != 10 {
		t.Errorf("GraduationCandidates = %d, want 10", s.GraduationCandidates)
	}
	if s.ToolDistribution["Bash"] != 53 {
		t.Errorf("ToolDistribution[Bash] = %d, want 53", s.ToolDistribution["Bash"])
	}
	// Cost from two inference events: 0.015 + 0.005 = 0.02
	if s.CostEstimateUSD < 0.019 || s.CostEstimateUSD > 0.021 {
		t.Errorf("CostEstimateUSD = %f, want ~0.02", s.CostEstimateUSD)
	}
	// Duration: 20260302T141710Z to 20260302T142208Z = ~4.97 min
	if s.FirstEvent.IsZero() {
		t.Error("FirstEvent should not be zero")
	}
	if s.LastEvent.IsZero() {
		t.Error("LastEvent should not be zero")
	}
	dur := s.LastEvent.Sub(s.FirstEvent).Minutes()
	if dur < 4.0 || dur > 6.0 {
		t.Errorf("session duration = %.2f min, want ~5", dur)
	}
}

func TestGetSessionSummaries_AbsentDir(t *testing.T) {
	c := newAuditLogClientAt("/nonexistent/dir")
	summaries, err := c.GetSessionSummaries("2026-03-01", "2026-03-01")
	if err != nil {
		t.Errorf("should not error on absent dir, got: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("should return empty slice, got %d", len(summaries))
	}
}

func TestGetEvents_DateRangeSpansMultipleFiles(t *testing.T) {
	dir := t.TempDir()

	day1 := `{"version":"1.0","timestamp":"2026-02-10 10:00:00","command":"git status","source":"interactive_shell"}` + "\n"
	day2 := `{"version":"1.0","timestamp":"2026-02-11 10:00:00","command":"git diff","source":"interactive_shell"}` + "\n"
	day3 := `{"version":"1.0","timestamp":"2026-02-12 10:00:00","command":"git log","source":"interactive_shell"}` + "\n"

	for name, content := range map[string]string{
		"2026-02-10.jsonl": day1,
		"2026-02-11.jsonl": day2,
		"2026-02-12.jsonl": day3,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	c := newAuditLogClientAt(dir)
	events, err := c.GetEvents("2026-02-10", "2026-02-12")
	if err != nil {
		t.Fatalf("GetEvents error: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("len(events) = %d, want 3 (one per day)", len(events))
	}
}
