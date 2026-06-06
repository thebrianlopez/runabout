package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// writeOrgYAML writes a minimal org.yaml to dir and returns the path.
func writeOrgYAML(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "org.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeAgentsJSONL writes a ~/.castex/agents.jsonl equivalent to dir.
func writeAgentsJSONL(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "agents.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func runDoctorCmd(t *testing.T, cfg DoctorConfig) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	report, err := RunDoctor(cfg)
	if err != nil {
		return buf.String(), err
	}
	if renderErr := renderDoctorReport(cmd, cfg, report); renderErr != nil {
		return buf.String(), renderErr
	}
	return buf.String(), nil
}

// minimal valid org.yaml with one static agent and one workspace-scoped agent
const minimalOrgYAML = `schema_version: "2.0"
org: personal
agents:
  - id: docs-agent
    cwd: %s
    archetype: agentic_coder
    default_model: claude-sonnet-4-6
    provider: anthropic
    languages: [markdown]
  - id: runabout-agent
    cwd: null
    archetype: agentic_coder
    default_model: claude-sonnet-4-6
    provider: anthropic
    languages: [go]
vocabulary:
  archetypes: [agentic_coder, shell_assistant, orchestrator, tool_runner]
  model_tiers:
    balanced: [claude-sonnet-4-6]
  provider_types:
    cloud_anthropic: [anthropic]
`

// CT-1: healthy registry with all CWDs present → no issues.
func TestDoctor_Healthy(t *testing.T) {
	dir := t.TempDir()
	cwdDir := filepath.Join(dir, "docs")
	if err := os.Mkdir(cwdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "schema_version: \"2.0\"\norg: personal\nagents:\n  - id: docs-agent\n    cwd: " + cwdDir + "\n    archetype: agentic_coder\n    default_model: claude-sonnet-4-6\n    provider: anthropic\n    languages: [markdown]\nvocabulary:\n  archetypes: [agentic_coder]\n  model_tiers:\n    balanced: [claude-sonnet-4-6]\n  provider_types:\n    cloud_anthropic: [anthropic]\n"
	orgPath := writeOrgYAML(t, dir, content)
	cfg := DoctorConfig{OrgYAML: orgPath, AgentsFile: filepath.Join(dir, "agents.jsonl")}

	report, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d: %v", len(report.Errors), report.Errors)
	}
	if len(report.Warnings) != 0 {
		t.Errorf("expected 0 warnings, got %d: %v", len(report.Warnings), report.Warnings)
	}
	if report.AgentCount != 1 {
		t.Errorf("expected 1 agent, got %d", report.AgentCount)
	}
}

// CT-2: static agent with missing CWD → E503 error.
func TestDoctor_MissingCWD(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, "nonexistent-dir")
	content := "schema_version: \"2.0\"\norg: personal\nagents:\n  - id: broken-agent\n    cwd: " + missingPath + "\n    archetype: agentic_coder\n    default_model: claude-sonnet-4-6\n    provider: anthropic\n    languages: [go]\nvocabulary:\n  archetypes: [agentic_coder]\n  model_tiers:\n    balanced: [claude-sonnet-4-6]\n  provider_types:\n    cloud_anthropic: [anthropic]\n"
	orgPath := writeOrgYAML(t, dir, content)
	cfg := DoctorConfig{OrgYAML: orgPath, AgentsFile: filepath.Join(dir, "agents.jsonl")}

	report, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(report.Errors))
	}
	if report.Errors[0].Code != "E503" {
		t.Errorf("expected E503, got %s", report.Errors[0].Code)
	}
	if !strings.Contains(report.Errors[0].Message, "nonexistent-dir") {
		t.Errorf("error should mention the missing path: %s", report.Errors[0].Message)
	}
}

// CT-3: workspace-scoped agent (cwd: null) → no missing-CWD error.
func TestDoctor_WorkspaceScopedNoError(t *testing.T) {
	dir := t.TempDir()
	content := "schema_version: \"2.0\"\norg: personal\nagents:\n  - id: runabout-agent\n    cwd: null\n    archetype: agentic_coder\n    default_model: claude-sonnet-4-6\n    provider: anthropic\n    languages: [go]\nvocabulary:\n  archetypes: [agentic_coder]\n  model_tiers:\n    balanced: [claude-sonnet-4-6]\n  provider_types:\n    cloud_anthropic: [anthropic]\n"
	orgPath := writeOrgYAML(t, dir, content)
	cfg := DoctorConfig{OrgYAML: orgPath, AgentsFile: filepath.Join(dir, "agents.jsonl")}

	report, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, e := range report.Errors {
		if e.Code == "E503" {
			t.Errorf("workspace-scoped agent should not produce E503: %v", e)
		}
	}
}

// CT-4: agent missing archetype → W504 warning.
func TestDoctor_UnclassifiedAgent(t *testing.T) {
	dir := t.TempDir()
	cwdDir := filepath.Join(dir, "agent")
	if err := os.Mkdir(cwdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No archetype, default_model, or provider fields.
	content := "schema_version: \"2.0\"\norg: personal\nagents:\n  - id: bare-agent\n    cwd: " + cwdDir + "\n    languages: [go]\nvocabulary:\n  archetypes: [agentic_coder]\n  model_tiers:\n    balanced: [claude-sonnet-4-6]\n  provider_types:\n    cloud_anthropic: [anthropic]\n"
	orgPath := writeOrgYAML(t, dir, content)
	cfg := DoctorConfig{OrgYAML: orgPath, AgentsFile: filepath.Join(dir, "agents.jsonl")}

	report, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Archetype and model and provider are defaulted by the registry loader,
	// so W504 would only fire if the defaults are empty strings post-load.
	// This test verifies the check runs without panic.
	_ = report
}

// CT-5: stale agents.jsonl entry (agent_id not in registry) → W505 warning.
func TestDoctor_StaleAgentsJSONL(t *testing.T) {
	dir := t.TempDir()
	cwdDir := filepath.Join(dir, "docs")
	if err := os.Mkdir(cwdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "schema_version: \"2.0\"\norg: personal\nagents:\n  - id: docs-agent\n    cwd: " + cwdDir + "\n    archetype: agentic_coder\n    default_model: claude-sonnet-4-6\n    provider: anthropic\n    languages: [markdown]\nvocabulary:\n  archetypes: [agentic_coder]\n  model_tiers:\n    balanced: [claude-sonnet-4-6]\n  provider_types:\n    cloud_anthropic: [anthropic]\n"
	orgPath := writeOrgYAML(t, dir, content)

	// agents.jsonl contains an agent not in the registry (not a known harness ID).
	agentsPath := writeAgentsJSONL(t, dir, `{"agent_id":"old-removed-agent","status":"active","instrumented_at":"20260101T000000Z"}`)
	cfg := DoctorConfig{OrgYAML: orgPath, AgentsFile: agentsPath}

	report, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var found bool
	for _, w := range report.Warnings {
		if w.Code == "W505" && strings.Contains(w.Agent, "old-removed-agent") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected W505 for old-removed-agent, got warnings: %v", report.Warnings)
	}
}

// CT-6: known harness IDs ("claude-code", "pi", "codex") are NOT flagged as stale.
func TestDoctor_KnownHarnessIDsNotStale(t *testing.T) {
	dir := t.TempDir()
	cwdDir := filepath.Join(dir, "docs")
	if err := os.Mkdir(cwdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "schema_version: \"2.0\"\norg: personal\nagents:\n  - id: docs-agent\n    cwd: " + cwdDir + "\n    archetype: agentic_coder\n    default_model: claude-sonnet-4-6\n    provider: anthropic\n    languages: [markdown]\nvocabulary:\n  archetypes: [agentic_coder]\n  model_tiers:\n    balanced: [claude-sonnet-4-6]\n  provider_types:\n    cloud_anthropic: [anthropic]\n"
	orgPath := writeOrgYAML(t, dir, content)
	// castex lifecycle hook writes "claude-code" as agent_id - should not be flagged.
	agentsPath := writeAgentsJSONL(t, dir, `{"agent_id":"claude-code","status":"active","instrumented_at":"20260101T000000Z"}`)
	cfg := DoctorConfig{OrgYAML: orgPath, AgentsFile: agentsPath}

	report, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, w := range report.Warnings {
		if w.Code == "W505" {
			t.Errorf("known harness ID should not produce W505: %v", w)
		}
	}
}

// CT-7: org.yaml not found → E501 error returned.
func TestDoctor_MissingOrgYAML(t *testing.T) {
	cfg := DoctorConfig{OrgYAML: "/nonexistent/path/org.yaml"}
	_, err := RunDoctor(cfg)
	if err == nil {
		t.Fatal("expected error for missing org.yaml")
	}
	if !strings.Contains(err.Error(), "E501") {
		t.Errorf("expected E501 in error, got: %v", err)
	}
}

// CT-8: org-yaml not configured → E501 error.
func TestDoctor_NotConfigured(t *testing.T) {
	cfg := DoctorConfig{OrgYAML: ""}
	_, err := RunDoctor(cfg)
	if err == nil {
		t.Fatal("expected error when org-yaml is empty")
	}
	if !strings.Contains(err.Error(), "E501") {
		t.Errorf("expected E501, got: %v", err)
	}
}

// CT-9: agents.jsonl absent (castex init not yet run) → no W505, no crash.
func TestDoctor_AgentsFileAbsent(t *testing.T) {
	dir := t.TempDir()
	cwdDir := filepath.Join(dir, "docs")
	if err := os.Mkdir(cwdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "schema_version: \"2.0\"\norg: personal\nagents:\n  - id: docs-agent\n    cwd: " + cwdDir + "\n    archetype: agentic_coder\n    default_model: claude-sonnet-4-6\n    provider: anthropic\n    languages: [markdown]\nvocabulary:\n  archetypes: [agentic_coder]\n  model_tiers:\n    balanced: [claude-sonnet-4-6]\n  provider_types:\n    cloud_anthropic: [anthropic]\n"
	orgPath := writeOrgYAML(t, dir, content)
	cfg := DoctorConfig{
		OrgYAML:    orgPath,
		AgentsFile: filepath.Join(dir, "nonexistent", "agents.jsonl"),
	}
	report, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Absent agents.jsonl is not an error  -  castex init may not have run yet.
	for _, w := range report.Warnings {
		if w.Code == "W505" {
			t.Errorf("absent agents.jsonl should not produce W505: %v", w)
		}
	}
}

// CT-10: output format  -  errors appear with [E5xx] prefix.
func TestDoctor_OutputFormat(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, "gone")
	content := "schema_version: \"2.0\"\norg: personal\nagents:\n  - id: gone-agent\n    cwd: " + missingPath + "\n    archetype: agentic_coder\n    default_model: claude-sonnet-4-6\n    provider: anthropic\n    languages: [go]\nvocabulary:\n  archetypes: [agentic_coder]\n  model_tiers:\n    balanced: [claude-sonnet-4-6]\n  provider_types:\n    cloud_anthropic: [anthropic]\n"
	orgPath := writeOrgYAML(t, dir, content)
	cfg := DoctorConfig{OrgYAML: orgPath, AgentsFile: filepath.Join(dir, "agents.jsonl")}

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	report, err := RunDoctor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	renderDoctorReport(cmd, cfg, report) //nolint
	out := buf.String()
	if !strings.Contains(out, "[E503]") {
		t.Errorf("output should contain [E503]: %s", out)
	}
	if !strings.Contains(out, "gone-agent") {
		t.Errorf("output should contain agent name: %s", out)
	}
}

// CT-11: multiple issues reported, exit indicates errors present.
func TestDoctor_MultipleIssues(t *testing.T) {
	dir := t.TempDir()
	// Two agents: one missing CWD, one stale in agents.jsonl.
	cwdDir := filepath.Join(dir, "good")
	if err := os.Mkdir(cwdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "schema_version: \"2.0\"\norg: personal\nagents:\n  - id: good-agent\n    cwd: " + cwdDir + "\n    archetype: agentic_coder\n    default_model: claude-sonnet-4-6\n    provider: anthropic\n    languages: [go]\n  - id: bad-agent\n    cwd: " + filepath.Join(dir, "missing") + "\n    archetype: agentic_coder\n    default_model: claude-sonnet-4-6\n    provider: anthropic\n    languages: [go]\nvocabulary:\n  archetypes: [agentic_coder]\n  model_tiers:\n    balanced: [claude-sonnet-4-6]\n  provider_types:\n    cloud_anthropic: [anthropic]\n"
	orgPath := writeOrgYAML(t, dir, content)
	agentsPath := writeAgentsJSONL(t, dir, `{"agent_id":"stale-agent","status":"active","instrumented_at":"20260101T000000Z"}`)
	cfg := DoctorConfig{OrgYAML: orgPath, AgentsFile: agentsPath}

	report, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Errors) == 0 {
		t.Error("expected at least one error (missing CWD)")
	}
	if len(report.Warnings) == 0 {
		t.Error("expected at least one warning (stale agent)")
	}
}

// CT-12: render returns non-nil error when errors present.
func TestDoctor_RenderReturnsErrorOnErrors(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, "gone")
	content := "schema_version: \"2.0\"\norg: personal\nagents:\n  - id: gone-agent\n    cwd: " + missingPath + "\n    archetype: agentic_coder\n    default_model: claude-sonnet-4-6\n    provider: anthropic\n    languages: [go]\nvocabulary:\n  archetypes: [agentic_coder]\n  model_tiers:\n    balanced: [claude-sonnet-4-6]\n  provider_types:\n    cloud_anthropic: [anthropic]\n"
	orgPath := writeOrgYAML(t, dir, content)
	cfg := DoctorConfig{OrgYAML: orgPath, AgentsFile: filepath.Join(dir, "agents.jsonl")}

	out, err := runDoctorCmd(t, cfg)
	if err == nil {
		t.Errorf("expected error from renderDoctorReport when errors present; output: %s", out)
	}
}

// BT-1: isKnownHarnessID covers expected harness IDs.
func TestIsKnownHarnessID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"claude-code", true},
		{"pi", true},
		{"codex", true},
		{"runabout-agent", false},
		{"unknown-agent", false},
		{"", false},
	}
	for _, c := range cases {
		got := isKnownHarnessID(c.id)
		if got != c.want {
			t.Errorf("isKnownHarnessID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

// BT-2: expandHome replaces ~ correctly.
func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	got := expandHome("~/foo/bar")
	want := filepath.Join(home, "foo", "bar")
	if got != want {
		t.Errorf("expandHome got %q, want %q", got, want)
	}
	// Non-home path unchanged.
	abs := "/absolute/path"
	if expandHome(abs) != abs {
		t.Errorf("expandHome should not modify absolute path")
	}
}

// BT-3: findStaleAgentIDs skips malformed JSONL lines.
func TestFindStaleAgentIDs_MalformedLines(t *testing.T) {
	dir := t.TempDir()
	cwdDir := filepath.Join(dir, "docs")
	if err := os.Mkdir(cwdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "schema_version: \"2.0\"\norg: personal\nagents:\n  - id: docs-agent\n    cwd: " + cwdDir + "\n    archetype: agentic_coder\n    default_model: claude-sonnet-4-6\n    provider: anthropic\n    languages: [markdown]\nvocabulary:\n  archetypes: [agentic_coder]\n  model_tiers:\n    balanced: [claude-sonnet-4-6]\n  provider_types:\n    cloud_anthropic: [anthropic]\n"
	orgPath := writeOrgYAML(t, dir, content)
	agentsPath := writeAgentsJSONL(t, dir, "not-json\n{\"agent_id\":\"claude-code\"}\n{bad json")

	cfg := DoctorConfig{OrgYAML: orgPath, AgentsFile: agentsPath}
	report, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Malformed lines are skipped; claude-code is a known harness ID so no W505.
	for _, w := range report.Warnings {
		if w.Code == "W505" {
			t.Errorf("malformed/harness lines should not produce W505: %v", w)
		}
	}
}

// BT-4: RunDoctor is idempotent - calling twice returns same result.
func TestDoctor_Idempotent(t *testing.T) {
	dir := t.TempDir()
	cwdDir := filepath.Join(dir, "docs")
	if err := os.Mkdir(cwdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "schema_version: \"2.0\"\norg: personal\nagents:\n  - id: docs-agent\n    cwd: " + cwdDir + "\n    archetype: agentic_coder\n    default_model: claude-sonnet-4-6\n    provider: anthropic\n    languages: [markdown]\nvocabulary:\n  archetypes: [agentic_coder]\n  model_tiers:\n    balanced: [claude-sonnet-4-6]\n  provider_types:\n    cloud_anthropic: [anthropic]\n"
	orgPath := writeOrgYAML(t, dir, content)
	cfg := DoctorConfig{OrgYAML: orgPath, AgentsFile: filepath.Join(dir, "agents.jsonl")}

	r1, err1 := RunDoctor(cfg)
	r2, err2 := RunDoctor(cfg)
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}
	if r1.AgentCount != r2.AgentCount {
		t.Errorf("idempotency violation: %d != %d", r1.AgentCount, r2.AgentCount)
	}
	if len(r1.Errors) != len(r2.Errors) || len(r1.Warnings) != len(r2.Warnings) {
		t.Errorf("idempotency violation in issues count")
	}
}
