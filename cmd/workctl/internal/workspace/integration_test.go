package workspace

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupBareCache creates a bare git repo cache directory with one repo.
// Returns the org root path containing the cache at ".git_cache/<repoName>.git".
func setupBareCache(t *testing.T, repoName string) string {
	t.Helper()
	orgRoot := t.TempDir()
	cacheDir := filepath.Join(orgRoot, ".git_cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}

	barePath := filepath.Join(cacheDir, repoName+".git")

	// Init bare repo
	if out, err := exec.Command("git", "init", "--bare", barePath).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %s: %v", out, err)
	}

	// Create a temporary working copy to push an initial commit
	tmpClone := filepath.Join(t.TempDir(), "init-clone")
	if out, err := exec.Command("git", "clone", barePath, tmpClone).CombinedOutput(); err != nil {
		t.Fatalf("git clone (init): %s: %v", out, err)
	}
	exec.Command("git", "-C", tmpClone, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", tmpClone, "config", "user.name", "Test").Run()

	readme := filepath.Join(tmpClone, "README.md")
	if err := os.WriteFile(readme, []byte("# "+repoName+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", tmpClone, "add", ".").Run()
	if out, err := exec.Command("git", "-C", tmpClone, "commit", "-m", "initial commit").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %s: %v", out, err)
	}
	exec.Command("git", "-C", tmpClone, "branch", "-M", "main").Run()
	if out, err := exec.Command("git", "-C", tmpClone, "push", "origin", "main").CombinedOutput(); err != nil {
		t.Fatalf("git push: %s: %v", out, err)
	}

	// Set the upstream URL to the local bare repo so gitCloneFromCache reads it
	// and git pull origin main works in tests (no network required).
	exec.Command("git", "-C", barePath, "remote", "add", "origin", "file://"+barePath).Run()

	return orgRoot
}

// testIssue returns a standard IssueInfo for integration tests.
func testIssue() *IssueInfo {
	return &IssueInfo{
		Key:         "ISRE-1234",
		Summary:     "Migrate auth service",
		Description: "<p>Move auth to new cluster.</p>",
		URL:         "https://example.atlassian.net/browse/ISRE-1234",
		Status:      "In Progress",
		Type:        "Story",
	}
}

// TestIntegration_FullInit exercises the complete Init() flow:
// mkdir → issue.md → clone from cache → pull → branch → docs symlink → manifest.
func TestIntegration_FullInit(t *testing.T) {
	orgRoot := setupBareCache(t, "my-service")

	// Create docs directory for symlink
	docsDir := filepath.Join(orgRoot, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(docsDir, "index.md"), []byte("# Docs\n"), 0o644)

	opts := &InitOptions{
		OrgPath:       orgRoot,
		WorkspaceDir:  "workspaces",
		GitCacheDir:   ".git_cache",
		DocsPath:      docsDir,
		Repos:         []string{"my-service"},
		BranchPattern: "feature/{key}",
		Issue:         testIssue(),
	}

	result, err := Init(opts)
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	// --- Verify Result struct ---

	expectedWsPath := filepath.Join(orgRoot, "workspaces", "ISRE-1234_migrate_auth_service")
	if result.WorkspacePath != expectedWsPath {
		t.Errorf("WorkspacePath = %q, want %q", result.WorkspacePath, expectedWsPath)
	}
	if result.BranchName != "feature/ISRE-1234" {
		t.Errorf("BranchName = %q, want %q", result.BranchName, "feature/ISRE-1234")
	}
	// 7 steps total: mkdir + issue_md + clone + pull + branch + docs_symlink + manifest
	if result.StepsRun != 7 {
		t.Errorf("StepsRun = %d, want 7", result.StepsRun)
	}
	if result.StepsSkipped != 0 {
		t.Errorf("StepsSkipped = %d, want 0", result.StepsSkipped)
	}

	// --- Verify workspace directory ---

	if _, err := os.Stat(expectedWsPath); os.IsNotExist(err) {
		t.Fatal("workspace directory was not created")
	}

	// --- Verify issue markdown ---

	mdPath := filepath.Join(expectedWsPath, "ISRE-1234.md")
	mdData, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("reading issue markdown: %v", err)
	}
	md := string(mdData)
	for _, want := range []string{
		"# ISRE-1234",
		"**Summary:** Migrate auth service",
		"**Status:** In Progress | **Type:** Story",
		"https://example.atlassian.net/browse/ISRE-1234",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("issue markdown missing %q", want)
		}
	}
	// Description section should exist (separator + converted/raw HTML)
	if !strings.Contains(md, "---") {
		t.Error("issue markdown missing description separator")
	}
	if !strings.Contains(md, "auth") && !strings.Contains(md, "cluster") {
		t.Error("issue markdown missing description content")
	}
	// File permissions should be 0600
	info, _ := os.Stat(mdPath)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("issue markdown permissions = %o, want 0600", info.Mode().Perm())
	}

	// --- Verify git clone from cache ---

	repoPath := filepath.Join(expectedWsPath, "my-service")
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); os.IsNotExist(err) {
		t.Fatal("cloned repo .git directory not found")
	}
	// Verify README exists (from initial commit)
	if _, err := os.Stat(filepath.Join(repoPath, "README.md")); os.IsNotExist(err) {
		t.Error("cloned repo missing README.md from initial commit")
	}
	// Verify origin URL was set to the upstream from the bare repo
	originURL, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
	if err != nil {
		t.Fatalf("git remote get-url: %v", err)
	}
	if strings.TrimSpace(string(originURL)) == "" {
		t.Error("origin URL should not be empty")
	}

	// --- Verify branch ---

	branchOut, err := exec.Command("git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	if strings.TrimSpace(string(branchOut)) != "feature/ISRE-1234" {
		t.Errorf("current branch = %q, want %q", strings.TrimSpace(string(branchOut)), "feature/ISRE-1234")
	}

	// --- Verify docs symlink ---

	linkPath := filepath.Join(expectedWsPath, "docs")
	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("docs symlink not found: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("docs path is not a symlink")
	}
	target, _ := os.Readlink(linkPath)
	if target != docsDir {
		t.Errorf("docs symlink target = %q, want %q", target, docsDir)
	}
	// Verify readability through symlink
	if _, err := os.ReadFile(filepath.Join(linkPath, "index.md")); err != nil {
		t.Errorf("cannot read through docs symlink: %v", err)
	}

	// --- Verify manifest ---

	manifestPath := filepath.Join(expectedWsPath, ".workspace.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("parsing manifest: %v", err)
	}
	if manifest.Key != "ISRE-1234" {
		t.Errorf("manifest.Key = %q, want %q", manifest.Key, "ISRE-1234")
	}
	if manifest.Summary != "Migrate auth service" {
		t.Errorf("manifest.Summary = %q, want %q", manifest.Summary, "Migrate auth service")
	}
	// All steps should be "done"
	expectedSteps := []string{"mkdir", "issue_md", "clone_my-service", "pull_my-service", "branch_my-service", "docs_symlink"}
	for _, key := range expectedSteps {
		step, ok := manifest.Steps[key]
		if !ok {
			t.Errorf("manifest missing step %q", key)
			continue
		}
		if step.Status != "done" {
			t.Errorf("manifest step %q status = %q, want %q", key, step.Status, "done")
		}
	}
}

// TestIntegration_Idempotency verifies that running Init twice skips already-completed steps.
func TestIntegration_Idempotency(t *testing.T) {
	orgRoot := setupBareCache(t, "my-service")

	opts := &InitOptions{
		OrgPath:       orgRoot,
		WorkspaceDir:  "workspaces",
		GitCacheDir:   ".git_cache",
		Repos:         []string{"my-service"},
		BranchPattern: "{key}",
		Issue:         testIssue(),
	}

	// First run
	result1, err := Init(opts)
	if err != nil {
		t.Fatalf("Init() first run: %v", err)
	}
	if result1.StepsRun == 0 {
		t.Fatal("first run should have run steps")
	}

	// Second run — same options, no force
	result2, err := Init(opts)
	if err != nil {
		t.Fatalf("Init() second run: %v", err)
	}

	// Everything except manifest write should be skipped
	// mkdir, issue_md, clone, pull, branch = 5 skipped; docs_symlink skipped (no DocsPath); manifest = 1 run
	if result2.StepsSkipped == 0 {
		t.Error("second run should skip previously completed steps")
	}
	if result2.StepsRun > 2 {
		// manifest is always written; docs_symlink may count as run
		t.Errorf("second run StepsRun = %d, expected at most 2 (manifest + maybe docs)", result2.StepsRun)
	}

	// Workspace should still be intact
	repoPath := filepath.Join(result2.WorkspacePath, "my-service")
	branchOut, _ := exec.Command("git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if strings.TrimSpace(string(branchOut)) != "ISRE-1234" {
		t.Errorf("branch after re-init = %q, want %q", strings.TrimSpace(string(branchOut)), "ISRE-1234")
	}
}

// TestIntegration_ForceReInit verifies that --force re-runs all steps.
func TestIntegration_ForceReInit(t *testing.T) {
	orgRoot := setupBareCache(t, "my-service")

	opts := &InitOptions{
		OrgPath:       orgRoot,
		WorkspaceDir:  "workspaces",
		GitCacheDir:   ".git_cache",
		Repos:         []string{"my-service"},
		BranchPattern: "{key}",
		Issue:         testIssue(),
	}

	// First run
	if _, err := Init(opts); err != nil {
		t.Fatalf("Init() first run: %v", err)
	}

	// Force re-init
	opts.Force = true
	result, err := Init(opts)
	if err != nil {
		t.Fatalf("Init(Force) error: %v", err)
	}

	// docs_symlink is always skipped when DocsPath="" (even with Force)
	if result.StepsSkipped != 1 {
		t.Errorf("Force re-init StepsSkipped = %d, want 1 (docs_symlink)", result.StepsSkipped)
	}
	// 6 steps re-run: mkdir + issue_md + clone + pull + branch + manifest
	if result.StepsRun != 6 {
		t.Errorf("Force re-init StepsRun = %d, want 6", result.StepsRun)
	}
}

// TestIntegration_MultipleRepos verifies Init with two repos.
func TestIntegration_MultipleRepos(t *testing.T) {
	// Create org root with two bare repos
	orgRoot := t.TempDir()
	cacheDir := filepath.Join(orgRoot, ".git_cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, repo := range []string{"frontend", "backend"} {
		barePath := filepath.Join(cacheDir, repo+".git")
		if out, err := exec.Command("git", "init", "--bare", barePath).CombinedOutput(); err != nil {
			t.Fatalf("git init --bare %s: %s: %v", repo, out, err)
		}

		tmpClone := filepath.Join(t.TempDir(), repo+"-init")
		if out, err := exec.Command("git", "clone", barePath, tmpClone).CombinedOutput(); err != nil {
			t.Fatalf("git clone %s: %s: %v", repo, out, err)
		}
		exec.Command("git", "-C", tmpClone, "config", "user.email", "test@test.com").Run()
		exec.Command("git", "-C", tmpClone, "config", "user.name", "Test").Run()
		os.WriteFile(filepath.Join(tmpClone, "README.md"), []byte("# "+repo+"\n"), 0o644)
		exec.Command("git", "-C", tmpClone, "add", ".").Run()
		exec.Command("git", "-C", tmpClone, "commit", "-m", "initial").CombinedOutput()
		exec.Command("git", "-C", tmpClone, "branch", "-M", "main").Run()
		exec.Command("git", "-C", tmpClone, "push", "origin", "main").CombinedOutput()
		exec.Command("git", "-C", barePath, "remote", "add", "origin", "file://"+barePath).Run()
	}

	opts := &InitOptions{
		OrgPath:       orgRoot,
		WorkspaceDir:  "ws",
		GitCacheDir:   ".git_cache",
		Repos:         []string{"frontend", "backend"},
		BranchPattern: "feature/{key}",
		Issue: &IssueInfo{
			Key:     "SR-99",
			Summary: "Full stack feature",
			URL:     "https://example.com/browse/SR-99",
			Status:  "Open",
			Type:    "Story",
		},
	}

	result, err := Init(opts)
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	// Verify both repos cloned and on correct branch
	for _, repo := range []string{"frontend", "backend"} {
		repoPath := filepath.Join(result.WorkspacePath, repo)
		if _, err := os.Stat(filepath.Join(repoPath, ".git")); os.IsNotExist(err) {
			t.Errorf("repo %s not cloned", repo)
			continue
		}
		branchOut, _ := exec.Command("git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD").Output()
		branch := strings.TrimSpace(string(branchOut))
		if branch != "feature/SR-99" {
			t.Errorf("repo %s branch = %q, want %q", repo, branch, "feature/SR-99")
		}
	}

	// Verify manifest has steps for both repos
	manifestData, _ := os.ReadFile(filepath.Join(result.WorkspacePath, ".workspace.json"))
	var m Manifest
	json.Unmarshal(manifestData, &m)
	for _, repo := range []string{"frontend", "backend"} {
		for _, prefix := range []string{"clone_", "pull_", "branch_"} {
			key := prefix + repo
			if _, ok := m.Steps[key]; !ok {
				t.Errorf("manifest missing step %q", key)
			}
		}
	}
}

// TestIntegration_ManifestSurvivesPartialFailure verifies the manifest saves progress
// when a step fails, enabling retry.
func TestIntegration_ManifestSurvivesPartialFailure(t *testing.T) {
	orgRoot := setupBareCache(t, "my-service")

	opts := &InitOptions{
		OrgPath:       orgRoot,
		WorkspaceDir:  "workspaces",
		GitCacheDir:   ".git_cache",
		Repos:         []string{"my-service", "missing-repo"}, // second repo doesn't exist in cache
		BranchPattern: "{key}",
		Issue:         testIssue(),
	}

	_, err := Init(opts)
	if err == nil {
		t.Fatal("Init should fail for missing-repo")
	}
	if !strings.Contains(err.Error(), "missing-repo") {
		t.Errorf("error should mention missing-repo, got: %v", err)
	}

	// Manifest should have been saved with completed steps
	wsPath := filepath.Join(orgRoot, "workspaces", "ISRE-1234_migrate_auth_service")
	manifest := loadManifest(wsPath)
	if manifest == nil {
		t.Fatal("manifest should be saved after partial failure")
	}

	// First repo steps should be done
	for _, key := range []string{"mkdir", "issue_md", "clone_my-service"} {
		if !isStepDone(manifest, key) {
			t.Errorf("step %q should be done after partial run", key)
		}
	}
	// Missing repo should have error state
	cloneKey := "clone_missing-repo"
	step, ok := manifest.Steps[cloneKey]
	if !ok {
		t.Errorf("manifest should have step %q", cloneKey)
	} else if step.Status != "error" {
		t.Errorf("step %q status = %q, want %q", cloneKey, step.Status, "error")
	}
}

// TestIntegration_NoDocsPath verifies Init works without a docs path.
func TestIntegration_NoDocsPath(t *testing.T) {
	orgRoot := setupBareCache(t, "my-service")

	opts := &InitOptions{
		OrgPath:       orgRoot,
		WorkspaceDir:  "workspaces",
		GitCacheDir:   ".git_cache",
		DocsPath:      "", // no docs
		Repos:         []string{"my-service"},
		BranchPattern: "{key}",
		Issue:         testIssue(),
	}

	result, err := Init(opts)
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	// Docs symlink should not exist
	linkPath := filepath.Join(result.WorkspacePath, "docs")
	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Error("docs symlink should not exist when DocsPath is empty")
	}

	// docs_symlink should be counted as skipped
	if result.StepsSkipped != 1 {
		t.Errorf("StepsSkipped = %d, want 1 (docs_symlink)", result.StepsSkipped)
	}
}
