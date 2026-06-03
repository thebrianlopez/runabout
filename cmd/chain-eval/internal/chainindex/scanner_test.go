package chainindex

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedClock returns a deterministic time for tests that inject a clock.
func fixedClock() func() time.Time {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// mockMdqRunner builds a runner that returns canned tab-separated field output.
// Each entry in fieldLines is returned as one line of output.
func mockMdqRunner(fieldLines []string) func(args ...string) ([]byte, error) {
	return func(args ...string) ([]byte, error) {
		return []byte(strings.Join(fieldLines, "\n") + "\n"), nil
	}
}

// writeMD creates a minimal markdown file with an explicit type header.
func writeMD(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// CT-9: Scan with nonexistent docs root exits with docs_root_not_found error.
func TestScan_DocsRootNotFound(t *testing.T) {
	_, err := Scan("/nonexistent/does/not/exist/"+t.Name(), fixedClock())
	if err == nil {
		t.Fatal("CT-9: expected error for missing docs root, got nil")
	}
	if !strings.Contains(err.Error(), "docs root not found at") {
		t.Errorf("CT-9: error message should contain 'docs root not found at', got: %v", err)
	}
}

// CT-4: A single malformed artifact emits a warning and does not abort the scan.
// The remaining valid artifacts are returned (10 of 11).
func TestScan_MalformedArtifactContinues(t *testing.T) {
	root := t.TempDir()
	prdDir := filepath.Join(root, "prds")
	if err := os.MkdirAll(prdDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 10 valid PRD files + 1 that will produce malformed mdq output.
	for i := 0; i < 11; i++ {
		writeMD(t, prdDir, fmt.Sprintf("PERSONAL_2026010%dT000000Z_PRD_Test_%d.md", i%9+1, i), "---\nstatus: Draft\n---\n")
	}

	// Inject a mock that returns valid output for 10 lines but malformed for 1.
	validLines := make([]string, 10)
	for i := 0; i < 10; i++ {
		validLines[i] = fmt.Sprintf("prds/PERSONAL_20260101T000000Z_PRD_Test_%d.md\tDraft\t\t20260101T000000Z", i)
	}
	// 11th line is malformed (missing required fields).
	allLines := append(validLines, "MALFORMED_LINE_NO_TABS")

	orig := mdqRunner
	mdqRunner = mockMdqRunner(allLines)
	defer func() { mdqRunner = orig }()

	var warnBuf bytes.Buffer
	origStderr := scannerStderr
	scannerStderr = &warnBuf
	defer func() { scannerStderr = origStderr }()

	records, err := Scan(root, fixedClock())
	if err != nil {
		t.Fatalf("CT-4: expected nil error, got: %v", err)
	}
	if len(records) != 10 {
		t.Errorf("CT-4: expected 10 records, got %d", len(records))
	}
	if !strings.Contains(warnBuf.String(), "artifact_parse_error") && !strings.Contains(warnBuf.String(), "parse error") {
		t.Errorf("CT-4: expected warning to mention parse error, got: %q", warnBuf.String())
	}
}

// CT-6: All six artifact types are returned when they exist in docs.
func TestScan_AllArtifactTypesPresent(t *testing.T) {
	root := t.TempDir()

	dirs := map[ArtifactType]string{
		ArtifactPRD:     "prds",
		ArtifactFDD:     "design",
		ArtifactTDD:     "design",
		ArtifactEpic:    "epics",
		ArtifactRelease: "releases",
		ArtifactPOMO:    "pomo",
	}
	created := map[string]bool{}
	for _, d := range dirs {
		if !created[d] {
			if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
				t.Fatal(err)
			}
			created[d] = true
		}
	}

	// One file per artifact type.
	writeMD(t, filepath.Join(root, "prds"), "PERSONAL_20260601T000000Z_Test_PRD.md", "---\nstatus: Active\n---\n")
	writeMD(t, filepath.Join(root, "design"), "PERSONAL_20260601T000000Z_Test_FDD.md", "---\nstatus: Approved\n---\n")
	writeMD(t, filepath.Join(root, "design"), "PERSONAL_20260601T000000Z_Test_TDD.md", "---\nstatus: Approved\n---\n")
	writeMD(t, filepath.Join(root, "epics"), "PERSONAL_20260601T000000Z_ClaudeCode_EPIC-001.md", "---\nstatus: In Progress\n---\n")
	writeMD(t, filepath.Join(root, "releases"), "PERSONAL_20260601T000000Z_Test_Release.md", "---\nstatus: Released\n---\n")
	writeMD(t, filepath.Join(root, "pomo"), "POMO_test_issue.md", "---\nstatus: Open\n---\n")

	// Mock mdq to return one record per artifact type.
	mdqLines := []string{
		"prds/PERSONAL_20260601T000000Z_Test_PRD.md\tActive\t\t20260601T000000Z\tprd",
		"design/PERSONAL_20260601T000000Z_Test_FDD.md\tApproved\t\t20260601T000000Z\tfdd",
		"design/PERSONAL_20260601T000000Z_Test_TDD.md\tApproved\t\t20260601T000000Z\ttdd",
		"epics/PERSONAL_20260601T000000Z_ClaudeCode_EPIC-001.md\tIn Progress\t\t20260601T000000Z\tepic",
		"releases/PERSONAL_20260601T000000Z_Test_Release.md\tReleased\t\t20260601T000000Z\trelease",
		"pomo/POMO_test_issue.md\tOpen\t\t20260601T000000Z\tpomo",
	}
	orig := mdqRunner
	mdqRunner = mockMdqRunner(mdqLines)
	defer func() { mdqRunner = orig }()

	records, err := Scan(root, fixedClock())
	if err != nil {
		t.Fatalf("CT-6: unexpected error: %v", err)
	}

	found := map[ArtifactType]bool{}
	for _, r := range records {
		found[r.Type] = true
	}

	wantTypes := []ArtifactType{ArtifactPRD, ArtifactFDD, ArtifactTDD, ArtifactEpic, ArtifactRelease, ArtifactPOMO}
	for _, at := range wantTypes {
		if !found[at] {
			t.Errorf("CT-6: artifact type %q not found in scan results", at)
		}
	}
}
