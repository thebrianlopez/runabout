package api

import (
	"testing"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// ---------------------------------------------------------------------------
// Fixture-based tests — use testdata JSON files for realistic API responses
// ---------------------------------------------------------------------------

func TestLoadFixture(t *testing.T) {
	body := LoadFixture(t, TestFixture.JiraIssues)
	if len(body) == 0 {
		t.Fatal("JiraIssues fixture is empty")
	}
	body = LoadFixture(t, TestFixture.ConfluencePages)
	if len(body) == 0 {
		t.Fatal("ConfluencePages fixture is empty")
	}
	body = LoadFixture(t, TestFixture.GitHubEvents)
	if len(body) == 0 {
		t.Fatal("GitHubEvents fixture is empty")
	}
}

// ---------------------------------------------------------------------------
// Jira fixture tests
// ---------------------------------------------------------------------------

func TestGetAllAssignedIssues_Fixture(t *testing.T) {
	body := LoadFixture(t, TestFixture.JiraIssues)
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/search/jql": {
			method:     "GET",
			statusCode: 200,
			body:       body,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		StartDate: "2025-06-01",
		EndDate:   "2025-08-31",
	}

	issues, err := clients.GetAllAssignedIssues("user-abc", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(issues) != 3 {
		t.Fatalf("got %d issues, want 3", len(issues))
	}

	// Verify each issue has expected key
	wantKeys := []string{"SR-101", "SR-102", "INFRA-50"}
	for i, key := range wantKeys {
		if issues[i].Key != key {
			t.Errorf("issues[%d].Key = %q, want %q", i, issues[i].Key, key)
		}
	}

	// Verify first issue fields
	if issues[0].Fields.Summary != "Implement user authentication" {
		t.Errorf("issues[0].Summary = %q", issues[0].Fields.Summary)
	}
	if issues[0].IssueType != "Story" {
		t.Errorf("issues[0].IssueType = %q, want Story", issues[0].IssueType)
	}
	if issues[0].AssigneeAccountID != "user-abc" {
		t.Errorf("issues[0].AssigneeAccountID = %q, want user-abc", issues[0].AssigneeAccountID)
	}
	if issues[0].Fields.Resolved == "" {
		t.Error("issues[0].Fields.Resolved should be set (has resolutiondate)")
	}

	// Second issue has no resolutiondate
	if issues[1].Fields.Resolved != "" {
		t.Errorf("issues[1].Fields.Resolved = %q, want empty (no resolution)", issues[1].Fields.Resolved)
	}
	if issues[1].IssueType != "Bug" {
		t.Errorf("issues[1].IssueType = %q, want Bug", issues[1].IssueType)
	}
}

func TestGetAllIssuesByProjects_Fixture(t *testing.T) {
	body := LoadFixture(t, TestFixture.JiraIssues)
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/search/jql": {
			method:     "GET",
			statusCode: 200,
			body:       body,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		ProjectKeys: []string{"SR", "INFRA"},
		StartDate:   "2025-06-01",
		EndDate:     "2025-08-31",
	}

	issues, err := clients.GetAllIssuesByProjects(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(issues) != 3 {
		t.Fatalf("got %d issues, want 3", len(issues))
	}

	// Verify project keys extracted
	if issues[0].ProjectKey != "SR" {
		t.Errorf("issues[0].ProjectKey = %q, want SR", issues[0].ProjectKey)
	}
	if issues[2].ProjectKey != "INFRA" {
		t.Errorf("issues[2].ProjectKey = %q, want INFRA", issues[2].ProjectKey)
	}

	// Verify assignee fields
	if issues[0].Assignee != "Alice Smith" {
		t.Errorf("issues[0].Assignee = %q, want 'Alice Smith'", issues[0].Assignee)
	}
	if issues[0].AssigneeEmail != "alice@example.com" {
		t.Errorf("issues[0].AssigneeEmail = %q, want alice@example.com", issues[0].AssigneeEmail)
	}
}

// ---------------------------------------------------------------------------
// Confluence fixture tests
// ---------------------------------------------------------------------------

func TestGetAllConfluenceArticles_Fixture(t *testing.T) {
	body := LoadFixture(t, TestFixture.ConfluencePages)
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search": {
			method:     "GET",
			statusCode: 200,
			body:       body,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		StartDate: "2025-01-01",
		EndDate:   "2025-12-31",
	}

	articles, err := clients.GetAllConfluenceArticles("user-001", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(articles) != 2 {
		t.Fatalf("got %d articles, want 2", len(articles))
	}

	// First article
	if articles[0].Title != "Architecture Decision Records" {
		t.Errorf("articles[0].Title = %q", articles[0].Title)
	}
	if articles[0].CreatorAccountID != "user-001" {
		t.Errorf("articles[0].CreatorAccountID = %q, want user-001", articles[0].CreatorAccountID)
	}
	if articles[0].Body.Storage.Value != "<h1>ADR Template</h1><p>Status: Accepted</p>" {
		t.Errorf("articles[0].Body = %q", articles[0].Body.Storage.Value)
	}

	// Second article
	if articles[1].Title != "Runbook: Incident Response" {
		t.Errorf("articles[1].Title = %q", articles[1].Title)
	}
	if articles[1].CreatorAccountID != "user-003" {
		t.Errorf("articles[1].CreatorAccountID = %q, want user-003", articles[1].CreatorAccountID)
	}
}

func TestGetAllPagesBySpaces_Fixture(t *testing.T) {
	body := LoadFixture(t, TestFixture.ConfluencePages)
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search": {
			method:     "GET",
			statusCode: 200,
			body:       body,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		SpaceKeys: []string{"ENG", "OPS"},
		StartDate: "2025-01-01",
		EndDate:   "2025-12-31",
	}

	articles, err := clients.GetAllPagesBySpaces(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(articles) != 2 {
		t.Fatalf("got %d articles, want 2", len(articles))
	}

	// Verify metadata extraction
	if articles[0].Creator != "Alice Smith" {
		t.Errorf("articles[0].Creator = %q, want 'Alice Smith'", articles[0].Creator)
	}
	if articles[0].LastEditor != "Bob Jones" {
		t.Errorf("articles[0].LastEditor = %q, want 'Bob Jones'", articles[0].LastEditor)
	}
	if articles[0].LastModifiedDate != "2025-06-15T14:00:00.000Z" {
		t.Errorf("articles[0].LastModifiedDate = %q", articles[0].LastModifiedDate)
	}

	// Second article has no lastUpdated
	if articles[1].Creator != "Carol Davis" {
		t.Errorf("articles[1].Creator = %q, want 'Carol Davis'", articles[1].Creator)
	}
	if articles[1].LastEditor != "" {
		t.Errorf("articles[1].LastEditor = %q, want empty (no lastUpdated)", articles[1].LastEditor)
	}
}
