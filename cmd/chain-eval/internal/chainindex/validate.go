package chainindex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrCUENotFound is returned when the cue binary is absent from PATH or the
// schema file is missing. Callers treat this as warning-class: write index anyway.
var ErrCUENotFound = errors.New("cue not in PATH")

// ErrCUEValidation is returned when cue vet reports a schema violation.
// Callers treat this as fatal: do not write the index (exit 2).
var ErrCUEValidation = errors.New("CUE schema violation")

// cueRunner is injectable for tests to avoid spawning a real cue subprocess.
// Returns (output, exitCode).
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
// Returns ErrCUENotFound when cue is absent or schema is missing (warning-class).
// Returns ErrCUEValidation on schema violation (includes gate_id in message).
func ValidateGateRecords(records []ChainGateRecord, schemaDir string) error {
	if len(records) == 0 {
		return nil
	}
	schemaPath := filepath.Join(schemaDir, "chain_gate.cue")
	return validateRecords(records, schemaPath, func(r []ChainGateRecord) (string, error) {
		if len(r) > 0 {
			return r[0].GateID, nil
		}
		return "", nil
	})
}

// ValidateWorkspaceLinks serializes links to a temp JSON file and invokes
// cue vet against {schemaDir}/workspace_link.cue. Same error semantics.
func ValidateWorkspaceLinks(links []WorkspaceChainLink, schemaDir string) error {
	if len(links) == 0 {
		return nil
	}
	schemaPath := filepath.Join(schemaDir, "workspace_link.cue")
	return validateRecords(links, schemaPath, func(r []WorkspaceChainLink) (string, error) {
		return "", nil
	})
}

func validateRecords[T any](records T, schemaPath string, firstID func(T) (string, error)) error {
	// Check schema exists.
	if _, err := os.Stat(schemaPath); os.IsNotExist(err) {
		return ErrCUENotFound
	}

	// Serialize to temp file.
	tmp, err := os.CreateTemp("", "chain-validate-*.json")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck

	data, err := json.Marshal(records)
	if err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("marshal: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("write temp: %w", err)
	}
	tmp.Close() //nolint:errcheck

	// Invoke cue vet once for all records (batch).
	out, code := cueRunner("vet", schemaPath, tmpName)
	if code == 127 || (code != 0 && isCUENotFound(string(out))) {
		return ErrCUENotFound
	}
	if code != 0 {
		id, _ := firstID(records)
		if id != "" {
			return fmt.Errorf("%w: %s: %s", ErrCUEValidation, id, string(out))
		}
		return fmt.Errorf("%w: %s", ErrCUEValidation, string(out))
	}
	return nil
}

func isCUENotFound(output string) bool {
	return len(output) > 0 && (output == "cue: command not found" ||
		output == "cannot find cue" ||
		len(output) > 10 && output[len(output)-10:] == "not found\n")
}
