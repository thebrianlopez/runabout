package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// CT-M1: first run seeds state file, no events emitted.
func TestMutation_CT1_FirstRunSeedsState(t *testing.T) {
	dir := t.TempDir()
	orgYAML := writeOrgYAML(t, dir, validOrgYAMLOneAgent)
	stateFile := filepath.Join(dir, "registry-state.json")

	cfg := DoctorConfig{
		OrgYAML:   orgYAML,
		StateFile: stateFile,
	}
	report, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	if len(report.MutationEvents) != 0 {
		t.Errorf("first run: expected no mutation events, got %d", len(report.MutationEvents))
	}
	if report.NewState == nil {
		t.Fatal("first run: expected NewState to be set")
	}
	if report.NewState.SHA256 == "" {
		t.Error("first run: NewState.SHA256 is empty")
	}
	if len(report.NewState.AgentIDs) == 0 {
		t.Error("first run: NewState.AgentIDs is empty")
	}
}

// CT-M2: unchanged org.yaml produces no events and nil NewState.
func TestMutation_CT2_UnchangedNoEvents(t *testing.T) {
	dir := t.TempDir()
	orgYAML := writeOrgYAML(t, dir, validOrgYAMLOneAgent)
	stateFile := filepath.Join(dir, "registry-state.json")

	cfg := DoctorConfig{OrgYAML: orgYAML, StateFile: stateFile}
	r1, _ := RunDoctor(cfg)
	if r1.NewState != nil {
		_ = saveRegistryState(stateFile, *r1.NewState)
	}

	r2, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("second RunDoctor: %v", err)
	}
	if len(r2.MutationEvents) != 0 {
		t.Errorf("unchanged: expected no events, got %d", len(r2.MutationEvents))
	}
	if r2.NewState != nil {
		t.Error("unchanged: expected NewState nil")
	}
}

// CT-M3: adding an agent emits registry_agent_added.
func TestMutation_CT3_AgentAdded(t *testing.T) {
	dir := t.TempDir()
	orgYAML := writeOrgYAML(t, dir, validOrgYAMLOneAgent)
	stateFile := filepath.Join(dir, "registry-state.json")

	cfg := DoctorConfig{OrgYAML: orgYAML, StateFile: stateFile}
	r1, _ := RunDoctor(cfg)
	if r1.NewState != nil {
		_ = saveRegistryState(stateFile, *r1.NewState)
	}

	// Add a second agent.
	_ = os.WriteFile(orgYAML, []byte(validOrgYAMLTwoAgents), 0o644)
	r2, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("second RunDoctor: %v", err)
	}
	if len(r2.MutationEvents) == 0 {
		t.Fatal("expected mutation events after agent add, got none")
	}
	found := false
	for _, ev := range r2.MutationEvents {
		if ev.EventType == "registry_agent_added" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected registry_agent_added, got %v", r2.MutationEvents)
	}
}

// CT-M4: removing an agent emits registry_agent_removed.
func TestMutation_CT4_AgentRemoved(t *testing.T) {
	dir := t.TempDir()
	orgYAML := writeOrgYAML(t, dir, validOrgYAMLTwoAgents)
	stateFile := filepath.Join(dir, "registry-state.json")

	cfg := DoctorConfig{OrgYAML: orgYAML, StateFile: stateFile}
	r1, _ := RunDoctor(cfg)
	if r1.NewState != nil {
		_ = saveRegistryState(stateFile, *r1.NewState)
	}

	// Remove second agent.
	_ = os.WriteFile(orgYAML, []byte(validOrgYAMLOneAgent), 0o644)
	r2, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("second RunDoctor: %v", err)
	}
	found := false
	for _, ev := range r2.MutationEvents {
		if ev.EventType == "registry_agent_removed" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected registry_agent_removed, got %v", r2.MutationEvents)
	}
}

// CT-M5: changed file but same agents emits registry_agent_updated.
func TestMutation_CT5_AgentUpdated(t *testing.T) {
	dir := t.TempDir()
	orgYAML := writeOrgYAML(t, dir, validOrgYAMLOneAgent)
	stateFile := filepath.Join(dir, "registry-state.json")

	cfg := DoctorConfig{OrgYAML: orgYAML, StateFile: stateFile}
	r1, _ := RunDoctor(cfg)
	if r1.NewState != nil {
		_ = saveRegistryState(stateFile, *r1.NewState)
	}

	// Change a field but keep same agent ID.
	updated := `schema_version: "2.0"
org: personal
agents:
  - id: test-agent
    cwd: ` + dir + `
    archetype: orchestrator
    default_model: claude-opus-4-7
    provider: anthropic
`
	_ = os.WriteFile(orgYAML, []byte(updated), 0o644)
	r2, err := RunDoctor(cfg)
	if err != nil {
		t.Fatalf("second RunDoctor: %v", err)
	}
	found := false
	for _, ev := range r2.MutationEvents {
		if ev.EventType == "registry_agent_updated" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected registry_agent_updated, got %v", r2.MutationEvents)
	}
}

// CT-M6: writeMutationEvents creates the daily JSONL file.
func TestMutation_CT6_WriteMutationEvents(t *testing.T) {
	dir := t.TempDir()
	events := []RegistryMutationEvent{
		{EventType: "registry_agent_added", AgentID: "foo-agent", Archetype: "agentic_coder", SHA256: "abc"},
	}
	if err := writeMutationEvents(dir, events); err != nil {
		t.Fatalf("writeMutationEvents: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) == 0 {
		t.Fatal("no JSONL file created")
	}
	b, _ := os.ReadFile(files[0])
	var row map[string]interface{}
	if err := json.Unmarshal(b[:len(b)-1], &row); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if row["event_type"] != "registry_agent_added" {
		t.Errorf("event_type: got %v", row["event_type"])
	}
	if row["layer"] != "orchestration" {
		t.Errorf("layer: got %v", row["layer"])
	}
}

// BT-M1: diffRegistryAgents correctly classifies add/remove/update.
func TestDiffRegistryAgents_BT1(t *testing.T) {
	prev := []string{"a", "b"}
	curr := []string{"b", "c"}
	archetypes := map[string]string{"b": "agentic_coder", "c": "orchestrator"}
	evs := diffRegistryAgents(prev, curr, "sha", archetypes)

	gotAdded, gotRemoved := false, false
	for _, ev := range evs {
		switch ev.EventType {
		case "registry_agent_added":
			if ev.AgentID == "c" {
				gotAdded = true
			}
		case "registry_agent_removed":
			if ev.AgentID == "a" {
				gotRemoved = true
			}
		}
	}
	if !gotAdded {
		t.Error("expected registry_agent_added for c")
	}
	if !gotRemoved {
		t.Error("expected registry_agent_removed for a")
	}
}

// BT-M2: loadRegistryState returns zero value when file absent.
func TestLoadRegistryState_BT2_Absent(t *testing.T) {
	s, err := loadRegistryState("/tmp/no-such-state-file-castex.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.SHA256 != "" || len(s.AgentIDs) != 0 {
		t.Error("expected zero value for absent state file")
	}
}

// BT-M3: saveRegistryState round-trips correctly.
func TestSaveRegistryState_BT3_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "state.json")
	state := RegistryState{SHA256: "abc123", AgentIDs: []string{"a", "b"}}
	if err := saveRegistryState(path, state); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := loadRegistryState(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.SHA256 != state.SHA256 {
		t.Errorf("SHA256: got %s want %s", loaded.SHA256, state.SHA256)
	}
	if len(loaded.AgentIDs) != len(state.AgentIDs) {
		t.Errorf("AgentIDs len: got %d want %d", len(loaded.AgentIDs), len(state.AgentIDs))
	}
}

const validOrgYAMLOneAgent = `schema_version: "2.0"
org: personal
agents:
  - id: test-agent
    cwd: /tmp
    archetype: agentic_coder
    default_model: claude-sonnet-4-6
    provider: anthropic
`

const validOrgYAMLTwoAgents = `schema_version: "2.0"
org: personal
agents:
  - id: test-agent
    cwd: /tmp
    archetype: agentic_coder
    default_model: claude-sonnet-4-6
    provider: anthropic
  - id: second-agent
    cwd: /tmp
    archetype: orchestrator
    default_model: claude-sonnet-4-6
    provider: anthropic
`
