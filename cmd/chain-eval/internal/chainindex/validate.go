package chainindex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	return validateRecords(records, schemaPath, "#ChainGateRecord", func(r []ChainGateRecord) (string, error) {
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
	return validateRecords(links, schemaPath, "#WorkspaceChainLink", func(r []WorkspaceChainLink) (string, error) {
		return "", nil
	})
}

func validateRecords[T any](records T, schemaPath string, defName string, firstID func(T) (string, error)) error {
	// Check schema exists.
	if _, err := os.Stat(schemaPath); os.IsNotExist(err) {
		return ErrCUENotFound
	}

	// Serialize to temp file. Records are wrapped in a named field so the data
	// unifies with a list constraint: a bare JSON array cannot unify with a
	// struct definition, which is why validation could only ever pass while the
	// record set was empty (F6 / EPIC-266).
	tmpDir, err := os.MkdirTemp("", "chain-validate-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck

	data, err := json.Marshal(map[string]T{"records": records})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	dataPath := filepath.Join(tmpDir, "records.json")
	if err := os.WriteFile(dataPath, data, 0o600); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}

	// Constraint file binding the record list to the schema definition. Without
	// it, cue vet unifies the data against the schema package's top level and
	// checks nothing.
	constraintPath := filepath.Join(tmpDir, "records_constraint.cue")
	constraint := fmt.Sprintf("package %s\n\nrecords: [...%s]\n", schemaPackage(schemaPath), defName)
	if err := os.WriteFile(constraintPath, []byte(constraint), 0o600); err != nil {
		return fmt.Errorf("write constraint: %w", err)
	}

	// Invoke cue vet once for all records (batch). Sibling schema files from the
	// same package are included so cross-file references (e.g. #Timestamp)
	// resolve.
	args := append([]string{"vet"}, schemaPackageFiles(schemaPath)...)
	args = append(args, constraintPath, dataPath)
	out, code := cueRunner(args...)
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

// schemaPackage returns the CUE package clause of the schema file, defaulting
// to "schemas" when it cannot be read.
func schemaPackage(schemaPath string) string {
	content, err := os.ReadFile(schemaPath)
	if err != nil {
		return "schemas"
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if pkg, ok := strings.CutPrefix(line, "package "); ok {
			if pkg = strings.TrimSpace(pkg); pkg != "" {
				return pkg
			}
		}
	}
	return "schemas"
}

// schemaPackageFiles returns the schema file plus its same-package siblings in
// the same directory, sorted for deterministic invocation.
func schemaPackageFiles(schemaPath string) []string {
	dir := filepath.Dir(schemaPath)
	pkg := schemaPackage(schemaPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{schemaPath}
	}
	files := []string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".cue") {
			continue
		}
		candidate := filepath.Join(dir, e.Name())
		if candidate != schemaPath && schemaPackage(candidate) != pkg {
			continue
		}
		files = append(files, candidate)
	}
	if len(files) == 0 {
		return []string{schemaPath}
	}
	sort.Strings(files)
	return files
}

func isCUENotFound(output string) bool {
	return len(output) > 0 && (output == "cue: command not found" ||
		output == "cannot find cue" ||
		len(output) > 10 && output[len(output)-10:] == "not found\n")
}
