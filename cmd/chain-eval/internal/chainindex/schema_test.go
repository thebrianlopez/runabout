package chainindex

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Threshold-relative timestamps. orphanThreshold is 20260421T000000Z.
const (
	postThreshold = "20260501T000000Z"
	preThreshold  = "20260101T000000Z"
)

// requireCUE skips when the real cue binary or the real artifact.cue is absent.
// Every contract test in this file runs against the real schema on purpose: the
// F6 regression survived 77 days behind a stubbed validation path that had only
// ever seen zero records.
func requireCUE(t *testing.T) string {
	t.Helper()
	schemaDir := locateSchemaDir()
	if schemaDir == "" {
		t.Skip("core CUE schema dir not found - set CHAIN_SCHEMA_DIR or WS_ORG_CORE")
	}
	if _, err := os.Stat(filepath.Join(schemaDir, "artifact.cue")); err != nil {
		t.Skip("artifact.cue not found in schema dir")
	}
	if _, err := exec.LookPath("cue"); err != nil {
		t.Skip("cue not in PATH")
	}
	return schemaDir
}

// conformant returns a record that breaches nothing, as the baseline every
// fixture in this file mutates one field away from.
func conformant(typ ArtifactType, status string) ArtifactRecord {
	return ArtifactRecord{
		Path:           "design/PERSONAL_" + postThreshold + "_Fixture.md",
		Type:           typ,
		Status:         status,
		UpstreamField:  "PERSONAL_20260101T000000Z_Fixture_FDD.md",
		UpstreamState:  UpstreamExtracted,
		CreatedAt:      postThreshold,
		RuntimeVersion: "2.0.1",
		HasFrontmatter: true,
	}
}

func rulesOf(violations []SchemaViolation) map[string]int {
	out := map[string]int{}
	for _, v := range violations {
		out[v.Rule]++
	}
	return out
}

func findRule(violations []SchemaViolation, path, rule string) *SchemaViolation {
	for i := range violations {
		if violations[i].ArtifactPath == path && violations[i].Rule == rule {
			return &violations[i]
		}
	}
	return nil
}

// CT-1: a conformant artifact produces no violation.
//
// This is the one place an empty result is a legitimate assertion, because the
// fixture is deliberately clean (RG-2).
func TestCT1_ConformantArtifactProducesNoViolation(t *testing.T) {
	schemaDir := requireCUE(t)

	records := []ArtifactRecord{
		conformant(ArtifactPRD, "Approved"),
		conformant(ArtifactFDD, "Approved"),
		conformant(ArtifactTDD, "Implemented"),
		conformant(ArtifactEpic, "Complete"),
		conformant(ArtifactRelease, "Released"),
		conformant(ArtifactPOMO, "Resolved"),
	}
	for i := range records {
		records[i].Path = "design/fixture-" + string(records[i].Type) + ".md"
	}

	violations, err := ValidateArtifacts(records, schemaDir)
	if err != nil {
		t.Fatalf("CT-1: unexpected error: %v", err)
	}
	if len(violations) != 0 {
		t.Errorf("CT-1: expected no violations over a clean fixture, got %d: %+v", len(violations), violations)
	}
}

// CT-2: an out-of-enum status is caught and names the offending value.
func TestCT2_OutOfEnumStatusIsCaught(t *testing.T) {
	schemaDir := requireCUE(t)

	rec := conformant(ArtifactEpic, "Done")
	rec.Path = "epics/bad-status.md"

	violations, err := ValidateArtifacts([]ArtifactRecord{rec}, schemaDir)
	if err != nil {
		t.Fatalf("CT-2: unexpected error: %v", err)
	}
	v := findRule(violations, "epics/bad-status.md", RuleStatusEnum)
	if v == nil {
		t.Fatalf("CT-2: expected a status_enum violation, got %+v", violations)
	}
	if v.Detected != "Done" {
		t.Errorf("CT-2: expected detected %q, got %q", "Done", v.Detected)
	}
	if v.Severity != SeverityError {
		t.Errorf("CT-2: post-threshold artifact should be error, got %q", v.Severity)
	}
	// The expectation must come from cue, not from a list restated in Go.
	if !strings.Contains(v.Expected, "Complete") {
		t.Errorf("CT-2: expected CUE-derived enum in Expected, got %q", v.Expected)
	}
}

// CT-3: non-empty production over a corpus carrying one breach of each error
// class yields exactly one violation per breach.
func TestCT3_OneViolationPerBreachAcrossClasses(t *testing.T) {
	schemaDir := requireCUE(t)

	statusEnum := conformant(ArtifactEpic, "Done")
	statusEnum.Path = "epics/status-enum.md"

	statusUnparseable := conformant(ArtifactEpic, "Complete (2026-05-21)")
	statusUnparseable.Path = "epics/status-unparseable.md"

	statusAbsent := conformant(ArtifactEpic, statusAbsentSentinel)
	statusAbsent.Path = "epics/status-absent.md"

	missingUpstream := conformant(ArtifactTDD, "Approved")
	missingUpstream.Path = "design/missing-upstream_TDD.md"
	missingUpstream.UpstreamField = ""
	missingUpstream.UpstreamState = UpstreamAbsent

	unextractable := conformant(ArtifactEpic, "Complete")
	unextractable.Path = "epics/unextractable.md"
	unextractable.UpstreamField = ""
	unextractable.UpstreamState = UpstreamDeclaredUnextractable

	missingFrontmatter := conformant(ArtifactEpic, "Complete")
	missingFrontmatter.Path = "epics/no-frontmatter.md"
	missingFrontmatter.HasFrontmatter = false

	badVersion := conformant(ArtifactEpic, "Complete")
	badVersion.Path = "epics/bad-version.md"
	badVersion.RuntimeVersion = "v1.9.0"

	records := []ArtifactRecord{
		statusEnum, statusUnparseable, statusAbsent,
		missingUpstream, unextractable, missingFrontmatter, badVersion,
	}

	violations, err := ValidateArtifacts(records, schemaDir)
	if err != nil {
		t.Fatalf("CT-3: unexpected error: %v", err)
	}
	if len(violations) == 0 {
		t.Fatal("CT-3: expected a non-empty violation set over a corpus of known breaches")
	}

	want := map[string]string{
		"epics/status-enum.md":           RuleStatusEnum,
		"epics/status-unparseable.md":    RuleStatusUnparseable,
		"epics/status-absent.md":         RuleStatusAbsent,
		"design/missing-upstream_TDD.md": RuleMissingUpstreamField,
		"epics/unextractable.md":         RuleUpstreamFieldUnextractable,
		"epics/no-frontmatter.md":        RuleMissingFrontmatter,
		"epics/bad-version.md":           RuleRuntimeVersionMalformed,
	}
	for path, rule := range want {
		if findRule(violations, path, rule) == nil {
			t.Errorf("CT-3: expected %s on %s; got %+v", rule, path, violations)
		}
	}
	if len(violations) != len(want) {
		t.Errorf("CT-3: expected exactly %d violations (one per breach), got %d: %+v",
			len(want), len(violations), violations)
	}
}

// CT-4: every violation is CUE-derived. Removing the status enum from a copy of
// artifact.cue must remove the status violation and leave the others standing.
func TestCT4_RemovingRuleFromCUERemovesViolation(t *testing.T) {
	schemaDir := requireCUE(t)

	original, err := os.ReadFile(filepath.Join(schemaDir, "artifact.cue"))
	if err != nil {
		t.Fatalf("CT-4: read schema: %v", err)
	}
	stripped := strings.Replace(string(original), "status:          #EpicStatus", "status:          string", 1)
	if stripped == string(original) {
		t.Fatal("CT-4: could not strip the epic status rule; schema shape changed")
	}

	patchedDir := t.TempDir()
	writeFile(t, filepath.Join(patchedDir, "artifact.cue"), stripped)

	rec := conformant(ArtifactEpic, "Done")
	rec.Path = "epics/ct4.md"
	rec.RuntimeVersion = "N/A" // a second, untouched rule that must survive

	before, err := ValidateArtifacts([]ArtifactRecord{rec}, schemaDir)
	if err != nil {
		t.Fatalf("CT-4: baseline: %v", err)
	}
	if findRule(before, "epics/ct4.md", RuleStatusEnum) == nil {
		t.Fatal("CT-4: baseline should report status_enum")
	}

	after, err := ValidateArtifacts([]ArtifactRecord{rec}, patchedDir)
	if err != nil {
		t.Fatalf("CT-4: patched: %v", err)
	}
	if findRule(after, "epics/ct4.md", RuleStatusEnum) != nil {
		t.Error("CT-4: status_enum survived removal of the rule from artifact.cue - it is duplicated in Go")
	}
	if findRule(after, "epics/ct4.md", RuleRuntimeVersionMalformed) == nil {
		t.Error("CT-4: removing the status rule must not disable unrelated rules")
	}
}

// CT-5: a TDD without Source FDD is caught.
func TestCT5_MissingSourceFDDOnTDD(t *testing.T) {
	schemaDir := requireCUE(t)

	rec := conformant(ArtifactTDD, "Approved")
	rec.Path = "design/no-fdd_TDD.md"
	rec.UpstreamField = ""
	rec.UpstreamState = UpstreamAbsent

	violations, err := ValidateArtifacts([]ArtifactRecord{rec}, schemaDir)
	if err != nil {
		t.Fatalf("CT-5: unexpected error: %v", err)
	}
	if findRule(violations, "design/no-fdd_TDD.md", RuleMissingUpstreamField) == nil {
		t.Errorf("CT-5: expected missing_upstream_field, got %+v", violations)
	}
}

// CT-6: malformed runtime versions are caught. "v1.9.0" and "N/A" are the two
// real corpus instances.
func TestCT6_MalformedRuntimeVersion(t *testing.T) {
	schemaDir := requireCUE(t)

	for _, bad := range []string{"v1.9.0", "N/A"} {
		t.Run(bad, func(t *testing.T) {
			rec := conformant(ArtifactPRD, "Approved")
			rec.Path = "prds/ct6.md"
			rec.RuntimeVersion = bad

			violations, err := ValidateArtifacts([]ArtifactRecord{rec}, schemaDir)
			if err != nil {
				t.Fatalf("CT-6: unexpected error: %v", err)
			}
			v := findRule(violations, "prds/ct6.md", RuleRuntimeVersionMalformed)
			if v == nil {
				t.Fatalf("CT-6: expected runtime_version_malformed for %q, got %+v", bad, violations)
			}
			if v.Detected != bad {
				t.Errorf("CT-6: expected detected %q, got %q", bad, v.Detected)
			}
		})
	}

	// An absent Chain Runtime Version is not a breach: TDD section 7 rules out
	// bulk-bumping the field, so silence is the correct behavior.
	t.Run("absent is not a violation", func(t *testing.T) {
		rec := conformant(ArtifactPRD, "Approved")
		rec.Path = "prds/ct6-absent.md"
		rec.RuntimeVersion = ""

		violations, err := ValidateArtifacts([]ArtifactRecord{rec}, schemaDir)
		if err != nil {
			t.Fatalf("CT-6: unexpected error: %v", err)
		}
		if findRule(violations, "prds/ct6-absent.md", RuleRuntimeVersionMalformed) != nil {
			t.Errorf("CT-6: absent runtime version must not be reported, got %+v", violations)
		}
	})
}

// CT-7: legacy artifacts warn, never error. Severity keys on the 2026-04-21
// threshold (Q7), which is the same boundary orphan detection already uses.
func TestCT7_LegacyArtifactsWarnNeverError(t *testing.T) {
	schemaDir := requireCUE(t)

	legacy := conformant(ArtifactEpic, "Done")
	legacy.Path = "epics/legacy.md"
	legacy.CreatedAt = preThreshold

	modern := conformant(ArtifactEpic, "Done")
	modern.Path = "epics/modern.md"
	modern.CreatedAt = postThreshold

	violations, err := ValidateArtifacts([]ArtifactRecord{legacy, modern}, schemaDir)
	if err != nil {
		t.Fatalf("CT-7: unexpected error: %v", err)
	}

	l := findRule(violations, "epics/legacy.md", RuleStatusEnum)
	if l == nil {
		t.Fatal("CT-7: legacy artifact must still be reported, just not as an error")
	}
	if l.Severity != SeverityWarning {
		t.Errorf("CT-7: pre-threshold artifact must be %q, got %q", SeverityWarning, l.Severity)
	}

	m := findRule(violations, "epics/modern.md", RuleStatusEnum)
	if m == nil || m.Severity != SeverityError {
		t.Errorf("CT-7: post-threshold artifact must be %q, got %+v", SeverityError, m)
	}
}

// CT-8: an epic without frontmatter is reported. Q7 reversed the original
// blanket-warning judgment: 135 of the 226 frontmatter-less epics are
// post-threshold and 88 sit in live chains, so age decides severity.
func TestCT8_EpicWithoutFrontmatter(t *testing.T) {
	schemaDir := requireCUE(t)

	legacy := conformant(ArtifactEpic, "Complete")
	legacy.Path = "epics/legacy-nofm.md"
	legacy.CreatedAt = preThreshold
	legacy.HasFrontmatter = false

	modern := conformant(ArtifactEpic, "Complete")
	modern.Path = "epics/modern-nofm.md"
	modern.HasFrontmatter = false

	violations, err := ValidateArtifacts([]ArtifactRecord{legacy, modern}, schemaDir)
	if err != nil {
		t.Fatalf("CT-8: unexpected error: %v", err)
	}

	l := findRule(violations, "epics/legacy-nofm.md", RuleMissingFrontmatter)
	if l == nil || l.Severity != SeverityWarning {
		t.Errorf("CT-8: pre-threshold epic must warn, got %+v", l)
	}
	m := findRule(violations, "epics/modern-nofm.md", RuleMissingFrontmatter)
	if m == nil || m.Severity != SeverityError {
		t.Errorf("CT-8: post-threshold epic must error (Q7), got %+v", m)
	}
}

// CT-10: cue absent degrades cleanly to ErrCUENotFound.
func TestCT10_CUEAbsentDegradesCleanly(t *testing.T) {
	schemaDir := t.TempDir()
	writeFile(t, filepath.Join(schemaDir, "artifact.cue"), "package schemas\n")

	orig := cueRunner
	cueRunner = func(_ ...string) ([]byte, int) { return []byte("cue: command not found"), 127 }
	t.Cleanup(func() { cueRunner = orig })

	_, err := ValidateArtifacts([]ArtifactRecord{conformant(ArtifactEpic, "Complete")}, schemaDir)
	if err != ErrCUENotFound {
		t.Errorf("CT-10: expected ErrCUENotFound, got %v", err)
	}
}

// CT-10b: a missing artifact.cue is warning-class, not fatal.
func TestCT10_MissingSchemaIsWarningClass(t *testing.T) {
	_, err := ValidateArtifacts([]ArtifactRecord{conformant(ArtifactEpic, "Complete")}, t.TempDir())
	if err != ErrCUENotFound {
		t.Errorf("CT-10b: expected ErrCUENotFound for a missing schema, got %v", err)
	}
}

// CT-11: violations are deterministically ordered across runs, including when
// the input order changes. Without this the index diff is noise.
func TestCT11_DeterministicOrdering(t *testing.T) {
	schemaDir := requireCUE(t)

	mk := func(path, status string) ArtifactRecord {
		r := conformant(ArtifactEpic, status)
		r.Path = path
		return r
	}
	forward := []ArtifactRecord{
		mk("epics/c.md", "Done"), mk("epics/a.md", "Done"), mk("epics/b.md", "Done"),
	}
	reversed := []ArtifactRecord{forward[2], forward[1], forward[0]}

	first, err := ValidateArtifacts(forward, schemaDir)
	if err != nil {
		t.Fatalf("CT-11: %v", err)
	}
	second, err := ValidateArtifacts(reversed, schemaDir)
	if err != nil {
		t.Fatalf("CT-11: %v", err)
	}
	if len(first) != 3 || len(first) != len(second) {
		t.Fatalf("CT-11: expected 3 violations from both runs, got %d and %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("CT-11: ordering diverged at %d: %+v vs %+v", i, first[i], second[i])
		}
	}
	for i, want := range []string{"epics/a.md", "epics/b.md", "epics/c.md"} {
		if first[i].ArtifactPath != want {
			t.Errorf("CT-11: expected %s at %d, got %s", want, i, first[i].ArtifactPath)
		}
	}
}

// CT-12: a malformed agents: entry in epic frontmatter is caught. epic-dispatch
// reads this block, so a malformed one breaks automation silently today.
func TestCT12_MalformedEpicAgentsBlock(t *testing.T) {
	schemaDir := requireCUE(t)

	missingID := conformant(ArtifactEpic, "Ready")
	missingID.Path = "epics/agents-missing-id.md"
	missingID.EpicAgents = []EpicAgentAssignment{{CWD: "/tmp", Milestones: []any{"M1"}}}

	scalarMilestones := conformant(ArtifactEpic, "Ready")
	scalarMilestones.Path = "epics/agents-scalar-milestones.md"
	scalarMilestones.EpicAgents = []EpicAgentAssignment{{ID: "runabout-agent", Milestones: "M2"}}

	wellFormed := conformant(ArtifactEpic, "Ready")
	wellFormed.Path = "epics/agents-ok.md"
	wellFormed.EpicAgents = []EpicAgentAssignment{{ID: "runabout-agent", CWD: "/tmp", Milestones: []any{"M2", "M3"}}}

	violations, err := ValidateArtifacts(
		[]ArtifactRecord{missingID, scalarMilestones, wellFormed}, schemaDir,
	)
	if err != nil {
		t.Fatalf("CT-12: unexpected error: %v", err)
	}

	if findRule(violations, "epics/agents-missing-id.md", RuleEpicFrontmatterMalformed) == nil {
		t.Errorf("CT-12: expected a violation for an agent entry without id, got %+v", violations)
	}
	if findRule(violations, "epics/agents-scalar-milestones.md", RuleEpicFrontmatterMalformed) == nil {
		t.Errorf("CT-12: expected a violation for scalar milestones, got %+v", violations)
	}
	if findRule(violations, "epics/agents-ok.md", RuleEpicFrontmatterMalformed) != nil {
		t.Errorf("CT-12: well-formed agents block must not be reported, got %+v", violations)
	}
}

// Epics without a frontmatter block are not additionally reported for agents
// shape: the missing block is the finding, and reporting both would double-count
// one defect.
func TestEpicWithoutFrontmatterSkipsAgentsCheck(t *testing.T) {
	schemaDir := requireCUE(t)

	rec := conformant(ArtifactEpic, "Ready")
	rec.Path = "epics/nofm.md"
	rec.HasFrontmatter = false

	violations, err := ValidateArtifacts([]ArtifactRecord{rec}, schemaDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if findRule(violations, "epics/nofm.md", RuleEpicFrontmatterMalformed) != nil {
		t.Errorf("frontmatter-less epic must not raise an agents violation: %+v", violations)
	}
	if rules := rulesOf(violations); rules[RuleMissingFrontmatter] != 1 {
		t.Errorf("expected exactly one missing_frontmatter, got %v", rules)
	}
}

// The three status classes share one field, so their separation must hold.
func TestStatusClassSeparation(t *testing.T) {
	schemaDir := requireCUE(t)

	cases := []struct {
		status string
		rule   string
	}{
		{"Done", RuleStatusEnum},
		{"complete", RuleStatusEnum},
		{"Complete \u2705", RuleStatusUnparseable},
		{"Complete (2026-05-21)", RuleStatusUnparseable},
		{"Cancelled (2026-05-16)", RuleStatusUnparseable},
		{statusAbsentSentinel, RuleStatusAbsent},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			rec := conformant(ArtifactEpic, tc.status)
			rec.Path = "epics/status.md"
			violations, err := ValidateArtifacts([]ArtifactRecord{rec}, schemaDir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if findRule(violations, "epics/status.md", tc.rule) == nil {
				t.Errorf("status %q: expected %s, got %+v", tc.status, tc.rule, violations)
			}
		})
	}
}

// A record set that is empty must not invoke cue at all, and must not be
// mistaken for a clean corpus.
func TestValidateArtifacts_EmptyInput(t *testing.T) {
	called := false
	orig := cueRunner
	cueRunner = func(_ ...string) ([]byte, int) { called = true; return nil, 0 }
	t.Cleanup(func() { cueRunner = orig })

	violations, err := ValidateArtifacts(nil, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(violations) != 0 {
		t.Errorf("expected no violations for empty input, got %+v", violations)
	}
	if called {
		t.Error("cue must not be invoked for an empty record set")
	}
}
