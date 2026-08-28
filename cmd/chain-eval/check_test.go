package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	writeFixture(t, root, "README.md", "initial\n")
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-qm", "initial")
	return root
}

func commitAll(t *testing.T, root, message string) string {
	t.Helper()
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-qm", message)
	return gitRun(t, root, "rev-parse", "HEAD")
}

func TestScopeChangedArtifactsCommitRange_CT1(t *testing.T) {
	root := fixtureRepo(t)
	base := gitRun(t, root, "rev-parse", "HEAD")
	writeFixture(t, root, "epics/EPIC-001.md", "# Epic\n")
	for _, p := range []string{"src/a.go", "src/b.txt", "notes.md", "README.txt", "design/a.txt", "prds/a.txt", "context/a.txt", "releases/a.txt", "pomo/a.txt"} {
		writeFixture(t, root, p, "x\n")
	}
	head := commitAll(t, root, "changes")

	got, err := scopeChangedArtifacts(diffModeCommitRange, root, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if got.Changed != 10 || len(got.Validated) != 1 || got.Validated[0].Path != "epics/EPIC-001.md" {
		t.Fatalf("unexpected set: %+v", got)
	}
	if got.Skipped.UnsupportedDir+got.Skipped.NonMarkdown != 9 {
		t.Fatalf("skip count: %+v", got.Skipped)
	}
}

func TestScopeChangedArtifactsWorkingTree_CT1b(t *testing.T) {
	root := fixtureRepo(t)
	writeFixture(t, root, "design/PERSONAL_Test_FDD.md", "# FDD\n")
	commitAll(t, root, "fdd")
	writeFixture(t, root, "design/PERSONAL_Test_FDD.md", "# Changed FDD\n")
	got, err := scopeChangedArtifacts(diffModeWorkingTree, root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Changed != 1 || len(got.Validated) != 1 || got.Validated[0].Type != "fdd" {
		t.Fatalf("unexpected set: %+v", got)
	}
}

func TestScopeChangedArtifactsAllIrrelevant_CT2(t *testing.T) {
	root := fixtureRepo(t)
	base := gitRun(t, root, "rev-parse", "HEAD")
	writeFixture(t, root, "src/a.go", "package a\n")
	writeFixture(t, root, "design/notes.txt", "x\n")
	writeFixture(t, root, "misc/readme.md", "x\n")
	head := commitAll(t, root, "irrelevant")
	got, err := scopeChangedArtifacts(diffModeCommitRange, root, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if got.Changed != 3 || len(got.Validated) != 0 || len(got.Deleted) != 0 || got.Skipped.UnsupportedDir+got.Skipped.NonMarkdown != 3 {
		t.Fatalf("unexpected set: %+v", got)
	}
	var stdout, stderr strings.Builder
	if code := runCheck(checkRunConfig{docsRoot: root, base: base, head: head}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 6 || !strings.Contains(lines[0], "3 changed, 0 validated") || !strings.Contains(lines[1], "0 unreadable") {
		t.Fatalf("counter output must be exactly six complete lines: %q", stdout.String())
	}
}

func TestRunCheckAdvisoryErrors_CT2b_CT2c(t *testing.T) {
	root := fixtureRepo(t)
	for _, tc := range []struct {
		name  string
		cfg   checkRunConfig
		class string
	}{
		{"bad refs", checkRunConfig{docsRoot: root, base: "missing", head: "HEAD"}, "unreadable_diff"},
		{"missing mode", checkRunConfig{docsRoot: root}, "usage_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := runCheck(tc.cfg, &stdout, &stderr)
			if code != 0 || !strings.Contains(stderr.String(), tc.class) {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestScopeChangedArtifactsUnreadable_CT14(t *testing.T) {
	root := fixtureRepo(t)
	base := gitRun(t, root, "rev-parse", "HEAD")
	writeFixture(t, root, "epics/EPIC-002.md", "# Epic\n")
	head := commitAll(t, root, "epic")
	if err := os.Remove(filepath.Join(root, "epics/EPIC-002.md")); err != nil {
		t.Fatal(err)
	}
	got, err := scopeChangedArtifacts(diffModeCommitRange, root, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if got.Changed != 1 || got.Skipped.Unreadable != 1 || len(got.Validated) != 0 || len(got.Findings) != 1 || got.Findings[0].Class != "unreadable_artifact" {
		t.Fatalf("unexpected set: %+v", got)
	}
	var stdout, stderr strings.Builder
	if code := runCheck(checkRunConfig{docsRoot: root, base: base, head: head}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "unreadable_artifact") {
		t.Fatalf("unreadable_artifact finding missing from stdout: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "unreadable_artifact") {
		t.Fatalf("unreadable_artifact finding must not be written to stderr: %q", stderr.String())
	}
}

func TestCounterInvariantTerminalOutcomes_CT15_CT16(t *testing.T) {
	valid := ChangedArtifactSet{Changed: 5, Validated: make([]ChangedArtifact, 1), Deleted: make([]ChangedArtifact, 1), Skipped: SkipCounts{UnsupportedDir: 1, NonMarkdown: 1, Unreadable: 1}}
	if err := checkCounterInvariant(valid); err != nil {
		t.Fatal(err)
	}
	bad := ChangedArtifactSet{Changed: 1}
	err := checkCounterInvariant(bad)
	if !errors.Is(err, ErrCounterInvariantViolated) || !strings.Contains(err.Error(), "changed=1") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScopeChangedArtifactsPrecedence_CT17(t *testing.T) {
	root := fixtureRepo(t)
	base := gitRun(t, root, "rev-parse", "HEAD")
	writeFixture(t, root, "src/foo.txt", "x\n")
	writeFixture(t, root, "design/notes.txt", "x\n")
	head := commitAll(t, root, "overlap")
	if err := os.Remove(filepath.Join(root, "design/notes.txt")); err != nil {
		t.Fatal(err)
	}
	got, err := scopeChangedArtifacts(diffModeCommitRange, root, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if got.Changed != 2 || got.Skipped.UnsupportedDir != 1 || got.Skipped.NonMarkdown != 1 || got.Skipped.Unreadable != 0 || len(got.Deleted) != 0 {
		t.Fatalf("unexpected precedence: %+v", got)
	}
}
