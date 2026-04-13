package workspace

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildFolderName(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		summary string
		want    string
	}{
		{"basic", "ISRE-1234", "Migrate auth service", "ISRE-1234_migrate_auth_service"},
		{"special chars", "SR-99", "Fix: the bug (v2) [urgent]", "SR-99_fix_the_bug_v2_urgent"},
		{"empty summary", "ISRE-5", "", "ISRE-5"},
		{"unicode", "ISRE-42", "Add café support — résumé", "ISRE-42_add_café_support_résumé"},
		{"trailing spaces", "SR-1", "trailing   ", "SR-1_trailing"},
		{"only special chars", "SR-1", "!@#$%", "SR-1"},
		{"long title", "ISRE-1234", "This is a very long title that should be truncated to keep the total path reasonable and prevent filesystem issues with overly long names", "ISRE-1234_this_is_a_very_long_title_that_should_be_truncated_to_keep_the_total_p"},
		{"numbers in summary", "SR-1", "Add v2.0 API endpoints", "SR-1_add_v2_0_api_endpoints"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildFolderName(tt.key, tt.summary)
			if got != tt.want {
				t.Errorf("buildFolderName(%q, %q) = %q, want %q", tt.key, tt.summary, got, tt.want)
			}
		})
	}
}

func TestResolveBranchName(t *testing.T) {
	tests := []struct {
		pattern string
		key     string
		want    string
	}{
		{"{key}", "ISRE-1234", "ISRE-1234"},
		{"feature/{key}", "SR-99", "feature/SR-99"},
		{"{key}_work", "ISRE-1", "ISRE-1_work"},
		{"plain-branch", "SR-1", "plain-branch"},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			got := resolveBranchName(tt.pattern, tt.key)
			if got != tt.want {
				t.Errorf("resolveBranchName(%q, %q) = %q, want %q", tt.pattern, tt.key, got, tt.want)
			}
		})
	}
}

func TestIsValidJiraKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"ISRE-1234", true},
		{"SR-1", true},
		{"A-1", false},
		{"ABC-99999", true},
		{"isre-1234", false}, // lowercase
		{"ISRE", false},      // no number
		{"1234", false},      // no project
		{"-1234", false},     // no project
		{"ISRE-", false},     // no number
		{"ISRE-abc", false},  // non-numeric
		{"", false},          // empty
		{"IS RE-123", false}, // space
		{"A2B-123", true},    // digits in project
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := IsValidJiraKey(tt.key)
			if got != tt.want {
				t.Errorf("IsValidJiraKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestFindBareRepo(t *testing.T) {
	tmp := t.TempDir()

	// Create name.git directory
	gitDir := filepath.Join(tmp, "my-repo.git")
	if err := os.Mkdir(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create plain directory
	plainDir := filepath.Join(tmp, "other-repo")
	if err := os.Mkdir(plainDir, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		repo    string
		want    string
		wantErr bool
	}{
		{"finds .git suffix", "my-repo", gitDir, false},
		{"finds plain name", "other-repo", plainDir, false},
		{"not found", "missing-repo", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findBareRepo(tmp, tt.repo)
			if (err != nil) != tt.wantErr {
				t.Errorf("findBareRepo(%q) error = %v, wantErr %v", tt.repo, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("findBareRepo(%q) = %q, want %q", tt.repo, got, tt.want)
			}
		})
	}
}

func TestManifestRoundTrip(t *testing.T) {
	tmp := t.TempDir()

	m := &Manifest{
		Key:       "ISRE-123",
		Summary:   "Test issue",
		CreatedAt: "2025-01-01T00:00:00Z",
		Steps: map[string]StepStatus{
			"mkdir":     {Status: "done", Timestamp: "2025-01-01T00:00:01Z"},
			"issue_md":  {Status: "done", Timestamp: "2025-01-01T00:00:02Z"},
			"clone_foo": {Status: "error", Timestamp: "2025-01-01T00:00:03Z", Error: "not found"},
		},
	}

	saveManifest(tmp, m)

	loaded := loadManifest(tmp)
	if loaded == nil {
		t.Fatal("loadManifest returned nil")
	}

	if loaded.Key != m.Key {
		t.Errorf("Key = %q, want %q", loaded.Key, m.Key)
	}
	if loaded.Summary != m.Summary {
		t.Errorf("Summary = %q, want %q", loaded.Summary, m.Summary)
	}
	if len(loaded.Steps) != len(m.Steps) {
		t.Errorf("Steps count = %d, want %d", len(loaded.Steps), len(m.Steps))
	}
	if !isStepDone(loaded, "mkdir") {
		t.Error("mkdir should be done")
	}
	if isStepDone(loaded, "clone_foo") {
		t.Error("clone_foo should not be done (error state)")
	}
	if isStepDone(loaded, "nonexistent") {
		t.Error("nonexistent should not be done")
	}

	// Verify JSON is well-formed
	data, err := os.ReadFile(filepath.Join(tmp, ".workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Error("manifest JSON is not valid")
	}
}

func TestLoadManifest_Missing(t *testing.T) {
	tmp := t.TempDir()
	m := loadManifest(tmp)
	if m != nil {
		t.Error("expected nil manifest for missing file")
	}
}

func TestLoadManifest_Invalid(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".workspace.json"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	m := loadManifest(tmp)
	if m != nil {
		t.Error("expected nil manifest for invalid JSON")
	}
}

func TestWriteIssueMD(t *testing.T) {
	tests := []struct {
		name  string
		issue *IssueInfo
	}{
		{
			name: "full issue",
			issue: &IssueInfo{
				Key:     "ISRE-1234",
				Summary: "Migrate auth service to new cluster",
				URL:     "https://example.atlassian.net/browse/ISRE-1234",
				Status:  "In Progress",
				Type:    "Story",
			},
		},
		{
			name: "minimal issue",
			issue: &IssueInfo{
				Key:     "SR-1",
				Summary: "",
				URL:     "",
				Status:  "",
				Type:    "",
			},
		},
		{
			name: "issue with special characters in summary",
			issue: &IssueInfo{
				Key:     "DATA-99",
				Summary: "Fix: \"quoted\" & <angled> stuff",
				URL:     "https://jira.example.com/browse/DATA-99",
				Status:  "Done",
				Type:    "Bug",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			if err := writeIssueMD(tmp, tt.issue); err != nil {
				t.Fatalf("writeIssueMD error: %v", err)
			}

			mdPath := filepath.Join(tmp, tt.issue.Key+".md")
			data, err := os.ReadFile(mdPath)
			if err != nil {
				t.Fatalf("reading written file: %v", err)
			}
			content := string(data)

			// Verify header line
			expectedHeader := "# " + tt.issue.Key
			if !strings.Contains(content, expectedHeader) {
				t.Errorf("content missing header %q:\n%s", expectedHeader, content)
			}

			// Verify summary line
			expectedSummary := "**Summary:** " + tt.issue.Summary
			if !strings.Contains(content, expectedSummary) {
				t.Errorf("content missing summary line %q:\n%s", expectedSummary, content)
			}

			// Verify status and type line
			expectedStatusType := "**Status:** " + tt.issue.Status + " | **Type:** " + tt.issue.Type
			if !strings.Contains(content, expectedStatusType) {
				t.Errorf("content missing status/type line %q:\n%s", expectedStatusType, content)
			}

			// Verify URL is present
			if !strings.Contains(content, tt.issue.URL) {
				t.Errorf("content missing URL %q:\n%s", tt.issue.URL, content)
			}

			// Verify file permissions
			info, err := os.Stat(mdPath)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if info.Mode().Perm() != 0600 {
				t.Errorf("file permissions = %o, want 0600", info.Mode().Perm())
			}
		})
	}
}

func TestWriteIssueMD_BadPath(t *testing.T) {
	err := writeIssueMD("/nonexistent/path/that/does/not/exist", &IssueInfo{Key: "SR-1"})
	if err == nil {
		t.Error("writeIssueMD to nonexistent path should return error")
	}
}

// TestWriteIssueMD_WithDescription verifies that the updated writeIssueMD includes
// the pandoc-converted description section after the separator.
func TestWriteIssueMD_WithDescription(t *testing.T) {
	tests := []struct {
		name              string
		issue             *IssueInfo
		wantSeparator     bool
		wantDescSubstring string
	}{
		{
			name: "issue with HTML description",
			issue: &IssueInfo{
				Key:         "ISRE-500",
				Summary:     "Add monitoring dashboards",
				Description: "<p>We need to add Grafana dashboards for the new service.</p>",
				URL:         "https://example.atlassian.net/browse/ISRE-500",
				Status:      "In Progress",
				Type:        "Story",
			},
			wantSeparator:     true,
			wantDescSubstring: "dashboards for the new service",
		},
		{
			name: "issue with empty description",
			issue: &IssueInfo{
				Key:         "SR-10",
				Summary:     "Quick fix",
				Description: "",
				URL:         "https://example.atlassian.net/browse/SR-10",
				Status:      "Open",
				Type:        "Bug",
			},
			wantSeparator:     false,
			wantDescSubstring: "",
		},
		{
			name: "issue with complex HTML description",
			issue: &IssueInfo{
				Key:         "DATA-200",
				Summary:     "Implement ETL pipeline",
				Description: "<h2>Requirements</h2><ul><li>Extract from S3</li><li>Transform with Spark</li><li>Load to Redshift</li></ul>",
				URL:         "https://example.atlassian.net/browse/DATA-200",
				Status:      "To Do",
				Type:        "Epic",
			},
			wantSeparator:     true,
			wantDescSubstring: "Extract from S3",
		},
		{
			name: "issue with description containing special characters",
			issue: &IssueInfo{
				Key:         "ISRE-777",
				Summary:     "Handle edge cases",
				Description: "<p>Fix the &amp; and &lt;bracket&gt; issues in config parsing.</p>",
				URL:         "https://example.atlassian.net/browse/ISRE-777",
				Status:      "In Review",
				Type:        "Bug",
			},
			wantSeparator:     true,
			wantDescSubstring: "issues in config parsing",
		},
		{
			name: "issue with code block in description",
			issue: &IssueInfo{
				Key:         "SR-42",
				Summary:     "Add retry logic",
				Description: "<p>Add retry with backoff:</p><pre><code>time.Sleep(backoff)</code></pre>",
				URL:         "https://example.atlassian.net/browse/SR-42",
				Status:      "Open",
				Type:        "Task",
			},
			wantSeparator:     true,
			wantDescSubstring: "retry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			if err := writeIssueMD(tmp, tt.issue); err != nil {
				t.Fatalf("writeIssueMD error: %v", err)
			}

			mdPath := filepath.Join(tmp, tt.issue.Key+".md")
			data, err := os.ReadFile(mdPath)
			if err != nil {
				t.Fatalf("reading written file: %v", err)
			}
			content := string(data)

			if !strings.Contains(content, "# "+tt.issue.Key) {
				t.Errorf("content missing header %q:\n%s", tt.issue.Key, content)
			}
			if !strings.Contains(content, "**Summary:** "+tt.issue.Summary) {
				t.Errorf("content missing summary:\n%s", content)
			}

			hasSeparator := strings.Contains(content, "\n---\n")
			if hasSeparator != tt.wantSeparator {
				t.Errorf("separator present = %v, want %v\ncontent:\n%s", hasSeparator, tt.wantSeparator, content)
			}

			if tt.wantDescSubstring != "" && !strings.Contains(content, tt.wantDescSubstring) {
				t.Errorf("content missing description substring %q:\n%s", tt.wantDescSubstring, content)
			}

			if tt.issue.Description == "" {
				lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
				lastNonEmpty := ""
				for i := len(lines) - 1; i >= 0; i-- {
					if strings.TrimSpace(lines[i]) != "" {
						lastNonEmpty = strings.TrimSpace(lines[i])
						break
					}
				}
				if lastNonEmpty == "---" {
					t.Errorf("empty description should not produce a separator:\n%s", content)
				}
			}
		})
	}
}

// TestWriteIssueMD_DescriptionFallback verifies pandoc fallback behavior.
func TestWriteIssueMD_DescriptionFallback(t *testing.T) {
	tmp := t.TempDir()
	htmlDesc := "<p>This is a <strong>bold</strong> description.</p>"
	issue := &IssueInfo{
		Key:         "SR-99",
		Summary:     "Test pandoc fallback",
		Description: htmlDesc,
		URL:         "https://example.com/browse/SR-99",
		Status:      "Open",
		Type:        "Task",
	}

	if err := writeIssueMD(tmp, issue); err != nil {
		t.Fatalf("writeIssueMD error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "SR-99.md"))
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "bold") {
		t.Errorf("content should contain 'bold' from description:\n%s", content)
	}
	if !strings.Contains(content, "\n---\n") {
		t.Errorf("content missing separator for non-empty description:\n%s", content)
	}

	if !hasPandoc() {
		if !strings.Contains(content, htmlDesc) {
			t.Errorf("without pandoc, raw HTML should appear in output:\n%s", content)
		}
	} else {
		if strings.Contains(content, "<p>") {
			t.Errorf("with pandoc available, HTML tags should be converted:\n%s", content)
		}
	}
}

// TestInitDryRun_WithDocsPath verifies dry run with docs path set.
func TestInitDryRun_WithDocsPath(t *testing.T) {
	tmp := t.TempDir()
	docsDir := filepath.Join(tmp, "org-docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatal(err)
	}

	opts := &InitOptions{
		OrgPath:       tmp,
		WorkspaceDir:  "workspaces",
		GitCacheDir:   ".git_cache",
		DocsPath:      docsDir,
		Repos:         []string{"repo-a"},
		BranchPattern: "feature/{key}",
		Issue: &IssueInfo{
			Key:     "ISRE-100",
			Summary: "Dry run with docs",
			URL:     "https://example.com/browse/ISRE-100",
			Status:  "Open",
			Type:    "Task",
		},
		DryRun: true,
	}

	result, err := Init(opts)
	if err != nil {
		t.Fatalf("Init(DryRun with docs) error: %v", err)
	}

	wsDir := filepath.Join(tmp, "workspaces")
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("dry run should not create workspace directory, but %s exists", wsDir)
	}

	if result.StepsRun != 0 {
		t.Errorf("StepsRun = %d, want 0 in dry run", result.StepsRun)
	}
}

func TestInitDryRun(t *testing.T) {
	tmp := t.TempDir()
	opts := &InitOptions{
		OrgPath:       tmp,
		WorkspaceDir:  "workspaces",
		GitCacheDir:   ".git_cache",
		Repos:         []string{"repo-a", "repo-b"},
		BranchPattern: "feature/{key}",
		Issue: &IssueInfo{
			Key:     "ISRE-42",
			Summary: "Test dry run",
			URL:     "https://example.com/browse/ISRE-42",
			Status:  "Open",
			Type:    "Task",
		},
		DryRun: true,
	}

	result, err := Init(opts)
	if err != nil {
		t.Fatalf("Init(DryRun) error: %v", err)
	}

	// Result should still have correct workspace path and branch name
	expectedWsPath := filepath.Join(tmp, "workspaces", "ISRE-42_test_dry_run")
	if result.WorkspacePath != expectedWsPath {
		t.Errorf("WorkspacePath = %q, want %q", result.WorkspacePath, expectedWsPath)
	}
	if result.BranchName != "feature/ISRE-42" {
		t.Errorf("BranchName = %q, want %q", result.BranchName, "feature/ISRE-42")
	}

	// Dry run should NOT create the workspace directory
	wsDir := filepath.Join(tmp, "workspaces")
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("dry run should not create workspace directory, but %s exists", wsDir)
	}

	// Dry run should NOT create issue markdown
	mdPath := filepath.Join(expectedWsPath, "ISRE-42.md")
	if _, err := os.Stat(mdPath); !os.IsNotExist(err) {
		t.Errorf("dry run should not create issue markdown, but %s exists", mdPath)
	}

	// Dry run should NOT create manifest
	manifestPath := filepath.Join(expectedWsPath, ".workspace.json")
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Errorf("dry run should not create manifest, but %s exists", manifestPath)
	}

	// StepsRun and StepsSkipped should both be 0 in dry run
	if result.StepsRun != 0 {
		t.Errorf("StepsRun = %d, want 0 in dry run", result.StepsRun)
	}
	if result.StepsSkipped != 0 {
		t.Errorf("StepsSkipped = %d, want 0 in dry run", result.StepsSkipped)
	}
}

func TestInitDryRun_NoRepos(t *testing.T) {
	tmp := t.TempDir()
	opts := &InitOptions{
		OrgPath:       tmp,
		WorkspaceDir:  "ws",
		GitCacheDir:   ".cache",
		Repos:         nil,
		BranchPattern: "{key}",
		Issue: &IssueInfo{
			Key:     "SR-1",
			Summary: "Simple",
			URL:     "https://example.com/browse/SR-1",
			Status:  "Open",
			Type:    "Task",
		},
		DryRun: true,
	}

	result, err := Init(opts)
	if err != nil {
		t.Fatalf("Init(DryRun, no repos) error: %v", err)
	}

	if result.BranchName != "SR-1" {
		t.Errorf("BranchName = %q, want %q", result.BranchName, "SR-1")
	}

	// Nothing should be created on disk
	wsDir := filepath.Join(tmp, "ws")
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("dry run should not create any directory, but %s exists", wsDir)
	}
}

func TestBuildFolderName_ConsecutiveSpecialChars(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		summary string
		want    string
	}{
		{
			"consecutive hyphens and spaces",
			"SR-1",
			"fix -- the -- bug",
			"SR-1_fix_the_bug",
		},
		{
			"mixed consecutive specials",
			"SR-2",
			"foo!!!bar???baz",
			"SR-2_foo_bar_baz",
		},
		{
			"leading special chars",
			"SR-3",
			"---leading",
			"SR-3_leading",
		},
		{
			"trailing consecutive specials",
			"SR-4",
			"trailing---",
			"SR-4_trailing",
		},
		{
			"all special chars between words",
			"SR-5",
			"a!@#b$%^c",
			"SR-5_a_b_c",
		},
		{
			"tabs and newlines",
			"SR-6",
			"word1\t\tword2\n\nword3",
			"SR-6_word1_word2_word3",
		},
		{
			"single char words with specials",
			"SR-7",
			"a--b--c",
			"SR-7_a_b_c",
		},
		{
			"emoji and special unicode",
			"SR-8",
			"deploy rocket to moon",
			"SR-8_deploy_rocket_to_moon",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildFolderName(tt.key, tt.summary)
			if got != tt.want {
				t.Errorf("buildFolderName(%q, %q) = %q, want %q", tt.key, tt.summary, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests for gitPullMain
// ---------------------------------------------------------------------------

// setupBareAndClone creates a bare git repo with an initial commit on main,
// then clones it into a working directory.
func setupBareAndClone(t *testing.T) (bareRepo, clonePath string) {
	t.Helper()
	tmp := t.TempDir()

	bareRepo = filepath.Join(tmp, "origin.git")
	clonePath = filepath.Join(tmp, "clone")

	if out, err := exec.Command("git", "init", "--bare", bareRepo).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %s: %v", out, err)
	}

	initCopy := filepath.Join(tmp, "init-copy")
	if out, err := exec.Command("git", "clone", bareRepo, initCopy).CombinedOutput(); err != nil {
		t.Fatalf("git clone (init): %s: %v", out, err)
	}

	exec.Command("git", "-C", initCopy, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", initCopy, "config", "user.name", "Test").Run()

	initFile := filepath.Join(initCopy, "README.md")
	if err := os.WriteFile(initFile, []byte("# Test Repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", initCopy, "add", ".").Run()
	if out, err := exec.Command("git", "-C", initCopy, "commit", "-m", "initial commit").CombinedOutput(); err != nil {
		t.Fatalf("git commit (init): %s: %v", out, err)
	}

	exec.Command("git", "-C", initCopy, "branch", "-M", "main").Run()

	if out, err := exec.Command("git", "-C", initCopy, "push", "origin", "main").CombinedOutput(); err != nil {
		t.Fatalf("git push (init): %s: %v", out, err)
	}

	if out, err := exec.Command("git", "clone", bareRepo, clonePath).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %s: %v", out, err)
	}

	exec.Command("git", "-C", clonePath, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", clonePath, "config", "user.name", "Test").Run()
	exec.Command("git", "-C", clonePath, "checkout", "main").Run()

	return bareRepo, clonePath
}

func TestGitPullMain_ValidRepo(t *testing.T) {
	_, clonePath := setupBareAndClone(t)

	err := gitPullMain(clonePath)
	if err != nil {
		t.Errorf("gitPullMain on valid repo should succeed, got: %v", err)
	}
}

func TestGitPullMain_ValidRepo_PullsLatest(t *testing.T) {
	bareRepo, clonePath := setupBareAndClone(t)

	tmp := t.TempDir()
	otherClone := filepath.Join(tmp, "other")
	if out, err := exec.Command("git", "clone", bareRepo, otherClone).CombinedOutput(); err != nil {
		t.Fatalf("git clone (other): %s: %v", out, err)
	}
	exec.Command("git", "-C", otherClone, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", otherClone, "config", "user.name", "Test").Run()
	exec.Command("git", "-C", otherClone, "checkout", "main").Run()

	newFile := filepath.Join(otherClone, "new_file.txt")
	if err := os.WriteFile(newFile, []byte("new content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", otherClone, "add", ".").Run()
	if out, err := exec.Command("git", "-C", otherClone, "commit", "-m", "add new file").CombinedOutput(); err != nil {
		t.Fatalf("git commit (other): %s: %v", out, err)
	}
	if out, err := exec.Command("git", "-C", otherClone, "push", "origin", "main").CombinedOutput(); err != nil {
		t.Fatalf("git push (other): %s: %v", out, err)
	}

	err := gitPullMain(clonePath)
	if err != nil {
		t.Fatalf("gitPullMain should succeed: %v", err)
	}

	pulledFile := filepath.Join(clonePath, "new_file.txt")
	if _, err := os.Stat(pulledFile); os.IsNotExist(err) {
		t.Error("gitPullMain should have pulled the new file, but it does not exist")
	}
}

func TestGitPullMain_NonGitDirectory(t *testing.T) {
	tmp := t.TempDir()
	plainDir := filepath.Join(tmp, "not-a-repo")
	if err := os.MkdirAll(plainDir, 0755); err != nil {
		t.Fatal(err)
	}

	err := gitPullMain(plainDir)
	if err == nil {
		t.Error("gitPullMain on non-git directory should return error")
	}
	if !strings.Contains(err.Error(), "git checkout main") {
		t.Errorf("error should mention git checkout main, got: %v", err)
	}
}

func TestGitPullMain_NoRemote(t *testing.T) {
	tmp := t.TempDir()
	repoPath := filepath.Join(tmp, "local-only")

	if out, err := exec.Command("git", "init", repoPath).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	exec.Command("git", "-C", repoPath, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", repoPath, "config", "user.name", "Test").Run()

	initFile := filepath.Join(repoPath, "README.md")
	if err := os.WriteFile(initFile, []byte("# Local repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", repoPath, "add", ".").Run()
	exec.Command("git", "-C", repoPath, "commit", "-m", "init").Run()
	exec.Command("git", "-C", repoPath, "branch", "-M", "main").Run()

	err := gitPullMain(repoPath)
	if err == nil {
		t.Error("gitPullMain on repo with no remote should return error from pull")
	}
	if !strings.Contains(err.Error(), "git pull origin main") {
		t.Errorf("error should mention git pull origin main, got: %v", err)
	}
}

func TestGitPullMain_NonExistentPath(t *testing.T) {
	err := gitPullMain("/nonexistent/path/to/repo")
	if err == nil {
		t.Error("gitPullMain on nonexistent path should return error")
	}
}

// ---------------------------------------------------------------------------
// Tests for convertWithPandoc
// ---------------------------------------------------------------------------

func TestConvertWithPandoc_EmptyString(t *testing.T) {
	result := convertWithPandoc("")
	if result != "" {
		t.Errorf("convertWithPandoc(\"\") = %q, want empty string", result)
	}
}

func TestConvertWithPandoc_SimpleHTML(t *testing.T) {
	result := convertWithPandoc("<p>hello world</p>")

	if hasPandoc() {
		if result != "hello world" {
			t.Errorf("convertWithPandoc with pandoc: got %q, want %q", result, "hello world")
		}
	} else {
		if result != "<p>hello world</p>" {
			t.Errorf("convertWithPandoc without pandoc: got %q, want %q", result, "<p>hello world</p>")
		}
	}
}

func TestConvertWithPandoc_ComplexHTML(t *testing.T) {
	html := "<h2>Title</h2><ul><li>Item 1</li><li>Item 2</li></ul><p>A <a href=\"https://example.com\">link</a>.</p>"
	result := convertWithPandoc(html)

	if hasPandoc() {
		if strings.Contains(result, "<h2>") {
			t.Errorf("pandoc should convert <h2> tags, got: %s", result)
		}
		if strings.Contains(result, "<ul>") {
			t.Errorf("pandoc should convert <ul> tags, got: %s", result)
		}
		if !strings.Contains(result, "Item 1") {
			t.Errorf("converted output should contain 'Item 1', got: %s", result)
		}
		if !strings.Contains(result, "example.com") {
			t.Errorf("converted output should contain link URL, got: %s", result)
		}
	} else {
		if result != html {
			t.Errorf("without pandoc, expected raw HTML returned, got: %s", result)
		}
	}
}

func TestConvertWithPandoc_SpecialCharacters(t *testing.T) {
	html := "<p>Use &amp; and &lt;brackets&gt; carefully. Also \"quotes\" matter.</p>"
	result := convertWithPandoc(html)

	if hasPandoc() {
		if !strings.Contains(result, "&") {
			t.Errorf("pandoc should decode &amp; to &, got: %s", result)
		}
	} else {
		if result != html {
			t.Errorf("without pandoc, expected raw HTML, got: %s", result)
		}
	}
}

func TestConvertWithPandoc_CodeBlock(t *testing.T) {
	html := "<pre><code>func main() {\n\tfmt.Println(\"hello\")\n}</code></pre>"
	result := convertWithPandoc(html)

	if hasPandoc() {
		if !strings.Contains(result, "func main()") {
			t.Errorf("pandoc should preserve code content, got: %s", result)
		}
	} else {
		if result != html {
			t.Errorf("without pandoc, expected raw HTML, got: %s", result)
		}
	}
}

func TestConvertWithPandoc_PlainText(t *testing.T) {
	text := "This is just plain text with no HTML."
	result := convertWithPandoc(text)

	if !strings.Contains(result, "plain text") {
		t.Errorf("plain text should be preserved, got: %s", result)
	}
}

func TestHasPandoc(t *testing.T) {
	result := hasPandoc()
	t.Logf("hasPandoc() = %v", result)

	_, err := exec.LookPath("pandoc")
	expected := err == nil
	if result != expected {
		t.Errorf("hasPandoc() = %v, but exec.LookPath says %v", result, expected)
	}
}

// ---------------------------------------------------------------------------
// Tests for createDocsSymlink
// ---------------------------------------------------------------------------

func TestCreateDocsSymlink_ValidPaths(t *testing.T) {
	tmp := t.TempDir()
	wsPath := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(wsPath, 0755); err != nil {
		t.Fatal(err)
	}
	docsPath := filepath.Join(tmp, "docs")
	if err := os.MkdirAll(docsPath, 0755); err != nil {
		t.Fatal(err)
	}

	err := createDocsSymlink(wsPath, docsPath)
	if err != nil {
		t.Fatalf("createDocsSymlink should succeed with valid paths: %v", err)
	}

	linkPath := filepath.Join(wsPath, "docs")
	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("symlink should exist at %s: %v", linkPath, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected symlink at %s, got mode %v", linkPath, fi.Mode())
	}

	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("os.Readlink: %v", err)
	}
	if target != docsPath {
		t.Errorf("symlink target = %q, want %q", target, docsPath)
	}
}

func TestCreateDocsSymlink_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	wsPath := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(wsPath, 0755); err != nil {
		t.Fatal(err)
	}
	docsPath := filepath.Join(tmp, "docs")
	if err := os.MkdirAll(docsPath, 0755); err != nil {
		t.Fatal(err)
	}

	err := createDocsSymlink(wsPath, docsPath)
	if err != nil {
		t.Fatalf("first call should succeed: %v", err)
	}

	err = createDocsSymlink(wsPath, docsPath)
	if err != nil {
		t.Fatalf("second call should be idempotent: %v", err)
	}

	target, err := os.Readlink(filepath.Join(wsPath, "docs"))
	if err != nil {
		t.Fatalf("os.Readlink: %v", err)
	}
	if target != docsPath {
		t.Errorf("symlink target after idempotent call = %q, want %q", target, docsPath)
	}
}

func TestCreateDocsSymlink_NonExistentDocsPath(t *testing.T) {
	tmp := t.TempDir()
	wsPath := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(wsPath, 0755); err != nil {
		t.Fatal(err)
	}

	err := createDocsSymlink(wsPath, filepath.Join(tmp, "missing-docs-dir"))
	if err == nil {
		t.Error("should return error for non-existent docsPath")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(wsPath, "docs")); !os.IsNotExist(err) {
		t.Error("no symlink should be created when docsPath does not exist")
	}
}

func TestCreateDocsSymlink_NonExistentWsPath(t *testing.T) {
	tmp := t.TempDir()
	docsPath := filepath.Join(tmp, "docs")
	if err := os.MkdirAll(docsPath, 0755); err != nil {
		t.Fatal(err)
	}

	err := createDocsSymlink(filepath.Join(tmp, "missing-workspace"), docsPath)
	if err == nil {
		t.Error("should return error for non-existent wsPath")
	}
}

func TestCreateDocsSymlink_ExistingNonSymlink(t *testing.T) {
	tmp := t.TempDir()
	wsPath := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(wsPath, 0755); err != nil {
		t.Fatal(err)
	}
	docsPath := filepath.Join(tmp, "docs")
	if err := os.MkdirAll(docsPath, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a regular directory where the symlink would go
	if err := os.MkdirAll(filepath.Join(wsPath, "docs"), 0755); err != nil {
		t.Fatal(err)
	}

	err := createDocsSymlink(wsPath, docsPath)
	if err == nil {
		t.Error("should error when docs already exists as a regular directory")
	}
	if !strings.Contains(err.Error(), "not a symlink") {
		t.Errorf("error should mention 'not a symlink', got: %v", err)
	}
}

func TestCreateDocsSymlink_VerifySymlinkTarget(t *testing.T) {
	tmp := t.TempDir()
	wsPath := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(wsPath, 0755); err != nil {
		t.Fatal(err)
	}

	docsPath := filepath.Join(tmp, "shared-docs")
	if err := os.MkdirAll(docsPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docsPath, "test.txt"), []byte("hello docs"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := createDocsSymlink(wsPath, docsPath); err != nil {
		t.Fatalf("createDocsSymlink: %v", err)
	}

	// Verify we can read through the symlink
	data, err := os.ReadFile(filepath.Join(wsPath, "docs", "test.txt"))
	if err != nil {
		t.Fatalf("reading file through symlink: %v", err)
	}
	if string(data) != "hello docs" {
		t.Errorf("file content through symlink = %q, want %q", string(data), "hello docs")
	}

	target, err := os.Readlink(filepath.Join(wsPath, "docs"))
	if err != nil {
		t.Fatalf("os.Readlink: %v", err)
	}
	if target != docsPath {
		t.Errorf("os.Readlink = %q, want %q", target, docsPath)
	}
}
