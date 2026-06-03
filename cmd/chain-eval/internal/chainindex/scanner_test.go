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

// writeMD creates a markdown file with minimal valid frontmatter.
func writeMD(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// CT-9: Scan with nonexistent docs root returns a docs_root_not_found error.
func TestScan_DocsRootNotFound(t *testing.T) {
	_, err := Scan("/nonexistent/does/not/exist/"+t.Name(), fixedClock())
	if err == nil {
		t.Fatal("CT-9: expected error for missing docs root, got nil")
	}
	if !strings.Contains(err.Error(), "docs root not found at") {
		t.Errorf("CT-9: error message should contain 'docs root not found at', got: %v", err)
	}
}

// CT-4: A single malformed (empty) artifact emits a warning and does not abort.
// Remaining 10 valid artifacts are returned.
func TestScan_MalformedArtifactContinues(t *testing.T) {
	root := t.TempDir()
	prdDir := filepath.Join(root, "prds")
	if err := os.MkdirAll(prdDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 10 valid PRD files.
	for i := 0; i < 10; i++ {
		writeMD(t, prdDir, fmt.Sprintf("PERSONAL_20260601T00000%dZ_PRD_Test.md", i),
			"---\nstatus: Draft\n---\n# PRD\n")
	}
	// 1 empty (malformed) file.
	writeMD(t, prdDir, "PERSONAL_20260601T000011Z_PRD_Malformed.md", "")

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
	warn := warnBuf.String()
	if !strings.Contains(warn, "parse error") && !strings.Contains(warn, "artifact_parse_error") {
		t.Errorf("CT-4: expected warning about parse error, got: %q", warn)
	}
}

// CT-6: All six artifact types are returned when they exist under docs root.
func TestScan_AllArtifactTypesPresent(t *testing.T) {
	root := t.TempDir()

	for _, d := range []string{"prds", "design", "epics", "releases", "pomo", "context"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	writeMD(t, filepath.Join(root, "prds"), "PERSONAL_20260601T000000Z_Test_PRD.md",
		"## Status and Metadata\n| **Status** | Active |\n")
	writeMD(t, filepath.Join(root, "design"), "PERSONAL_20260601T000000Z_Test_FDD.md",
		"## Status and Metadata\n| **Status** | Approved |\n")
	writeMD(t, filepath.Join(root, "design"), "PERSONAL_20260601T000000Z_Test_TDD.md",
		"## Status and Metadata\n| **Status** | Approved |\n")
	writeMD(t, filepath.Join(root, "epics"), "PERSONAL_20260601T000000Z_ClaudeCode_EPIC-001_test.md",
		"---\nstatus: In Progress\n---\n# Epic\n")
	writeMD(t, filepath.Join(root, "releases"), "PERSONAL_20260601T000000Z_Test_Release.md",
		"## Status and Metadata\n| **Status** | Released |\n")
	writeMD(t, filepath.Join(root, "pomo"), "POMO_test_issue.md",
		"## Status and Metadata\n| **Status** | Open |\n")
	writeMD(t, filepath.Join(root, "context"), "PERSONAL_20260601T000000Z_Test_Sidecar.md",
		"## Status and Metadata\n| **Status** | Active |\n")

	records, err := Scan(root, fixedClock())
	if err != nil {
		t.Fatalf("CT-6: unexpected error: %v", err)
	}

	found := map[ArtifactType]bool{}
	for _, r := range records {
		found[r.Type] = true
	}

	for _, at := range []ArtifactType{ArtifactPRD, ArtifactFDD, ArtifactTDD, ArtifactEpic, ArtifactRelease, ArtifactPOMO} {
		if !found[at] {
			t.Errorf("CT-6: artifact type %q not found in scan results; got: %v", at, records)
		}
	}
}
