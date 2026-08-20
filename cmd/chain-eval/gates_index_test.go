package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// F6 (EPIC-266) integration coverage: gate record production over a real
// fixture docs tree, plus CUE validation of every emitted record.

type gateRecordJSON struct {
	GateID           string `json:"gate_id"`
	GateType         string `json:"gate_type"`
	ArtifactPath     string `json:"artifact_path"`
	UpstreamArtifact string `json:"upstream_artifact,omitempty"`
	Status           string `json:"status"`
	SatisfiedAt      string `json:"satisfied_at,omitempty"`
}

type chainEntryJSON struct {
	PRD *struct {
		Path   string `json:"path"`
		Status string `json:"status"`
	} `json:"prd"`
	POMOs []struct {
		Path string `json:"path"`
	} `json:"pomos"`
	GateRecords []gateRecordJSON `json:"gate_records"`
}

type indexJSON struct {
	GateRecords []gateRecordJSON          `json:"gate_records"`
	Chains      map[string]chainEntryJSON `json:"chains"`
}

// writeFixtureCorpus creates a docs tree with one PRD-rooted chain, a linked
// TDD/Epic/POMO, and an unlinked post-threshold epic.
func writeFixtureCorpus(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	fdd := "PERSONAL_20260601T000000Z_Alpha_FDD.md"
	prd := "PERSONAL_20260601T000000Z_Alpha_PRD.md"

	files := map[string]string{
		"prds/" + prd: "## Status and Metadata\n| **Status** | Approved |\n| **Created** | `20260601T000000Z` |\n",
		"design/" + fdd: "## Status and Metadata\n| **Status** | Approved |\n| **Created** | `20260601T000000Z` |\n" +
			"| **Source PRD** | `" + prd + "` |\n",
		"design/PERSONAL_20260602T000000Z_Alpha_TDD.md": "## Status and Metadata\n| **Status** | Approved |\n| **Created** | `20260602T000000Z` |\n" +
			"| **Source FDD** | `" + fdd + "` |\n",
		"epics/PERSONAL_20260603T000000Z_EPIC-001.md": "## Status and Metadata\n| **Status** | Ready |\n| **Created** | `20260603T000000Z` |\n" +
			"| **Source FDD** | `" + fdd + "` |\n",
		"pomo/PERSONAL_20260601T000000Z_POMO_alpha.md": "## Status and Metadata\n| **Status** | Open |\n| **Created** | `20260601T000000Z` |\n" +
			"| **Source FDD** | `" + fdd + "` |\n",
		"epics/PERSONAL_20260604T000000Z_EPIC-002.md": "## Status and Metadata\n| **Status** | Ready |\n| **Created** | `20260604T000000Z` |\n",
	}
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func buildFixtureIndex(t *testing.T) indexJSON {
	t.Helper()
	root := writeFixtureCorpus(t)
	outputPath := filepath.Join(t.TempDir(), "idx.json")
	if code := runIndex(nil, indexRunConfig{docsRoot: root, output: outputPath, quiet: true}); code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var idx indexJSON
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	return idx
}

// CT-3 / RG-1: a fresh index over a real corpus produces gate records, with at
// least one satisfied and one unsatisfied outcome. This is the assertion whose
// absence let the regression ship.
func TestRunIndex_GateRecordsNonEmpty(t *testing.T) {
	idx := buildFixtureIndex(t)

	if len(idx.GateRecords) < 2 {
		t.Fatalf("CT-3: expected >= 2 gate records, got %d", len(idx.GateRecords))
	}
	var satisfied, unsatisfied int
	for _, r := range idx.GateRecords {
		switch r.Status {
		case "satisfied":
			satisfied++
			if r.SatisfiedAt == "" {
				t.Errorf("CT-3: satisfied record %q has no satisfied_at", r.GateID)
			}
			if r.UpstreamArtifact == "" {
				t.Errorf("CT-3: satisfied record %q has no upstream_artifact", r.GateID)
			}
		case "unsatisfied":
			unsatisfied++
		default:
			t.Errorf("CT-9: unexpected status %q on %q", r.Status, r.GateID)
		}
	}
	if satisfied == 0 || unsatisfied == 0 {
		t.Errorf("CT-3: expected both outcomes, got %d satisfied / %d unsatisfied", satisfied, unsatisfied)
	}
}

// CT-11 / CT-13 / RG-3: chain linkage is populated for PRD and POMOs.
func TestRunIndex_PRDAndPOMOLinkage(t *testing.T) {
	idx := buildFixtureIndex(t)

	entry, ok := idx.Chains["alpha"]
	if !ok {
		t.Fatalf("expected chain key 'alpha', got %v", chainKeyList(idx))
	}
	if entry.PRD == nil {
		t.Fatal("CT-13: chains[alpha].prd is nil")
	}
	if entry.PRD.Status != "Approved" {
		t.Errorf("CT-13: prd.status = %q, want Approved", entry.PRD.Status)
	}
	if len(entry.POMOs) != 1 {
		t.Fatalf("CT-11: expected 1 linked POMO, got %d", len(entry.POMOs))
	}
	if len(entry.GateRecords) == 0 {
		t.Error("expected chain-scoped gate records")
	}
}

// CT-4: every emitted record validates against #ChainGateRecord. Skips when the
// core schema dir or the cue binary is unavailable.
func TestRunIndex_GateRecordsPassCUE(t *testing.T) {
	schemaDir := findSchemaDir()
	if schemaDir == "" {
		t.Skip("CT-4: core CUE schema dir not found - set CHAIN_SCHEMA_DIR or WS_ORG_CORE")
	}
	if _, err := exec.LookPath("cue"); err != nil {
		t.Skip("CT-4: cue not in PATH")
	}

	idx := buildFixtureIndex(t)
	if len(idx.GateRecords) == 0 {
		t.Fatal("CT-4: no gate records to validate")
	}

	gateSchema := filepath.Join(schemaDir, "chain_gate.cue")
	tsSchema := filepath.Join(schemaDir, "workspace.cue")
	dir := t.TempDir()
	for i, r := range idx.GateRecords {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		recPath := filepath.Join(dir, "record.json")
		if err := os.WriteFile(recPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("cue", "vet", "-d", "#ChainGateRecord", gateSchema, tsSchema, recPath).CombinedOutput()
		if err != nil {
			t.Errorf("CT-4: record %d (%s) failed cue vet: %v\n%s", i, r.GateID, err, out)
		}
	}
}

// CT-5 at integration level: every emitted gate_id is artifact-derived and
// schema-conformant.
func TestRunIndex_GateIDsAreArtifactDerived(t *testing.T) {
	idx := buildFixtureIndex(t)
	re := regexp.MustCompile(`^[a-z0-9_-]+:[a-z_]+$`)
	for _, r := range idx.GateRecords {
		if !re.MatchString(r.GateID) {
			t.Errorf("CT-5: gate_id %q violates schema regex", r.GateID)
		}
		if r.ArtifactPath == "" {
			t.Errorf("CT-5: gate record %q has no artifact_path", r.GateID)
		}
	}
}

// Determinism: two consecutive builds over unchanged input are byte-identical
// once the clock-derived fields (indexed_at and the gate satisfied_at stamps,
// which share one clock read) are normalized.
func TestRunIndex_DeterministicExceptClockFields(t *testing.T) {
	root := writeFixtureCorpus(t)
	outDir := t.TempDir()
	out1 := filepath.Join(outDir, "idx1.json")
	out2 := filepath.Join(outDir, "idx2.json")

	if code := runIndex(nil, indexRunConfig{docsRoot: root, output: out1, quiet: true}); code != 0 {
		t.Fatalf("first build exit %d", code)
	}
	if code := runIndex(nil, indexRunConfig{docsRoot: root, output: out2, quiet: true}); code != 0 {
		t.Fatalf("second build exit %d", code)
	}

	a := normalizeClockFields(t, out1)
	b := normalizeClockFields(t, out2)
	if a != b {
		t.Errorf("determinism: builds differ after clock normalization\n--- first ---\n%s\n--- second ---\n%s", a, b)
	}
}

// normalizeClockFields blanks indexed_at and every satisfied_at stamp so two
// builds can be compared byte for byte.
func normalizeClockFields(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["indexed_at"] = ""
	blankSatisfiedAt(raw)
	normalized, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(normalized)
}

func blankSatisfiedAt(node any) {
	switch v := node.(type) {
	case map[string]any:
		if _, ok := v["satisfied_at"]; ok {
			v["satisfied_at"] = ""
		}
		for _, child := range v {
			blankSatisfiedAt(child)
		}
	case []any:
		for _, child := range v {
			blankSatisfiedAt(child)
		}
	}
}

func findSchemaDir() string {
	candidates := []string{os.Getenv("CHAIN_SCHEMA_DIR")}
	if core := os.Getenv("WS_ORG_CORE"); core != "" {
		candidates = append(candidates, filepath.Join(core, "schemas", "cue"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "core", "schemas", "cue"))
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(c, "chain_gate.cue")); err == nil {
			return c
		}
	}
	return ""
}

func chainKeyList(idx indexJSON) []string {
	keys := make([]string, 0, len(idx.Chains))
	for k := range idx.Chains {
		keys = append(keys, k)
	}
	return keys
}
