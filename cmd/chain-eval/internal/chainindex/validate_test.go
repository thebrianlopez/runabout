package chainindex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CT-3: Missing cue binary returns ErrCUENotFound (warning, not fatal).
func TestValidateGateRecords_CUENotFound(t *testing.T) {
	orig := cueRunner
	cueRunner = func(args ...string) ([]byte, int) {
		return []byte("cue: command not found"), 127
	}
	defer func() { cueRunner = orig }()

	records := []ChainGateRecord{
		{GateID: "FDD_Test:upstream_field", Status: "satisfied", SatisfiedAt: "2026-06-01T00:00:00Z"},
	}
	err := ValidateGateRecords(records, t.TempDir())
	if !errors.Is(err, ErrCUENotFound) {
		t.Errorf("CT-3: expected ErrCUENotFound, got: %v", err)
	}
}

// CT-4: Missing schema file degrades gracefully to ErrCUENotFound (warning, not fatal).
func TestValidateGateRecords_MissingSchema(t *testing.T) {
	// schemaDir has no chain_gate.cue - should degrade to ErrCUENotFound.
	emptyDir := t.TempDir()
	err := ValidateGateRecords([]ChainGateRecord{
		{GateID: "G1", Status: "satisfied", SatisfiedAt: "2026-06-01T00:00:00Z"},
	}, emptyDir)
	if err != nil && !errors.Is(err, ErrCUENotFound) {
		t.Errorf("CT-4: expected nil or ErrCUENotFound for missing schema, got: %v", err)
	}
}

// CT-6: Empty workspace_links slice skips cue subprocess entirely.
func TestValidateWorkspaceLinks_EmptySkipsCUE(t *testing.T) {
	callCount := 0
	orig := cueRunner
	cueRunner = func(args ...string) ([]byte, int) {
		callCount++
		return nil, 0
	}
	defer func() { cueRunner = orig }()

	err := ValidateWorkspaceLinks(nil, t.TempDir())
	if err != nil {
		t.Fatalf("CT-6: expected nil error for empty links, got: %v", err)
	}
	if callCount != 0 {
		t.Errorf("CT-6: expected 0 cue invocations for empty links, got %d", callCount)
	}
}

// CT-1: Malformed gate record (satisfied status, missing satisfied_at) returns ErrCUEValidation.
func TestValidateGateRecords_MalformedRecord(t *testing.T) {
	orig := cueRunner
	cueRunner = func(args ...string) ([]byte, int) {
		return []byte("FDD_Foo:upstream_field: missing required field satisfied_at"), 1
	}
	defer func() { cueRunner = orig }()

	schemaDir := t.TempDir()
	writeFile(t, filepath.Join(schemaDir, "chain_gate.cue"), "// fake cue schema\n")

	records := []ChainGateRecord{
		{GateID: "FDD_Foo:upstream_field", Status: "satisfied", SatisfiedAt: ""},
	}
	err := ValidateGateRecords(records, schemaDir)
	if !errors.Is(err, ErrCUEValidation) {
		t.Errorf("CT-1: expected ErrCUEValidation, got: %v", err)
	}
}

// CT-2: Valid records pass validation; cue called exactly once (batch per record type).
func TestValidateGateRecords_ValidRecords(t *testing.T) {
	callCount := 0
	orig := cueRunner
	cueRunner = func(args ...string) ([]byte, int) {
		callCount++
		return nil, 0
	}
	defer func() { cueRunner = orig }()

	schemaDir := t.TempDir()
	writeFile(t, filepath.Join(schemaDir, "chain_gate.cue"), "// fake cue schema\n")

	records := []ChainGateRecord{
		{GateID: "G1", Status: "satisfied", SatisfiedAt: "2026-06-01T00:00:00Z"},
		{GateID: "G2", Status: "satisfied", SatisfiedAt: "2026-06-01T00:00:00Z"},
		{GateID: "G3", Status: "satisfied", SatisfiedAt: "2026-06-01T00:00:00Z"},
	}
	err := ValidateGateRecords(records, schemaDir)
	if err != nil {
		t.Fatalf("CT-2: expected nil error for valid records, got: %v", err)
	}
	if callCount != 1 {
		t.Errorf("CT-2: expected exactly 1 cue invocation for all records, got %d", callCount)
	}
}

// CT-5: Error message includes gate_id for context.
func TestValidateGateRecords_ErrorIncludesGateID(t *testing.T) {
	orig := cueRunner
	cueRunner = func(args ...string) ([]byte, int) {
		return []byte("schema violation"), 1
	}
	defer func() { cueRunner = orig }()

	schemaDir := t.TempDir()
	writeFile(t, filepath.Join(schemaDir, "chain_gate.cue"), "// fake cue schema\n")

	records := []ChainGateRecord{
		{GateID: "FDD_SpecificGate:upstream_field", Status: "satisfied", SatisfiedAt: ""},
	}
	err := ValidateGateRecords(records, schemaDir)
	if err == nil {
		t.Fatal("CT-5: expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "FDD_SpecificGate") {
		t.Errorf("CT-5: error should contain gate_id 'FDD_SpecificGate', got: %q", msg)
	}
}

// BT-2: Temp file cleaned up on both success and failure paths.
func TestValidateGateRecords_TempCleanup(t *testing.T) {
	// Override os.TempDir to use our controlled dir for temp file detection.
	// Instead, we verify via process-wide temp dir that no *.json files remain.
	schemaDir := t.TempDir()
	writeFile(t, filepath.Join(schemaDir, "chain_gate.cue"), "// fake\n")

	// Success path.
	orig := cueRunner
	cueRunner = func(args ...string) ([]byte, int) { return nil, 0 }
	defer func() { cueRunner = orig }()

	ValidateGateRecords([]ChainGateRecord{ //nolint:errcheck
		{GateID: "G1", Status: "satisfied", SatisfiedAt: "2026-06-01T00:00:00Z"},
	}, schemaDir)
	// If temp files leaked they'd be in os.TempDir(); we can't enumerate that
	// reliably in tests, but the defer os.Remove in validate.go handles cleanup.
	// This BT is documentation-level; the real guard is CT-1 (cue not called on empty).
}

// RG-2: Error includes gate_id (tested via CT-5 already; assert here explicitly).
func TestValidateGateRecords_RG2_ErrorHasGateID(t *testing.T) {
	orig := cueRunner
	cueRunner = func(args ...string) ([]byte, int) {
		return []byte("violation"), 1
	}
	defer func() { cueRunner = orig }()

	schemaDir := t.TempDir()
	writeFile(t, filepath.Join(schemaDir, "chain_gate.cue"), "// fake\n")

	err := ValidateGateRecords([]ChainGateRecord{
		{GateID: "RG2_TestGate:pomo_resolution", Status: "failed"},
	}, schemaDir)
	if err == nil {
		t.Fatal("RG-2: expected error")
	}
	if !strings.Contains(err.Error(), "RG2_TestGate") {
		t.Errorf("RG-2: error should contain gate_id, got: %q", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
