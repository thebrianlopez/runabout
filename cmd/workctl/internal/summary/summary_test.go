package summary

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// captureStdout redirects os.Stdout during fn() and returns the captured output.
// Tests using this must NOT call t.Parallel() — pipe capture is not goroutine-safe.
func captureStdout(fn func()) string {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	out, _ := io.ReadAll(r)
	return string(out)
}

// ---------------------------------------------------------------------------
// GenerateJiraSummary
// ---------------------------------------------------------------------------

func TestGenerateJiraSummary_Empty(t *testing.T) {
	// Should not panic; log path (goes to stderr, not captured)
	GenerateJiraSummary(nil)
	GenerateJiraSummary([]models.Issue{})
}

func TestGenerateJiraSummary_WithIssues(t *testing.T) {
	issues := []models.Issue{
		{
			ProjectKey: "SR",
			Assignee:   "alice",
			IssueType:  "Story",
		},
		{
			ProjectKey: "ISRE",
			IssueType:  "Bug",
		},
	}
	issues[0].Fields.Status.Name = "Done"
	issues[1].Fields.Status.Name = "In Progress"

	out := captureStdout(func() {
		GenerateJiraSummary(issues)
	})

	if !strings.Contains(out, "JIRA SUMMARY") {
		t.Errorf("expected JIRA SUMMARY header in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Total Issues: 2") {
		t.Errorf("expected Total Issues: 2 in output, got:\n%s", out)
	}
	if !strings.Contains(out, "SR") {
		t.Errorf("expected project key SR in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Done") {
		t.Errorf("expected status Done in output, got:\n%s", out)
	}
}

func TestGenerateJiraSummary_FallbackLabels(t *testing.T) {
	// Empty fields should fall back to "Unknown" / "Unassigned"
	issues := []models.Issue{{Key: "SR-1"}}

	out := captureStdout(func() {
		GenerateJiraSummary(issues)
	})

	if !strings.Contains(out, "Unknown") {
		t.Errorf("expected Unknown fallback label in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Unassigned") {
		t.Errorf("expected Unassigned fallback label in output, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// GenerateConfluenceSummary
// ---------------------------------------------------------------------------

func TestGenerateConfluenceSummary_Empty(t *testing.T) {
	GenerateConfluenceSummary(nil)
	GenerateConfluenceSummary([]models.ConfluenceArticle{})
}

func TestGenerateConfluenceSummary_WithArticles(t *testing.T) {
	articles := []models.ConfluenceArticle{
		{Title: "Runbook A", SpaceName: "Engineering", Creator: "bob"},
		{Title: "Runbook B", SpaceKey: "ENG", LastEditor: "alice"},
	}

	out := captureStdout(func() {
		GenerateConfluenceSummary(articles)
	})

	if !strings.Contains(out, "CONFLUENCE SUMMARY") {
		t.Errorf("expected CONFLUENCE SUMMARY header in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Total Articles: 2") {
		t.Errorf("expected Total Articles: 2 in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Engineering") {
		t.Errorf("expected space name Engineering in output, got:\n%s", out)
	}
}

func TestGenerateConfluenceSummary_FallbackLabels(t *testing.T) {
	// Empty creator/editor/space should fall back to SpaceKey or "Unknown"
	articles := []models.ConfluenceArticle{{Title: "Doc"}}

	out := captureStdout(func() {
		GenerateConfluenceSummary(articles)
	})

	if !strings.Contains(out, "Unknown") {
		t.Errorf("expected Unknown fallback label in output, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// GenerateGitHubSummary
// ---------------------------------------------------------------------------

func TestGenerateGitHubSummary_Empty(t *testing.T) {
	GenerateGitHubSummary(nil)
	GenerateGitHubSummary([]models.GitHubActivity{})
}

func TestGenerateGitHubSummary_WithActivities(t *testing.T) {
	activities := []models.GitHubActivity{
		{EventType: "PullRequestEvent", Repository: "org/repo1", Public: true, Timestamp: time.Now()},
		{EventType: "PushEvent", Repository: "org/repo2", Public: false, Timestamp: time.Now()},
		{EventType: "PullRequestEvent", Repository: "org/repo1", Public: true, Timestamp: time.Now()},
	}

	out := captureStdout(func() {
		GenerateGitHubSummary(activities)
	})

	if !strings.Contains(out, "GITHUB SUMMARY") {
		t.Errorf("expected GITHUB SUMMARY header in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Total Activities: 3") {
		t.Errorf("expected Total Activities: 3 in output, got:\n%s", out)
	}
	if !strings.Contains(out, "PullRequestEvent") {
		t.Errorf("expected PullRequestEvent in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Public") {
		t.Errorf("expected Public visibility in output, got:\n%s", out)
	}
}

func TestGenerateGitHubSummary_FallbackLabels(t *testing.T) {
	// Empty EventType and Repository should fall back to "Unknown"
	activities := []models.GitHubActivity{{Public: true}}

	out := captureStdout(func() {
		GenerateGitHubSummary(activities)
	})

	if !strings.Contains(out, "Unknown") {
		t.Errorf("expected Unknown fallback label in output, got:\n%s", out)
	}
}
