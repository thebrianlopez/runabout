package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// CT-1: artifact without consensus_gates -> nil (gate N/A).
func TestConsensusGateCheck_NoGate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.md")
	content := "---\nstatus: Approved\n---\n\n# Test Artifact\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	checker := &ConsensusGateCheck{EventBusDir: t.TempDir()}
	if err := checker.Check(path); err != nil {
		t.Errorf("CT-1: want nil (gate N/A), got %v", err)
	}
}

// CT-2: consensus_gates present + approved gate result with matching hash -> nil (pass).
func TestConsensusGateCheck_ApprovedMatchingHash(t *testing.T) {
	dir := t.TempDir()
	busDir := t.TempDir()

	content := "---\nconsensus_gates:\n  promotion:\n    required_agents: [docs-agent]\n---\n\n# Test FDD\n"
	path := filepath.Join(dir, "artifact.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := contentSHA256(data)

	ev := consensusGateEvent{
		Type:         "consensus_gate_result",
		ArtifactPath: path,
		ArtifactHash: hash,
		Result:       "approved",
		RoundID:      "round-1",
	}
	writeEventBus(t, busDir, ev)

	checker := &ConsensusGateCheck{EventBusDir: busDir}
	if err := checker.Check(path); err != nil {
		t.Errorf("CT-2: want nil (approved + matching hash), got %v", err)
	}
}

// CT-3: consensus_gates present + no gate result -> ErrConsensusMissing.
func TestConsensusGateCheck_NoResult(t *testing.T) {
	dir := t.TempDir()

	content := "---\nconsensus_gates:\n  promotion:\n    required_agents: [docs-agent]\n---\n\n# Test FDD\n"
	path := filepath.Join(dir, "artifact.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	checker := &ConsensusGateCheck{EventBusDir: t.TempDir()} // empty bus
	err := checker.Check(path)
	if !errors.Is(err, ErrConsensusMissing) {
		t.Errorf("CT-3: want ErrConsensusMissing, got %v", err)
	}
}

// CT-4: consensus_gates present + gate result with stale hash -> ErrConsensusStale.
func TestConsensusGateCheck_StaleHash(t *testing.T) {
	dir := t.TempDir()
	busDir := t.TempDir()

	content := "---\nconsensus_gates:\n  promotion:\n    required_agents: [docs-agent]\n---\n\n# Test FDD\n"
	path := filepath.Join(dir, "artifact.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	ev := consensusGateEvent{
		Type:         "consensus_gate_result",
		ArtifactPath: path,
		ArtifactHash: "deadbeefdeadbeefdeadbeef00000000", // wrong hash
		Result:       "approved",
		RoundID:      "round-old",
	}
	writeEventBus(t, busDir, ev)

	checker := &ConsensusGateCheck{EventBusDir: busDir}
	err := checker.Check(path)
	if !errors.Is(err, ErrConsensusStale) {
		t.Errorf("CT-4: want ErrConsensusStale, got %v", err)
	}
}

// CT-5: consensus_gates present + rejected gate result -> ErrConsensusMissing.
func TestConsensusGateCheck_Rejected(t *testing.T) {
	dir := t.TempDir()
	busDir := t.TempDir()

	content := "---\nconsensus_gates:\n  promotion:\n    required_agents: [docs-agent]\n---\n\n# Test FDD\n"
	path := filepath.Join(dir, "artifact.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := contentSHA256(data)

	ev := consensusGateEvent{
		Type:         "consensus_gate_result",
		ArtifactPath: path,
		ArtifactHash: hash,
		Result:       "rejected",
		RoundID:      "round-1",
	}
	writeEventBus(t, busDir, ev)

	checker := &ConsensusGateCheck{EventBusDir: busDir}
	err = checker.Check(path)
	if !errors.Is(err, ErrConsensusMissing) {
		t.Errorf("CT-5: want ErrConsensusMissing (rejected result), got %v", err)
	}
}

// CT-6: malformed consensus_gates YAML -> ErrFrontmatterInvalid.
func TestConsensusGateCheck_MalformedYAML(t *testing.T) {
	dir := t.TempDir()

	// Unclosed YAML sequence - parse error guaranteed.
	content := "---\nconsensus_gates:\n  promotion:\n    agents: [unclosed\n---\n\n# Test\n"
	path := filepath.Join(dir, "artifact.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	checker := &ConsensusGateCheck{EventBusDir: t.TempDir()}
	err := checker.Check(path)
	if !errors.Is(err, ErrFrontmatterInvalid) {
		t.Errorf("CT-6: want ErrFrontmatterInvalid, got %v", err)
	}
}

// CT-7: runConsensusGateChecks returns false when a fixture artifact declares
// consensus_gates but no gate result exists in the event bus.
func TestRunConsensusGateChecks_MissingGate(t *testing.T) {
	fixturesDir := t.TempDir()
	busDir := t.TempDir() // empty - no events

	fixtureDir := filepath.Join(fixturesDir, "ct7_fixture")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nconsensus_gates:\n  promotion:\n    required_agents: [docs-agent]\n---\n\n# Fixture FDD\n"
	if err := os.WriteFile(filepath.Join(fixtureDir, "FIXTURE_FDD.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if runConsensusGateChecks(fixturesDir, "", busDir) {
		t.Error("CT-7: want false (gate fail) when fixture declares consensus_gates but bus has no result")
	}
}

// RG-1: artifacts without consensus_gates must not erroneously fail.
func TestRunConsensusGateChecks_NoGateArtifacts(t *testing.T) {
	fixturesDir := t.TempDir()
	busDir := t.TempDir()

	fixtureDir := filepath.Join(fixturesDir, "rg1_fixture")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nstatus: Approved\ntype: fdd\n---\n\n# Normal FDD - no consensus_gates\n"
	if err := os.WriteFile(filepath.Join(fixtureDir, "FIXTURE_FDD.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if !runConsensusGateChecks(fixturesDir, "", busDir) {
		t.Error("RG-1: artifacts without consensus_gates must not fail consensus gate check")
	}
}

// BT-2: error message includes advisory "castex consensus submit" when gate is missing.
func TestConsensusGateCheck_MissingAdvisory(t *testing.T) {
	dir := t.TempDir()

	content := "---\nconsensus_gates:\n  promotion:\n    required_agents: [docs-agent]\n---\n\n# Test\n"
	path := filepath.Join(dir, "artifact.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	checker := &ConsensusGateCheck{EventBusDir: t.TempDir()}
	err := checker.Check(path)
	if err == nil {
		t.Fatal("BT-2: expected error, got nil")
	}
	if msg := err.Error(); !contains(msg, "castex consensus submit") {
		t.Errorf("BT-2: error message must include advisory 'castex consensus submit', got: %v", err)
	}
}

// writeEventBus writes a single consensusGateEvent as JSONL to busDir/events.jsonl.
func writeEventBus(t *testing.T, busDir string, ev consensusGateEvent) {
	t.Helper()
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(busDir, "events.jsonl"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// contains reports whether substr appears in s.
// indexOf is shared with resolve_test.go in this package.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || indexOf(s, substr) >= 0)
}
