package chainindex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- normalizeReferent near-miss/normalisation cases ---

func TestNormalizeReferent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "clean path passes through unchanged",
			in:   "design/PERSONAL_x_FDD.md",
			want: "design/PERSONAL_x_FDD.md",
		},
		{
			name: "annotation suffix stripped",
			in:   "PERSONAL_20260514T172755Z_Linkari_System_PRD.md (F-018)",
			want: "PERSONAL_20260514T172755Z_Linkari_System_PRD.md",
		},
		{
			name: "anchor fragment stripped",
			in:   "design/PERSONAL_x_FDD.md#section-2",
			want: "design/PERSONAL_x_FDD.md",
		},
		{
			name: "bare stem gains .md",
			in:   "PERSONAL_x_FDD",
			want: "PERSONAL_x_FDD.md",
		},
		{
			name: "docs prefix stripped",
			in:   "docs/design/PERSONAL_x_FDD.md",
			want: "design/PERSONAL_x_FDD.md",
		},
		{
			name: "backtick-wrapped value unwrapped",
			in:   "`design/PERSONAL_x_FDD.md`",
			want: "design/PERSONAL_x_FDD.md",
		},
		{
			name: "annotation and anchor both stripped, order-independent result",
			in:   "PERSONAL_x_PRD.md#anchor (F-018)",
			want: "PERSONAL_x_PRD.md",
		},
		{
			name: "already-extensioned value untouched by bare-stem rule",
			in:   "PERSONAL_x_FDD.md",
			want: "PERSONAL_x_FDD.md",
		},
		{
			name: "empty input yields empty output",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeReferent(tc.in)
			if got != tc.want {
				t.Errorf("normalizeReferent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- ResolveUpstreamReferents: end-to-end outcome cases ---

func TestResolveUpstreamReferents_Resolved(t *testing.T) {
	docsRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(docsRoot, "design", "PERSONAL_x_PRD.md"), "# PRD\n")

	records := []ArtifactRecord{
		{
			Path:          "design/PERSONAL_y_FDD.md",
			Type:          ArtifactFDD,
			UpstreamField: "PERSONAL_x_PRD.md",
			UpstreamState: UpstreamExtracted,
		},
	}

	results, report := ResolveUpstreamReferents(records, docsRoot, "")

	if report.Resolved != 1 || report.Unresolved != 0 || report.Severed != 0 || report.DeclaredNoneExcluded != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(results) != 1 || results[0].Outcome != ResolutionResolved {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestResolveUpstreamReferents_Unresolved(t *testing.T) {
	docsRoot := t.TempDir()
	mustMkdirAll(t, filepath.Join(docsRoot, "design"))

	records := []ArtifactRecord{
		{
			Path:          "design/PERSONAL_y_FDD.md",
			Type:          ArtifactFDD,
			UpstreamField: "PERSONAL_does_not_exist_PRD.md",
			UpstreamState: UpstreamExtracted,
		},
	}

	results, report := ResolveUpstreamReferents(records, docsRoot, "")

	if report.Unresolved != 1 || report.Resolved != 0 || report.Severed != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(results) != 1 || results[0].Outcome != ResolutionUnresolved {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestResolveUpstreamReferents_Severed_Archive(t *testing.T) {
	docsRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(docsRoot, "prds", "archive", "PERSONAL_x_PRD.md"), "# PRD\n")

	records := []ArtifactRecord{
		{
			Path:          "design/PERSONAL_y_FDD.md",
			Type:          ArtifactFDD,
			UpstreamField: "PERSONAL_x_PRD.md",
			UpstreamState: UpstreamExtracted,
		},
	}

	results, report := ResolveUpstreamReferents(records, docsRoot, "")

	if report.Severed != 1 || report.Resolved != 0 || report.Unresolved != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(results) != 1 || results[0].Outcome != ResolutionSevered {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestResolveUpstreamReferents_DeclaredNoneExcluded(t *testing.T) {
	docsRoot := t.TempDir()
	mustMkdirAll(t, filepath.Join(docsRoot, "design"))

	records := []ArtifactRecord{
		{
			Path:          "design/PERSONAL_y_FDD.md",
			Type:          ArtifactFDD,
			UpstreamField: "NO-UPSTREAM",
			UpstreamState: UpstreamDeclaredNone,
		},
	}

	results, report := ResolveUpstreamReferents(records, docsRoot, "")

	if report.DeclaredNoneExcluded != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.candidateCount() != 0 {
		t.Fatalf("declared_none record must not enter the resolution candidate set, candidateCount=%d", report.candidateCount())
	}
	if len(results) != 0 {
		t.Fatalf("declared_none record must not appear in results at all, got: %+v", results)
	}
}

// A record whose upstream_field is empty (absent / declared_unextractable)
// must not be swept into either bucket - it is a different, already-tracked
// class (missing_upstream_field / upstream_field_unextractable).
func TestResolveUpstreamReferents_EmptyFieldSkipped(t *testing.T) {
	docsRoot := t.TempDir()

	records := []ArtifactRecord{
		{Path: "design/PERSONAL_y_FDD.md", Type: ArtifactFDD, UpstreamField: "", UpstreamState: UpstreamAbsent},
		{Path: "design/PERSONAL_z_TDD.md", Type: ArtifactTDD, UpstreamField: "", UpstreamState: UpstreamDeclaredUnextractable},
	}

	results, report := ResolveUpstreamReferents(records, docsRoot, "")

	if report.candidateCount() != 0 || report.DeclaredNoneExcluded != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got: %+v", results)
	}
}

// Regression for the orchestrator-review finding: V2's first cut restricted
// the candidate population to FDD/TDD only and silently dropped Release
// records, which also carry a Source FDD referent via the same
// extractUpstreamField path. Against the real corpus this made exactly the
// 3 genuine dangling references (all in releases/, all fabricated referents
// per research/PERSONAL_20260822T130928Z_..._Gate_Specification.md 5.3.2)
// disappear from the report instead of resolving and failing.
func TestResolveUpstreamReferents_ReleaseIncludedAndCanBeGenuinelyUnresolved(t *testing.T) {
	docsRoot := t.TempDir()
	mustMkdirAll(t, filepath.Join(docsRoot, "design"))

	records := []ArtifactRecord{
		{
			Path:          "releases/PERSONAL_z_Release.md",
			Type:          ArtifactRelease,
			UpstreamField: "PERSONAL_fabricated_never_existed_FDD.md",
			UpstreamState: UpstreamExtracted,
		},
	}

	results, report := ResolveUpstreamReferents(records, docsRoot, "")

	if report.Unresolved != 1 {
		t.Fatalf("expected Release records to enter V2's population and resolve/fail like FDD/TDD, got report: %+v", report)
	}
	if len(results) != 1 || results[0].Outcome != ResolutionUnresolved {
		t.Fatalf("unexpected results: %+v", results)
	}
}

// POMO carries an upstream obligation per gates.go's gateableTypes and must
// also enter V2's population.
func TestResolveUpstreamReferents_POMOIncluded(t *testing.T) {
	docsRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(docsRoot, "design", "PERSONAL_x_FDD.md"), "# FDD\n")

	records := []ArtifactRecord{
		{
			Path:          "pomo/POMO_y.md",
			Type:          ArtifactPOMO,
			UpstreamField: "PERSONAL_x_FDD.md",
			UpstreamState: UpstreamExtracted,
		},
	}

	results, report := ResolveUpstreamReferents(records, docsRoot, "")

	if report.Resolved != 1 {
		t.Fatalf("expected POMO records to enter V2's population, got report: %+v", report)
	}
	if len(results) != 1 || results[0].Outcome != ResolutionResolved {
		t.Fatalf("unexpected results: %+v", results)
	}
}

// PRD (chain root by definition) and Sidecar (advisory-only, never gates a
// chain - gates.go's own rationale for excluding it from gateableTypes)
// remain out of V2's population even after the Release/POMO correction.
func TestResolveUpstreamReferents_PRDAndSidecarStillExcluded(t *testing.T) {
	docsRoot := t.TempDir()
	mustMkdirAll(t, filepath.Join(docsRoot, "design"))

	records := []ArtifactRecord{
		{Path: "prds/PERSONAL_x_PRD.md", Type: ArtifactPRD, UpstreamField: "PERSONAL_does_not_exist.md", UpstreamState: UpstreamExtracted},
		{Path: "context/PERSONAL_y_Sidecar.md", Type: ArtifactSidecar, UpstreamField: "PERSONAL_does_not_exist.md", UpstreamState: UpstreamExtracted},
	}

	results, report := ResolveUpstreamReferents(records, docsRoot, "")

	if report.candidateCount() != 0 || report.DeclaredNoneExcluded != 0 {
		t.Fatalf("expected PRD and Sidecar records to stay out of V2's population entirely, got report: %+v", report)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got: %+v", results)
	}
}

// Epics carry a Source-FDD-shaped upstream_field via a different extraction
// path (extractEpicFDD) and are explicitly out of scope (Q3). A type-agnostic
// filter would wrongly sweep them into V2's flagged population.
func TestResolveUpstreamReferents_EpicsExcluded(t *testing.T) {
	docsRoot := t.TempDir()
	mustMkdirAll(t, filepath.Join(docsRoot, "design"))

	records := []ArtifactRecord{
		{
			Path:          "epics/PERSONAL_x_EPIC-001_thing.md",
			Type:          ArtifactEpic,
			UpstreamField: "PERSONAL_does_not_exist_FDD.md",
			UpstreamState: UpstreamExtracted,
		},
	}

	results, report := ResolveUpstreamReferents(records, docsRoot, "")

	if report.candidateCount() != 0 || report.DeclaredNoneExcluded != 0 {
		t.Fatalf("epic record must not enter V2's population at all, got report: %+v", report)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for an epic record, got: %+v", results)
	}
}

// Resolution against a second root (coreRoot), mirroring the two-root universe
// (~/docs + $WS_ORG_CORE) the scoping document specifies.
func TestResolveUpstreamReferents_SecondRoot(t *testing.T) {
	docsRoot := t.TempDir()
	coreRoot := t.TempDir()
	mustMkdirAll(t, filepath.Join(docsRoot, "design"))
	mustWriteFile(t, filepath.Join(coreRoot, "schemas", "PERSONAL_x_PRD.md"), "# PRD\n")

	records := []ArtifactRecord{
		{
			Path:          "design/PERSONAL_y_FDD.md",
			Type:          ArtifactFDD,
			UpstreamField: "PERSONAL_x_PRD.md",
			UpstreamState: UpstreamExtracted,
		},
	}

	results, report := ResolveUpstreamReferents(records, docsRoot, coreRoot)

	if report.Resolved != 1 {
		t.Fatalf("expected resolution against the second root, got report: %+v", report)
	}
	if len(results) != 1 || results[0].Outcome != ResolutionResolved {
		t.Fatalf("unexpected results: %+v", results)
	}
}

// citer-relative resolution: the referent exists next to the citing artifact
// but not under a resolution root's direct join.
func TestResolveUpstreamReferents_CiterRelative(t *testing.T) {
	docsRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(docsRoot, "design", "PERSONAL_x_PRD.md"), "# PRD\n")
	mustWriteFile(t, filepath.Join(docsRoot, "design", "sub", "PERSONAL_y_FDD.md"), "# FDD\n")

	records := []ArtifactRecord{
		{
			Path:          "design/sub/PERSONAL_y_FDD.md",
			Type:          ArtifactFDD,
			UpstreamField: "PERSONAL_x_PRD.md",
			UpstreamState: UpstreamExtracted,
		},
	}

	results, report := ResolveUpstreamReferents(records, docsRoot, "")

	if report.Resolved != 1 {
		t.Fatalf("expected citer-relative resolution, got report: %+v", report)
	}
	if len(results) != 1 || results[0].Outcome != ResolutionResolved {
		t.Fatalf("unexpected results: %+v", results)
	}
}

// basename-anywhere is the mandatory clause 3 fallback, per the pinned M2/M3
// predicate the scoping document carries forward.
func TestResolveUpstreamReferents_BasenameAnywhere(t *testing.T) {
	docsRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(docsRoot, "prds", "moved", "deep", "PERSONAL_x_PRD.md"), "# PRD\n")

	records := []ArtifactRecord{
		{
			Path:          "design/PERSONAL_y_FDD.md",
			Type:          ArtifactFDD,
			UpstreamField: "design/PERSONAL_x_PRD.md", // wrong directory, real basename
			UpstreamState: UpstreamExtracted,
		},
	}

	results, report := ResolveUpstreamReferents(records, docsRoot, "")

	if report.Resolved != 1 {
		t.Fatalf("expected basename-anywhere resolution, got report: %+v", report)
	}
	if len(results) != 1 || results[0].Outcome != ResolutionResolved {
		t.Fatalf("unexpected results: %+v", results)
	}
}

// Near-miss normalisation applied end-to-end: the annotation-suffixed
// referent (the epic's own LinkariAndroid_UI_Polish_FDD.md example) resolves
// once normalized, where a naive resolver would report it unresolved.
func TestResolveUpstreamReferents_AnnotationSuffixNormalizedThenResolved(t *testing.T) {
	docsRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(docsRoot, "prds", "PERSONAL_20260514T172755Z_Linkari_System_PRD.md"), "# PRD\n")

	records := []ArtifactRecord{
		{
			Path:          "design/PERSONAL_x_FDD.md",
			Type:          ArtifactFDD,
			UpstreamField: "PERSONAL_20260514T172755Z_Linkari_System_PRD.md (F-018)",
			UpstreamState: UpstreamExtracted,
		},
	}

	results, report := ResolveUpstreamReferents(records, docsRoot, "")

	if report.Resolved != 1 || report.Unresolved != 0 {
		t.Fatalf("expected annotation-suffix normalisation to enable resolution, got report: %+v", report)
	}
	if len(results) != 1 || results[0].Normalized != "PERSONAL_20260514T172755Z_Linkari_System_PRD.md" {
		t.Fatalf("unexpected results: %+v", results)
	}
}

// --- upstream_referent_resolution event emission ---

func TestBuildUpstreamReferentEvents_OnlyActionableOutcomesAggregated(t *testing.T) {
	results := []UpstreamResolution{
		{ArtifactPath: "design/a_FDD.md", ArtifactType: "fdd", Outcome: ResolutionUnresolved},
		{ArtifactPath: "design/b_FDD.md", ArtifactType: "fdd", Outcome: ResolutionUnresolved},
		{ArtifactPath: "design/c_TDD.md", ArtifactType: "tdd", Outcome: ResolutionSevered},
		{ArtifactPath: "design/d_FDD.md", ArtifactType: "fdd", Outcome: ResolutionResolved},
	}

	events := buildUpstreamReferentEvents(results, "chain-eval resolve", time.Now())

	if len(events) != 2 {
		t.Fatalf("expected 2 aggregated events (resolved outcome never emitted), got %d: %+v", len(events), events)
	}
	for _, e := range events {
		if e.EventType != EventUpstreamReferentResolution {
			t.Errorf("unexpected event_type %q", e.EventType)
		}
		switch e.Outcome {
		case ResolutionUnresolved:
			if e.Count != 2 {
				t.Errorf("expected unresolved count 2, got %d", e.Count)
			}
		case ResolutionSevered:
			if e.Count != 1 {
				t.Errorf("expected severed count 1, got %d", e.Count)
			}
		default:
			t.Errorf("unexpected outcome emitted: %q", e.Outcome)
		}
	}

	// Deterministic ordering.
	again := buildUpstreamReferentEvents(results, "chain-eval resolve", time.Now())
	for i := range events {
		if events[i].Outcome != again[i].Outcome || events[i].ArtifactType != again[i].ArtifactType {
			t.Errorf("event ordering is not deterministic at %d", i)
		}
	}
}

func TestEmitUpstreamReferentEvents_AppendsOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	eventsDir := filepath.Join(dir, "events")
	mustMkdirAll(t, eventsDir)
	existing := filepath.Join(eventsDir, nowFunc().UTC().Format(eventFileLayout)+".jsonl")
	sentinel := `{"schema_version":"2","event_type":"pre_existing"}` + "\n"
	if err := os.WriteFile(existing, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	results := []UpstreamResolution{
		{ArtifactPath: "design/a_FDD.md", ArtifactType: "fdd", Outcome: ResolutionUnresolved},
	}

	if err := EmitUpstreamReferentEvents(results, "chain-eval resolve"); err != nil {
		t.Fatalf("EmitUpstreamReferentEvents: %v", err)
	}

	content, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), sentinel) {
		t.Error("pre-existing history was rewritten; emission must be append-only")
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected the sentinel plus one emitted event, got %d lines", len(lines))
	}
	var e map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &e); err != nil {
		t.Fatalf("emitted line is not valid JSON: %v", err)
	}
	if e["event_type"] != EventUpstreamReferentResolution {
		t.Errorf("event_type = %v, want %v", e["event_type"], EventUpstreamReferentResolution)
	}
	if e["outcome"] != ResolutionUnresolved {
		t.Errorf("outcome = %v, want %v", e["outcome"], ResolutionUnresolved)
	}
}

func TestIsArchivePath(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{"prds/archive/PERSONAL_x_PRD.md", true},
		{"archive/PERSONAL_x_PRD.md", true},
		{"prds/PERSONAL_x_PRD.md", false},
		{"prds/archived/PERSONAL_x_PRD.md", false}, // segment must match exactly
	}
	for _, tc := range cases {
		if got := isArchivePath(tc.rel); got != tc.want {
			t.Errorf("isArchivePath(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}
