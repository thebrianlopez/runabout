package main

import (
	"path/filepath"
	"testing"
)

// withWorkspaceRegistry stores reg for the duration of the test.
func withWorkspaceRegistry(t *testing.T, reg *WorkspaceRegistry) {
	t.Helper()
	prev := activeWorkspaceRegistry()
	setWorkspaceRegistryForTest(reg)
	t.Cleanup(func() { setWorkspaceRegistryForTest(prev) })
}

func TestTriageVerdictWorkspaceFieldsParse(t *testing.T) {
	raw := `{"score": 72, "verdict": "useful rewards article", "rubric_scores": {"relevance": 72}, "workspace": "shopping", "workspace_confidence": 0.9}`
	v, err := parseHaikuVerdictJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if v.Workspace != "shopping" {
		t.Fatalf("workspace = %q", v.Workspace)
	}
	if v.WorkspaceConfidence != 0.9 {
		t.Fatalf("workspace_confidence = %v", v.WorkspaceConfidence)
	}
}

func TestTriageVerdictWorkspaceFieldsOptional(t *testing.T) {
	raw := `{"score": 50, "verdict": "fine", "rubric_scores": {"relevance": 50}}`
	v, err := parseHaikuVerdictJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if v.Workspace != "" || v.WorkspaceConfidence != 0 {
		t.Fatalf("absent fields must be zero values, got %q %v", v.Workspace, v.WorkspaceConfidence)
	}
}

// parseHaikuVerdictJSON extracts the TriageVerdict from a raw claude CLI JSON
// envelope. Returns the verdict and call metadata.
func parseHaikuVerdictJSON(raw []byte) (TriageVerdict, error) {
	v, _, err := parseHaikuEnvelope(raw)
	return v, err
}

func TestScorecardWorkspacePopulatedByEvaluator(t *testing.T) {
	reg := loadTestRegistry(t)
	withWorkspaceRegistry(t, reg)

	// Simulate what HaikuJSONEvaluator does post-parse: normalize against the
	// active registry. Hallucinated names must collapse to "".
	v := TriageVerdict{Score: 60, Verdict: "ok", Workspace: "hallucinated", WorkspaceConfidence: 0.99}
	if got := normalizeWorkspaceAssignment(activeWorkspaceRegistry(), v.Workspace); got != "" {
		t.Fatalf("hallucinated workspace must collapse, got %q", got)
	}

	v.Workspace = "mental-health"
	if got := normalizeWorkspaceAssignment(activeWorkspaceRegistry(), v.Workspace); got != "mental-health" {
		t.Fatalf("known workspace must survive, got %q", got)
	}
}

func TestSetWorkspaceRoundTrip(t *testing.T) {
	db := filepath.Join(t.TempDir(), "queue.db")
	q, err := NewQueue(db, false)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	id, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://example.com/a", Profile: "life"})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.SetWorkspace(id, "shopping", 0.91, "tag"); err != nil {
		t.Fatal(err)
	}
	// WorkspaceReport counts only scored rows; score the row first.
	if _, err := q.ScoreByID(id, 72, "", "verdict", "slug-a", "", ""); err != nil {
		t.Fatal(err)
	}
	got, err := q.WorkspaceReport("2000-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Workspace != "shopping" || got[0].Items != 1 {
		t.Fatalf("unexpected report: %+v", got)
	}

	if err := q.SetWorkspace(id, "", 0, ""); err != nil {
		t.Fatal(err)
	}
	got, err = q.WorkspaceReport("2000-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("cleared workspace must disappear from report, got %+v", got)
	}
}
