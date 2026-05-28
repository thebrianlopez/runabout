package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const pandocTimeout = 15 * time.Second

// IssueInfo holds the Jira issue metadata needed for workspace scaffolding.
type IssueInfo struct {
	Key         string
	Summary     string
	Description string // HTML-rendered description from Jira
	URL         string
	Status      string
	Type        string
}

// InitOptions holds all parameters for workspace initialization.
type InitOptions struct {
	OrgPath       string
	WorkspaceDir  string
	GitCacheDir   string
	DocsPath      string // path to org docs directory for symlink (e.g. ~/code/grindr/docs)
	Repos         []string
	BranchPattern string
	Issue         *IssueInfo
	DryRun        bool
	Force         bool
	Verbose       bool
}

// StepStatus records the outcome of a single init step.
type StepStatus struct {
	Status    string `json:"status"` // "done", "skipped", "error"
	Timestamp string `json:"timestamp,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Manifest tracks completed steps for idempotent re-runs.
type Manifest struct {
	Key       string                `json:"key"`
	Summary   string                `json:"summary"`
	CreatedAt string                `json:"created_at"`
	Steps     map[string]StepStatus `json:"steps"`
}

// Result holds the output of a workspace init operation.
type Result struct {
	WorkspacePath string
	BranchName    string
	StepsRun      int
	StepsSkipped  int
}

// Init orchestrates workspace scaffolding with idempotent steps.
func Init(opts *InitOptions) (*Result, error) {
	folderName := buildFolderName(opts.Issue.Key, opts.Issue.Summary)
	wsPath := filepath.Join(opts.OrgPath, opts.WorkspaceDir, folderName)
	branchName := resolveBranchName(opts.BranchPattern, opts.Issue.Key)
	cachePath := filepath.Join(opts.OrgPath, opts.GitCacheDir)

	totalSteps := 4 + len(opts.Repos)*3 // mkdir + issue.md + (clone + pull + branch) per repo + docs_symlink + manifest
	result := &Result{
		WorkspacePath: wsPath,
		BranchName:    branchName,
	}

	if opts.DryRun {
		fmt.Printf("[dry-run] Would create directory: %s\n", wsPath)
		fmt.Printf("[dry-run] Would write: %s/%s.md (with pandoc-converted description)\n", wsPath, opts.Issue.Key)
		for _, repo := range opts.Repos {
			fmt.Printf("[dry-run] Would clone %s from cache\n", repo)
			fmt.Printf("[dry-run] Would pull origin main in %s\n", repo)
			fmt.Printf("[dry-run] Would create branch %s in %s\n", branchName, repo)
		}
		if opts.DocsPath != "" {
			fmt.Printf("[dry-run] Would symlink docs → %s\n", opts.DocsPath)
		}
		fmt.Printf("[dry-run] Would write: %s/.workspace.json\n", wsPath)
		return result, nil
	}

	// Load or create manifest
	manifest := loadManifest(wsPath)
	if manifest == nil {
		manifest = &Manifest{
			Key:       opts.Issue.Key,
			Summary:   opts.Issue.Summary,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			Steps:     make(map[string]StepStatus),
		}
	}

	stepNum := 0

	// Step: Create workspace directory
	stepNum++
	stepKey := "mkdir"
	if opts.Force || !isStepDone(manifest, stepKey) {
		printStep(stepNum, totalSteps, "Create workspace directory")
		start := time.Now()
		if err := os.MkdirAll(wsPath, 0o755); err != nil {
			markStep(manifest, stepKey, "error", err.Error())
			saveManifest(wsPath, manifest)
			return nil, fmt.Errorf("creating workspace directory: %w", err)
		}
		markStep(manifest, stepKey, "done", "")
		printDone(start)
		result.StepsRun++
	} else {
		stepNum = printSkip(stepNum, totalSteps, "Create workspace directory")
		result.StepsSkipped++
	}

	// Step: Write issue markdown
	stepNum++
	stepKey = "issue_md"
	if opts.Force || !isStepDone(manifest, stepKey) {
		printStep(stepNum, totalSteps, fmt.Sprintf("Write %s.md", opts.Issue.Key))
		start := time.Now()
		if err := writeIssueMD(wsPath, opts.Issue); err != nil {
			markStep(manifest, stepKey, "error", err.Error())
			saveManifest(wsPath, manifest)
			return nil, fmt.Errorf("writing issue markdown: %w", err)
		}
		markStep(manifest, stepKey, "done", "")
		printDone(start)
		result.StepsRun++
	} else {
		stepNum = printSkip(stepNum, totalSteps, fmt.Sprintf("Write %s.md", opts.Issue.Key))
		result.StepsSkipped++
	}

	// Steps: Clone repos and create branches
	for _, repo := range opts.Repos {
		// Clone step
		stepNum++
		cloneKey := fmt.Sprintf("clone_%s", repo)
		repoPath := filepath.Join(wsPath, repo)
		if opts.Force || !isStepDone(manifest, cloneKey) {
			printStep(stepNum, totalSteps, fmt.Sprintf("Clone %s from cache", repo))
			start := time.Now()
			if err := gitCloneFromCache(cachePath, repo, repoPath); err != nil {
				markStep(manifest, cloneKey, "error", err.Error())
				saveManifest(wsPath, manifest)
				return nil, fmt.Errorf("cloning %s: %w", repo, err)
			}
			markStep(manifest, cloneKey, "done", "")
			printDone(start)
			result.StepsRun++
		} else {
			stepNum = printSkip(stepNum, totalSteps, fmt.Sprintf("Clone %s from cache", repo))
			result.StepsSkipped++
		}

		// Pull step (non-fatal: warn and continue on failure)
		stepNum++
		pullKey := fmt.Sprintf("pull_%s", repo)
		if opts.Force || !isStepDone(manifest, pullKey) {
			printStep(stepNum, totalSteps, fmt.Sprintf("Pull origin main in %s", repo))
			start := time.Now()
			if err := gitPullMain(repoPath); err != nil {
				markStep(manifest, pullKey, "error", err.Error())
				fmt.Printf("warning: %v (branch created from cached state)\n", err)
			} else {
				markStep(manifest, pullKey, "done", "")
				printDone(start)
			}
			result.StepsRun++
		} else {
			stepNum = printSkip(stepNum, totalSteps, fmt.Sprintf("Pull origin main in %s", repo))
			result.StepsSkipped++
		}

		// Branch step
		stepNum++
		branchKey := fmt.Sprintf("branch_%s", repo)
		if opts.Force || !isStepDone(manifest, branchKey) {
			printStep(stepNum, totalSteps, fmt.Sprintf("Create branch %s in %s", branchName, repo))
			start := time.Now()
			if err := gitCreateBranch(repoPath, branchName); err != nil {
				markStep(manifest, branchKey, "error", err.Error())
				saveManifest(wsPath, manifest)
				return nil, fmt.Errorf("creating branch in %s: %w", repo, err)
			}
			markStep(manifest, branchKey, "done", "")
			printDone(start)
			result.StepsRun++
		} else {
			stepNum = printSkip(stepNum, totalSteps, fmt.Sprintf("Create branch %s in %s", branchName, repo))
			result.StepsSkipped++
		}
	}

	// Step: Create docs symlink (non-fatal: warn and continue on failure)
	stepNum++
	stepKey = "docs_symlink"
	if opts.DocsPath != "" {
		if opts.Force || !isStepDone(manifest, stepKey) {
			printStep(stepNum, totalSteps, "Create docs symlink")
			start := time.Now()
			if err := createDocsSymlink(wsPath, opts.DocsPath); err != nil {
				markStep(manifest, stepKey, "error", err.Error())
				fmt.Printf("warning: %v (skipping docs symlink)\n", err)
			} else {
				markStep(manifest, stepKey, "done", "")
				printDone(start)
			}
			result.StepsRun++
		} else {
			stepNum = printSkip(stepNum, totalSteps, "Create docs symlink")
			result.StepsSkipped++
		}
	} else {
		stepNum = printSkip(stepNum, totalSteps, "Create docs symlink (no docs path)")
		result.StepsSkipped++
	}

	// Final step: Write manifest
	stepNum++
	printStep(stepNum, totalSteps, "Write .workspace.json")
	start := time.Now()
	saveManifest(wsPath, manifest)
	printDone(start)
	result.StepsRun++

	return result, nil
}

// buildFolderName creates a filesystem-safe folder name: KEY_sanitized_summary
func buildFolderName(key, summary string) string {
	if summary == "" {
		return key
	}

	// Lowercase and replace non-alphanumeric with underscores
	s := strings.ToLower(summary)
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevUnderscore = false
		} else {
			if !prevUnderscore && b.Len() > 0 {
				b.WriteRune('_')
				prevUnderscore = true
			}
		}
	}

	sanitized := strings.TrimRight(b.String(), "_")
	if sanitized == "" {
		return key
	}

	// Truncate to keep total path reasonable (key + _ + summary ≤ 80 chars)
	maxSummaryLen := 80 - len(key) - 1
	if maxSummaryLen > 0 && len(sanitized) > maxSummaryLen {
		sanitized = sanitized[:maxSummaryLen]
		sanitized = strings.TrimRight(sanitized, "_")
	}

	return key + "_" + sanitized
}

// resolveBranchName applies the branch pattern, substituting {key}.
func resolveBranchName(pattern, key string) string {
	return strings.ReplaceAll(pattern, "{key}", key)
}

// findBareRepo locates a bare git repo in the cache directory.
// Checks name.git first, then name.
func findBareRepo(cachePath, name string) (string, error) {
	gitPath := filepath.Join(cachePath, name+".git")
	if info, err := os.Stat(gitPath); err == nil && info.IsDir() {
		return gitPath, nil
	}

	plainPath := filepath.Join(cachePath, name)
	if info, err := os.Stat(plainPath); err == nil && info.IsDir() {
		return plainPath, nil
	}

	return "", fmt.Errorf("bare repo not found in cache: %s (tried %s.git and %s)", name, name, name)
}

// gitCloneFromCache clones a repo using a bare cache reference.
func gitCloneFromCache(cachePath, repoName, destPath string) error {
	// Check if dest already exists (idempotency)
	if _, err := os.Stat(destPath); err == nil {
		return nil
	}

	bareRef, err := findBareRepo(cachePath, repoName)
	if err != nil {
		return err
	}

	// Read the upstream URL from the bare repo's config
	upstream, err := gitGetOriginURL(bareRef)
	if err != nil {
		return fmt.Errorf("reading origin URL from bare repo: %w", err)
	}

	// Clone using the bare repo as a reference
	cmd := exec.Command("git", "clone", "--reference-if-able", bareRef, "file://"+bareRef, destPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Set origin to the real upstream
	if upstream != "" {
		cmd = exec.Command("git", "-C", destPath, "remote", "set-url", "origin", upstream)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git remote set-url: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}

	return nil
}

// gitGetOriginURL reads the origin remote URL from a git repo.
func gitGetOriginURL(repoPath string) (string, error) {
	cmd := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitPullMain ensures the repo is on main and up-to-date before branching.
func gitPullMain(repoPath string) error {
	// Checkout main
	cmd := exec.Command("git", "-C", repoPath, "checkout", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout main: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Pull latest from origin
	cmd = exec.Command("git", "-C", repoPath, "pull", "origin", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git pull origin main: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return nil
}

// gitCreateBranch checks out an existing branch or creates a new one.
func gitCreateBranch(repoPath, branch string) error {
	// Check if branch exists locally
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", branch)
	if err := cmd.Run(); err == nil {
		// Branch exists locally, just check it out
		cmd = exec.Command("git", "-C", repoPath, "checkout", branch)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git checkout %s: %s: %w", branch, strings.TrimSpace(string(out)), err)
		}
		return nil
	}

	// Check if branch exists on remote
	cmd = exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "origin/"+branch)
	if err := cmd.Run(); err == nil {
		// Remote branch exists, create local tracking branch
		cmd = exec.Command("git", "-C", repoPath, "checkout", "-b", branch, "origin/"+branch)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git checkout -b %s: %s: %w", branch, strings.TrimSpace(string(out)), err)
		}
		return nil
	}

	// Create new branch
	cmd = exec.Command("git", "-C", repoPath, "checkout", "-b", branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout -b %s: %s: %w", branch, strings.TrimSpace(string(out)), err)
	}

	return nil
}

// convertWithPandoc converts HTML content to GitHub-flavored markdown using pandoc.
// Falls back to raw content if pandoc is not available or times out.
func convertWithPandoc(html string) string {
	if html == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), pandocTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pandoc", "-f", "html", "-t", "gfm")
	cmd.Stdin = bytes.NewReader([]byte(html))
	out, err := cmd.Output()
	if err != nil {
		return html
	}
	return strings.TrimSpace(string(out))
}

// hasPandoc returns true if pandoc is available on the system.
func hasPandoc() bool {
	_, err := exec.LookPath("pandoc")
	return err == nil
}

// createDocsSymlink creates a symbolic link to the org docs directory.
func createDocsSymlink(wsPath, docsPath string) error {
	// Verify the docs source directory exists
	if _, err := os.Stat(docsPath); os.IsNotExist(err) {
		return fmt.Errorf("docs directory not found: %s", docsPath)
	}

	linkPath := filepath.Join(wsPath, "docs")

	// Check if symlink already exists (idempotent)
	if fi, err := os.Lstat(linkPath); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		return fmt.Errorf("docs path already exists and is not a symlink: %s", linkPath)
	}

	return os.Symlink(docsPath, linkPath)
}

// writeIssueMD writes a markdown file with issue metadata and description.
func writeIssueMD(wsPath string, issue *IssueInfo) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", issue.Key)
	fmt.Fprintf(&b, "**Summary:** %s\n\n", issue.Summary)
	fmt.Fprintf(&b, "**Status:** %s | **Type:** %s\n\n", issue.Status, issue.Type)
	fmt.Fprintf(&b, "%s\n", issue.URL)

	if issue.Description != "" {
		b.WriteString("\n---\n\n")
		b.WriteString(convertWithPandoc(issue.Description))
		b.WriteString("\n")
	}

	return os.WriteFile(filepath.Join(wsPath, issue.Key+".md"), []byte(b.String()), 0o600)
}

// loadManifest reads the .workspace.json manifest from a workspace directory.
func loadManifest(wsPath string) *Manifest {
	data, err := os.ReadFile(filepath.Join(wsPath, ".workspace.json"))
	if err != nil {
		return nil
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return &m
}

// saveManifest writes the .workspace.json manifest to a workspace directory.
func saveManifest(wsPath string, m *Manifest) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(wsPath, ".workspace.json"), data, 0o600)
}

func isStepDone(m *Manifest, key string) bool {
	s, ok := m.Steps[key]
	return ok && s.Status == "done"
}

func markStep(m *Manifest, key, status, errMsg string) {
	m.Steps[key] = StepStatus{
		Status:    status,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Error:     errMsg,
	}
}

func printStep(num, total int, desc string) {
	fmt.Printf("[%d/%d] %s... ", num, total, desc)
}

func printDone(start time.Time) {
	fmt.Printf("done (%.1fs)\n", time.Since(start).Seconds())
}

func printSkip(num, total int, desc string) int {
	fmt.Printf("[%d/%d] %s... skipped\n", num, total, desc)
	return num
}

// isValidJiraKey validates a Jira issue key format (PROJECT-123).
var jiraKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]+-\d+$`)

func IsValidJiraKey(key string) bool {
	return jiraKeyPattern.MatchString(key)
}
