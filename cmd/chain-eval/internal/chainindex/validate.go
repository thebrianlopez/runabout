package chainindex

import "errors"

// ErrCUENotFound is returned when the cue binary is absent from PATH or the
// schema file is missing. Callers treat this as warning-class: write index anyway.
var ErrCUENotFound = errors.New("cue not in PATH")

// ErrCUEValidation is returned when cue vet reports a schema violation.
// Callers treat this as fatal: do not write the index (exit 2).
var ErrCUEValidation = errors.New("CUE schema violation")

// cueRunner is injectable for tests to avoid spawning a real cue subprocess.
var cueRunner func(args ...string) ([]byte, int) = defaultCueRunner

func defaultCueRunner(args ...string) ([]byte, int) {
	cmd := newCmd("cue", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, 1
	}
	return out, 0
}

// ValidateGateRecords serializes records to a temp JSON file and invokes
// cue vet against {schemaDir}/chain_gate.cue. Returns nil on success.
// Returns ErrCUENotFound when cue is absent or schema is missing.
// Returns ErrCUEValidation on schema violation (includes gate_id in message).
func ValidateGateRecords(records []ChainGateRecord, schemaDir string) error {
	panic("not implemented")
}

// ValidateWorkspaceLinks serializes links to a temp JSON file and invokes
// cue vet against {schemaDir}/workspace_link.cue. Same error semantics.
func ValidateWorkspaceLinks(links []WorkspaceChainLink, schemaDir string) error {
	panic("not implemented")
}
