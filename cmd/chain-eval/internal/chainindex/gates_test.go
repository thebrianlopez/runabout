package chainindex

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// gateIDRe mirrors chain_gate.cue's gate_id constraint.
var gateIDRe = regexp.MustCompile(`^[a-z0-9_-]+:[a-z_]+$`)

const testNow = "20260820T120000Z"

func withFixedNow(t *testing.T) {
	t.Helper()
	prev := nowFunc
	nowFunc = func() time.Time { return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { nowFunc = prev })
}

func captureGateStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := gateStderr
	gateStderr = buf
	t.Cleanup(func() { gateStderr = prev })
	return buf
}

// CT-5: gate_id normalization produces a schema-conformant identifier from a
// mixed-case filename containing "." and ",".
func TestGateID_ConformsToSchemaRegex(t *testing.T) {
	cases := []string{
		"epics/PERSONAL_20260729T124754Z_PiTelemetry_EPIC-251_F007_Schema_Snapshot_Validator.md",
		"design/PERSONAL_20260602T215243Z_ClaudeCode_Chain, Deterministic.Index_FDD.md",
		"releases/RELEASE 2026.06.03.md",
	}
	for _, path := range cases {
		got, err := GateID(path, GateTypeUpstreamField)
		if err != nil {
			t.Fatalf("CT-5: GateID(%q) returned error: %v", path, err)
		}
		if !gateIDRe.MatchString(got) {
			t.Errorf("CT-5: gate_id %q does not match schema regex", got)
		}
		if strings.Contains(got, "--") {
			t.Errorf("CT-5: gate_id %q contains an uncollapsed dash run", got)
		}
	}

	want := "personal_20260729t124754z_pitelemetry_epic-251_f007_schema_snapshot_validator:upstream_field"
	got, err := GateID(cases[0], GateTypeUpstreamField)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("CT-5: gate_id = %q, want %q", got, want)
	}
}

// gate_id_unnormalizable: a stem that reduces to the empty string is an error,
// not a panic and not an invalid record.
func TestGateID_UnnormalizableStem(t *testing.T) {
	if _, err := GateID("epics/...md", GateTypeUpstreamField); err == nil {
		t.Error("expected error for stem normalizing to empty")
	}
	if _, err := GateID("epics/valid.md", "upstream_field-r2"); err == nil {
		t.Error("expected error for gate_type violating [a-z_]+")
	}
}

// CT-10: two artifacts normalizing to the same stem get distinct, schema-valid ids.
func TestEvaluateUpstreamGates_GateIDCollision(t *testing.T) {
	records := []ArtifactRecord{
		{Path: "epics/EPIC.049,TDD.md", Type: ArtifactEpic, CreatedAt: "20260601T000000Z"},
		{Path: "epics/EPIC.049.TDD.md", Type: ArtifactEpic, CreatedAt: "20260601T000000Z"},
	}
	recs := EvaluateUpstreamGates(records, map[string]ChainEntry{}, map[string]bool{}, false, testNow)
	if len(recs) != 2 {
		t.Fatalf("CT-10: expected 2 records, got %d", len(recs))
	}
	if recs[0].GateID == recs[1].GateID {
		t.Errorf("CT-10: colliding gate_ids not disambiguated: %q", recs[0].GateID)
	}
	for _, r := range recs {
		if !gateIDRe.MatchString(r.GateID) {
			t.Errorf("CT-10: disambiguated gate_id %q violates schema regex", r.GateID)
		}
	}
}

// CT-1: an artifact assigned to a chain emits one satisfied record carrying the
// resolved upstream and a satisfied_at timestamp.
func TestEvaluateUpstreamGates_Satisfied(t *testing.T) {
	fdd := ChainNode{Path: "design/PERSONAL_20260601T000000Z_Alpha_FDD.md", Status: "Approved"}
	epic := ChainNode{Path: "epics/PERSONAL_20260601T000000Z_EPIC-001.md", Status: "Ready"}
	chains := map[string]ChainEntry{"alpha": {FDD: &fdd, Epics: []ChainNode{epic}}}
	records := []ArtifactRecord{
		{Path: epic.Path, Type: ArtifactEpic, CreatedAt: "20260601T000000Z", UpstreamField: fdd.Path},
	}

	recs := EvaluateUpstreamGates(records, chains, map[string]bool{epic.Path: true}, false, testNow)
	if len(recs) != 1 {
		t.Fatalf("CT-1: expected exactly 1 record, got %d", len(recs))
	}
	r := recs[0]
	if r.Status != GateStatusSatisfied {
		t.Errorf("CT-1: status = %q, want satisfied", r.Status)
	}
	if r.UpstreamArtifact != fdd.Path {
		t.Errorf("CT-1: upstream_artifact = %q, want %q", r.UpstreamArtifact, fdd.Path)
	}
	if r.SatisfiedAt != testNow {
		t.Errorf("CT-1: satisfied_at = %q, want %q", r.SatisfiedAt, testNow)
	}
	if r.GateType != GateTypeUpstreamField {
		t.Errorf("CT-1: gate_type = %q", r.GateType)
	}
}

// CT-2: a post-threshold artifact with no resolvable upstream emits one
// unsatisfied record with neither satisfied_at nor upstream_artifact.
func TestEvaluateUpstreamGates_Unsatisfied(t *testing.T) {
	records := []ArtifactRecord{
		{Path: "epics/PERSONAL_20260601T000000Z_EPIC-002.md", Type: ArtifactEpic, CreatedAt: "20260601T000000Z"},
	}
	recs := EvaluateUpstreamGates(records, map[string]ChainEntry{}, map[string]bool{}, false, testNow)
	if len(recs) != 1 {
		t.Fatalf("CT-2: expected exactly 1 record, got %d", len(recs))
	}
	r := recs[0]
	if r.Status != GateStatusUnsatisfied {
		t.Errorf("CT-2: status = %q, want unsatisfied", r.Status)
	}
	if r.SatisfiedAt != "" {
		t.Errorf("CT-2: satisfied_at = %q, want empty", r.SatisfiedAt)
	}
	if r.UpstreamArtifact != "" {
		t.Errorf("CT-2: upstream_artifact = %q, want empty", r.UpstreamArtifact)
	}
}

// CT-6: pre-threshold artifacts emit no record unless includeLegacy is set.
func TestEvaluateUpstreamGates_LegacyThreshold(t *testing.T) {
	records := []ArtifactRecord{
		{Path: "epics/OLD_EPIC.md", Type: ArtifactEpic, CreatedAt: "20260420T000000Z"},
	}
	if recs := EvaluateUpstreamGates(records, nil, nil, false, testNow); len(recs) != 0 {
		t.Errorf("CT-6: expected 0 records for pre-threshold artifact, got %d", len(recs))
	}
	if recs := EvaluateUpstreamGates(records, nil, nil, true, testNow); len(recs) != 1 {
		t.Errorf("CT-6: expected 1 record with includeLegacy, got %d", len(recs))
	}
}

// CT-7: PRDs, FDDs and sidecars are not gated by upstream_field.
func TestEvaluateUpstreamGates_RootsNotGated(t *testing.T) {
	records := []ArtifactRecord{
		{Path: "prds/PERSONAL_20260601T000000Z_Alpha_PRD.md", Type: ArtifactPRD, CreatedAt: "20260601T000000Z"},
		{Path: "design/PERSONAL_20260601T000000Z_Alpha_FDD.md", Type: ArtifactFDD, CreatedAt: "20260601T000000Z"},
		{Path: "context/PERSONAL_20260601T000000Z_Runbook.md", Type: ArtifactSidecar, CreatedAt: "20260601T000000Z"},
	}
	if recs := EvaluateUpstreamGates(records, nil, nil, false, testNow); len(recs) != 0 {
		t.Errorf("CT-7: expected 0 records for roots/sidecars, got %d: %+v", len(recs), recs)
	}
}

// CT-9: the indexer never emits a waived record - waivers are an operator act.
func TestEvaluateUpstreamGates_NeverWaived(t *testing.T) {
	fdd := ChainNode{Path: "design/PERSONAL_20260601T000000Z_Alpha_FDD.md"}
	chains := map[string]ChainEntry{"alpha": {FDD: &fdd, TDDs: []ChainNode{{Path: "design/A_TDD.md"}}}}
	records := []ArtifactRecord{
		{Path: "design/A_TDD.md", Type: ArtifactTDD, CreatedAt: "20260601T000000Z"},
		{Path: "epics/B.md", Type: ArtifactEpic, CreatedAt: "20260601T000000Z"},
		{Path: "releases/C.md", Type: ArtifactRelease, CreatedAt: "20260601T000000Z"},
		{Path: "pomo/D.md", Type: ArtifactPOMO, CreatedAt: "20260601T000000Z"},
	}
	recs := EvaluateUpstreamGates(records, chains, map[string]bool{"design/A_TDD.md": true}, false, testNow)
	for _, r := range recs {
		if r.Status != GateStatusSatisfied && r.Status != GateStatusUnsatisfied {
			t.Errorf("CT-9: unexpected status %q for %s", r.Status, r.ArtifactPath)
		}
	}
}

// Deterministic ordering: records are emitted in stable artifact-path order
// regardless of input order.
func TestEvaluateUpstreamGates_DeterministicOrdering(t *testing.T) {
	forward := []ArtifactRecord{
		{Path: "epics/A.md", Type: ArtifactEpic, CreatedAt: "20260601T000000Z"},
		{Path: "epics/B.md", Type: ArtifactEpic, CreatedAt: "20260601T000000Z"},
		{Path: "epics/C.md", Type: ArtifactEpic, CreatedAt: "20260601T000000Z"},
	}
	reversed := []ArtifactRecord{forward[2], forward[1], forward[0]}

	a := EvaluateUpstreamGates(forward, nil, nil, false, testNow)
	b := EvaluateUpstreamGates(reversed, nil, nil, false, testNow)
	if len(a) != len(b) {
		t.Fatalf("determinism: lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("determinism: record %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// RG-1 / CT-3 (unit level): a non-empty post-threshold corpus always produces
// gate records, including at least one satisfied and one unsatisfied.
func TestBuild_ProducesGateRecords(t *testing.T) {
	withFixedNow(t)
	idx := Build(regressionFixture(), t.TempDir(), false)

	if len(idx.GateRecords) < 2 {
		t.Fatalf("RG-1: expected >= 2 gate records, got %d", len(idx.GateRecords))
	}
	var satisfied, unsatisfied int
	for _, r := range idx.GateRecords {
		switch r.Status {
		case GateStatusSatisfied:
			satisfied++
			if r.SatisfiedAt != testNow {
				t.Errorf("RG-1: satisfied_at = %q, want %q", r.SatisfiedAt, testNow)
			}
		case GateStatusUnsatisfied:
			unsatisfied++
		}
		if !gateIDRe.MatchString(r.GateID) {
			t.Errorf("RG-1: gate_id %q violates schema regex", r.GateID)
		}
	}
	if satisfied == 0 || unsatisfied == 0 {
		t.Errorf("RG-1: expected both outcomes, got %d satisfied / %d unsatisfied", satisfied, unsatisfied)
	}
}

// Gate records attach to the owning chain as well as the index root.
func TestBuild_GateRecordsAttachedToChain(t *testing.T) {
	withFixedNow(t)
	idx := Build(regressionFixture(), t.TempDir(), false)

	entry, ok := idx.Chains["alpha"]
	if !ok {
		t.Fatalf("expected chain key 'alpha', got %v", chainKeys(idx))
	}
	if len(entry.GateRecords) == 0 {
		t.Fatal("expected chain-scoped gate records, got 0")
	}
	for _, r := range entry.GateRecords {
		if r.Status != GateStatusSatisfied {
			t.Errorf("chain-scoped record %q has status %q, want satisfied", r.GateID, r.Status)
		}
	}
}

// CT-11 / RG-3: a POMO declaring a resolvable Source FDD lands in chains[key].pomos.
func TestBuild_POMOLinkedToChain(t *testing.T) {
	withFixedNow(t)
	idx := Build(regressionFixture(), t.TempDir(), false)

	entry := idx.Chains["alpha"]
	if len(entry.POMOs) != 1 {
		t.Fatalf("CT-11: expected 1 linked POMO, got %d", len(entry.POMOs))
	}
	if entry.POMOs[0].Path != "pomo/PERSONAL_20260601T000000Z_POMO_alpha.md" {
		t.Errorf("CT-11: linked POMO path = %q", entry.POMOs[0].Path)
	}

	var total int
	for _, e := range idx.Chains {
		total += len(e.POMOs)
	}
	if total == 0 {
		t.Error("RG-3: summed chains[*].pomos is 0 with a resolvable Source FDD present")
	}
}

// CT-12: an unlinked post-threshold POMO emits one unsatisfied record.
func TestBuild_UnlinkedPOMOGateRecord(t *testing.T) {
	withFixedNow(t)
	idx := Build(regressionFixture(), t.TempDir(), false)

	var found bool
	for _, r := range idx.GateRecords {
		if r.ArtifactPath != "pomo/PERSONAL_20260601T000000Z_POMO_unlinked.md" {
			continue
		}
		found = true
		if r.Status != GateStatusUnsatisfied {
			t.Errorf("CT-12: unlinked POMO status = %q, want unsatisfied", r.Status)
		}
	}
	if !found {
		t.Error("CT-12: no gate record emitted for the unlinked POMO")
	}
}

// CT-13: an FDD declaring Source PRD populates chains[key].prd, preserving the
// PRD's own status so archetype-keyed gates stay evaluable.
func TestBuild_FDDLinksSourcePRD(t *testing.T) {
	withFixedNow(t)
	idx := Build(regressionFixture(), t.TempDir(), false)

	entry := idx.Chains["alpha"]
	if entry.PRD == nil {
		t.Fatal("CT-13: chains[alpha].prd is nil")
	}
	if entry.PRD.Path != "prds/PERSONAL_20260601T000000Z_Alpha_PRD.md" {
		t.Errorf("CT-13: prd.path = %q", entry.PRD.Path)
	}
	if entry.PRD.Status != "Approved" {
		t.Errorf("CT-13: prd.status = %q, want Approved (source status preserved)", entry.PRD.Status)
	}
	if entry.FDD == nil || entry.FDD.Path != "design/PERSONAL_20260601T000000Z_Alpha_FDD.md" {
		t.Error("CT-13: chain root FDD must be preserved when a PRD is linked")
	}
}

// CT-8: an empty corpus produces no records and warns gate_records_empty.
func TestBuild_EmptyCorpusWarnsGateRecordsEmpty(t *testing.T) {
	buf := captureGateStderr(t)
	idx := Build(nil, t.TempDir(), false)
	if len(idx.GateRecords) != 0 {
		t.Errorf("CT-8: expected 0 gate records, got %d", len(idx.GateRecords))
	}
	if !strings.Contains(buf.String(), "gate_records_empty") {
		t.Errorf("CT-8: expected gate_records_empty warning, got %q", buf.String())
	}
}

// A fully-legacy corpus is the other case where empty gate records are correct
// (RG-2 boundary) - the warning must still fire.
func TestBuild_LegacyOnlyCorpusWarnsGateRecordsEmpty(t *testing.T) {
	buf := captureGateStderr(t)
	records := []ArtifactRecord{
		{Path: "epics/OLD.md", Type: ArtifactEpic, CreatedAt: "20260101T000000Z"},
	}
	idx := Build(records, t.TempDir(), false)
	if len(idx.GateRecords) != 0 {
		t.Errorf("expected 0 gate records for a fully-legacy corpus, got %d", len(idx.GateRecords))
	}
	if !strings.Contains(buf.String(), "gate_records_empty") {
		t.Errorf("expected gate_records_empty warning, got %q", buf.String())
	}
}

// Two builds over unchanged input are identical, including gate records.
func TestBuild_GateRecordsDeterministic(t *testing.T) {
	withFixedNow(t)
	root := t.TempDir()
	a := Build(regressionFixture(), root, false)
	b := Build(regressionFixture(), root, false)
	if len(a.GateRecords) != len(b.GateRecords) {
		t.Fatalf("determinism: %d vs %d gate records", len(a.GateRecords), len(b.GateRecords))
	}
	for i := range a.GateRecords {
		if a.GateRecords[i] != b.GateRecords[i] {
			t.Errorf("determinism: record %d differs: %+v vs %+v", i, a.GateRecords[i], b.GateRecords[i])
		}
	}
}

// regressionFixture is the shared F6 corpus: one chain with a PRD-rooted FDD, a
// linked TDD/Epic/POMO, plus unlinked post-threshold artifacts.
func regressionFixture() []ArtifactRecord {
	fdd := "PERSONAL_20260601T000000Z_Alpha_FDD.md"
	prd := "PERSONAL_20260601T000000Z_Alpha_PRD.md"
	return []ArtifactRecord{
		{Path: "prds/" + prd, Type: ArtifactPRD, Status: "Approved", CreatedAt: "20260601T000000Z"},
		{Path: "design/" + fdd, Type: ArtifactFDD, Status: "Approved", CreatedAt: "20260601T000000Z", UpstreamField: prd},
		{Path: "design/PERSONAL_20260602T000000Z_Alpha_TDD.md", Type: ArtifactTDD, Status: "Approved", CreatedAt: "20260602T000000Z", UpstreamField: fdd},
		{Path: "epics/PERSONAL_20260603T000000Z_EPIC-001.md", Type: ArtifactEpic, Status: "Ready", CreatedAt: "20260603T000000Z", UpstreamField: fdd},
		{Path: "pomo/PERSONAL_20260601T000000Z_POMO_alpha.md", Type: ArtifactPOMO, Status: "Open", CreatedAt: "20260601T000000Z", UpstreamField: fdd},
		{Path: "pomo/PERSONAL_20260601T000000Z_POMO_unlinked.md", Type: ArtifactPOMO, Status: "Open", CreatedAt: "20260601T000000Z"},
		{Path: "epics/PERSONAL_20260604T000000Z_EPIC-002.md", Type: ArtifactEpic, Status: "Ready", CreatedAt: "20260604T000000Z"},
	}
}

// F6: the real cue binary and the real chain_gate.cue schema must accept a
// non-empty record set and reject an invalid one. Before EPIC-266 this path had
// only ever run against zero records, so both the cross-file #Timestamp
// reference and the array/struct unification defect stayed hidden.
func TestValidateGateRecords_RealSchemaRoundTrip(t *testing.T) {
	schemaDir := locateSchemaDir()
	if schemaDir == "" {
		t.Skip("core CUE schema dir not found - set CHAIN_SCHEMA_DIR or WS_ORG_CORE")
	}
	if _, err := exec.LookPath("cue"); err != nil {
		t.Skip("cue not in PATH")
	}

	withFixedNow(t)
	idx := Build(regressionFixture(), t.TempDir(), false)
	if len(idx.GateRecords) == 0 {
		t.Fatal("expected non-empty gate records")
	}
	if err := ValidateGateRecords(idx.GateRecords, schemaDir); err != nil {
		t.Errorf("expected produced records to pass cue vet, got: %v", err)
	}

	invalid := []ChainGateRecord{{
		GateID:       "BAD_Upper:upstream_field",
		GateType:     GateTypeUpstreamField,
		ArtifactPath: "epics/x.md",
		Status:       GateStatusUnsatisfied,
	}}
	if err := ValidateGateRecords(invalid, schemaDir); err == nil {
		t.Error("expected cue vet to reject an invalid gate_id, got nil")
	}
}

func locateSchemaDir() string {
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
