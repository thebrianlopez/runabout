package chainindex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The conformance fields F7 adds are derived during the scan, so they are
// tested against real markdown rather than hand-built records.

func TestClassifyUpstreamState(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		extracted string
		want      string
	}{
		{
			name:      "extracted",
			content:   "| **Source FDD** | `PERSONAL_x_FDD.md` |\n",
			extracted: "PERSONAL_x_FDD.md",
			want:      UpstreamExtracted,
		},
		{
			// The EPIC-266 sub-class: bold markers absent, so extractTableField
			// returns nothing while a reviewer reads a correctly linked artifact.
			name:      "declared without bold markers",
			content:   "| Source FDD | `PERSONAL_x_FDD.md` |\n",
			extracted: "",
			want:      UpstreamDeclaredUnextractable,
		},
		{
			// The EPIC-164 sub-class: a trailing annotation after the value.
			name:      "declared with trailing annotation",
			content:   "| **Source FDD** | `PERSONAL_x_FDD.md` (F5) |\n",
			extracted: "",
			want:      UpstreamDeclaredUnextractable,
		},
		{
			name:      "declared as a list item",
			content:   "- Source PRD: `PERSONAL_x_PRD.md`\n",
			extracted: "",
			want:      UpstreamDeclaredUnextractable,
		},
		{
			name:      "no upstream declared anywhere",
			content:   "# Title\n\nSome prose.\n",
			extracted: "",
			want:      UpstreamAbsent,
		},
		{
			// Prose mentioning the field name must not be read as a declaration.
			name:      "prose mention is not a declaration",
			content:   "The Source FDD row must be bolded to extract.\n",
			extracted: "",
			want:      UpstreamAbsent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyUpstreamState(tc.content, tc.extracted); got != tc.want {
				t.Errorf("classifyUpstreamState = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseArtifact_ConformanceFields(t *testing.T) {
	content := `---
status: Ready
agents:
  - id: runabout-agent
    cwd: ~/workspaces/linkari/runabout
    milestones: [M2, M3]
---

# EPIC: Something

| Field | Value |
|-------|-------|
| **Chain Runtime Version** | ` + "`2.0.1`" + ` |
| **Source FDD** | ` + "`PERSONAL_20260101T000000Z_X_FDD.md`" + ` |
`
	rec := parseArtifact("epics/x.md", ArtifactEpic, content)

	if !rec.HasFrontmatter {
		t.Error("expected HasFrontmatter true")
	}
	if rec.RuntimeVersion != "2.0.1" {
		t.Errorf("RuntimeVersion = %q, want %q", rec.RuntimeVersion, "2.0.1")
	}
	if rec.UpstreamState != UpstreamExtracted {
		t.Errorf("UpstreamState = %q, want %q", rec.UpstreamState, UpstreamExtracted)
	}
	if len(rec.EpicAgents) != 1 {
		t.Fatalf("expected 1 agent assignment, got %d", len(rec.EpicAgents))
	}
	if rec.EpicAgents[0].ID != "runabout-agent" {
		t.Errorf("agent id = %q", rec.EpicAgents[0].ID)
	}
}

func TestParseArtifact_NoFrontmatter(t *testing.T) {
	rec := parseArtifact("epics/y.md", ArtifactEpic, "# EPIC: Legacy\n\nNo frontmatter here.\n")
	if rec.HasFrontmatter {
		t.Error("expected HasFrontmatter false")
	}
	if rec.EpicAgents != nil {
		t.Error("expected no agent assignments without frontmatter")
	}
	if rec.RuntimeVersion != "" {
		t.Errorf("expected empty RuntimeVersion, got %q", rec.RuntimeVersion)
	}
}

// A malformed milestones value must survive parsing so the validator can reject
// it. Typing it away at parse time would hide exactly the CT-12 defect.
func TestParseArtifact_MalformedAgentsSurvivesParsing(t *testing.T) {
	content := `---
status: Ready
agents:
  - id: runabout-agent
    milestones: M2
---

# EPIC
`
	rec := parseArtifact("epics/z.md", ArtifactEpic, content)
	if len(rec.EpicAgents) != 1 {
		t.Fatalf("expected 1 agent assignment, got %d", len(rec.EpicAgents))
	}
	if _, isList := rec.EpicAgents[0].Milestones.([]any); isList {
		t.Error("expected the malformed scalar milestones value to survive as a scalar")
	}
}

// ─────────────────────────────────────────────────────────────
// Event emission
// ─────────────────────────────────────────────────────────────

func TestBuildSchemaEvents_AggregatesAndOrders(t *testing.T) {
	violations := []SchemaViolation{
		{ArtifactPath: "epics/a.md", ArtifactType: "epic", Rule: RuleStatusEnum, Detected: "Done", Expected: "X", Severity: SeverityError},
		{ArtifactPath: "epics/b.md", ArtifactType: "epic", Rule: RuleStatusEnum, Detected: "Done", Expected: "X", Severity: SeverityError},
		// Warnings are legacy drift and are not emitted.
		{ArtifactPath: "epics/c.md", ArtifactType: "epic", Rule: RuleStatusEnum, Detected: "Done", Expected: "X", Severity: SeverityWarning},
	}
	records := []ArtifactRecord{
		{Path: "epics/a.md", Type: ArtifactEpic, Status: "Done"},
		{Path: "epics/b.md", Type: ArtifactEpic, Status: "Done"},
		{Path: "epics/d.md", Type: ArtifactEpic, Status: "Complete"},
	}

	events := buildSchemaEvents(violations, records, time.Now())

	var violationEvents, statusEvents []busEvent
	for _, e := range events {
		switch e.EventType {
		case EventSchemaViolation:
			violationEvents = append(violationEvents, e)
		case EventStatusValueObserved:
			statusEvents = append(statusEvents, e)
		}
	}

	if len(violationEvents) != 1 {
		t.Fatalf("expected 1 aggregated schema_violation event, got %d", len(violationEvents))
	}
	if violationEvents[0].Count != 2 {
		t.Errorf("expected count 2 (errors only, warning excluded), got %d", violationEvents[0].Count)
	}

	if len(statusEvents) != 2 {
		t.Fatalf("expected 2 status_value_observed events, got %d", len(statusEvents))
	}
	for _, e := range statusEvents {
		if e.Conformant == nil {
			t.Fatal("conformant must be set on status_value_observed")
		}
		switch e.Value {
		case "Done":
			if *e.Conformant {
				t.Error("Done must be reported non-conformant")
			}
			if e.Count != 2 {
				t.Errorf("expected Done count 2, got %d", e.Count)
			}
		case "Complete":
			if !*e.Conformant {
				t.Error("Complete must be reported conformant")
			}
		}
	}

	// Deterministic ordering so repeated builds diff cleanly.
	again := buildSchemaEvents(violations, records, time.Now())
	for i := range events {
		if events[i].EventType != again[i].EventType || events[i].Value != again[i].Value {
			t.Errorf("event ordering is not deterministic at %d", i)
		}
	}
}

// Emission appends; it never rewrites or backfills existing history.
func TestEmitSchemaEvents_AppendsOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	eventsDir := filepath.Join(dir, "events")
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(eventsDir, nowFunc().UTC().Format(eventFileLayout)+".jsonl")
	sentinel := `{"schema_version":"2","event_type":"pre_existing"}` + "\n"
	if err := os.WriteFile(existing, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	violations := []SchemaViolation{
		{ArtifactPath: "epics/a.md", ArtifactType: "epic", Rule: RuleStatusEnum, Detected: "Done", Severity: SeverityError},
	}
	records := []ArtifactRecord{{Path: "epics/a.md", Type: ArtifactEpic, Status: "Done"}}

	if err := EmitSchemaEvents(violations, records); err != nil {
		t.Fatalf("EmitSchemaEvents: %v", err)
	}

	content, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), sentinel) {
		t.Error("pre-existing history was rewritten; emission must be append-only")
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected the sentinel plus emitted events, got %d lines", len(lines))
	}
	for _, line := range lines[1:] {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("emitted line is not valid JSON: %v", err)
		}
		if e["layer"] != "go_cli" {
			t.Errorf("layer = %v, want go_cli", e["layer"])
		}
		ts, _ := e["timestamp"].(string)
		if len(ts) != 16 || !strings.HasSuffix(ts, "Z") {
			t.Errorf("timestamp %q is not compact ISO 8601 UTC (YYYYMMDDTHHMMSSZ)", ts)
		}
	}
}

// Tests must never reach the operator's real event bus.
func TestEventsDir_HonorsOverride(t *testing.T) {
	t.Setenv("AUTOMATION_METRICS_DIR", "/tmp/f7-events-probe")
	if got, want := EventsDir(), filepath.Join("/tmp/f7-events-probe", "events"); got != want {
		t.Errorf("EventsDir() = %q, want %q", got, want)
	}
}
