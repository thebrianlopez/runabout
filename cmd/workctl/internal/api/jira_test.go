package api

import (
	"strings"
	"testing"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

// ---------------------------------------------------------------------------
// GetIssueByKey
// ---------------------------------------------------------------------------

func TestGetIssueByKey_Success(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/issue/TEST-123": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"id": "10001",
				"key": "TEST-123",
				"fields": {
					"summary": "Fix authentication bug",
					"status": {"name": "In Progress"},
					"issuetype": {"name": "Bug"}
				},
				"renderedFields": {
					"description": "<p>Detailed description of the bug</p>"
				}
			}`,
		},
	})
	defer cleanup()

	info, err := clients.GetIssueByKey("TEST-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.Key != "TEST-123" {
		t.Errorf("Key = %q, want TEST-123", info.Key)
	}
	if info.Summary != "Fix authentication bug" {
		t.Errorf("Summary = %q, want 'Fix authentication bug'", info.Summary)
	}
	if info.Status != "In Progress" {
		t.Errorf("Status = %q, want 'In Progress'", info.Status)
	}
	if info.Type != "Bug" {
		t.Errorf("Type = %q, want 'Bug'", info.Type)
	}
	if !strings.Contains(info.URL, "TEST-123") {
		t.Errorf("URL = %q, should contain TEST-123", info.URL)
	}
	if info.Description != "<p>Detailed description of the bug</p>" {
		t.Errorf("Description = %q, want HTML description", info.Description)
	}
}

func TestGetIssueByKey_NilFields(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/issue/EMPTY-1": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"id": "10002",
				"key": "EMPTY-1"
			}`,
		},
	})
	defer cleanup()

	info, err := clients.GetIssueByKey("EMPTY-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.Key != "EMPTY-1" {
		t.Errorf("Key = %q, want EMPTY-1", info.Key)
	}
	// Nil fields should not panic — all strings default to empty
	if info.Summary != "" {
		t.Errorf("Summary = %q, want empty for nil fields", info.Summary)
	}
	if info.Status != "" {
		t.Errorf("Status = %q, want empty for nil status", info.Status)
	}
	if info.Type != "" {
		t.Errorf("Type = %q, want empty for nil issuetype", info.Type)
	}
	if info.Description != "" {
		t.Errorf("Description = %q, want empty for nil renderedFields", info.Description)
	}
}

func TestGetIssueByKey_APIError(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/issue/NOPE-404": {
			method:     "GET",
			statusCode: 404,
			body:       `{"errorMessages":["Issue does not exist"]}`,
		},
	})
	defer cleanup()

	_, err := clients.GetIssueByKey("NOPE-404")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	if !strings.Contains(err.Error(), "NOPE-404") {
		t.Errorf("error = %q, should mention issue key", err.Error())
	}
}

// ---------------------------------------------------------------------------
// GetAllAssignedIssues
// ---------------------------------------------------------------------------

func TestGetAllAssignedIssues_Success(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/search/jql": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"total": 2,
				"issues": [
					{
						"id": "1001",
						"key": "PROJ-1",
						"fields": {
							"summary": "First issue",
							"created": "2025-06-01T10:00:00.000+0000",
							"updated": "2025-06-15T12:00:00.000+0000",
							"resolutiondate": "2025-06-10T09:00:00.000+0000",
							"status": {"name": "Done"},
							"assignee": {"accountId": "user-abc"},
							"issuetype": {"name": "Story"}
						}
					},
					{
						"id": "1002",
						"key": "PROJ-2",
						"fields": {
							"summary": "Second issue",
							"created": "2025-07-01T08:00:00.000+0000",
							"updated": "2025-07-10T14:00:00.000+0000",
							"status": {"name": "In Progress"},
							"assignee": {"accountId": "user-abc"},
							"issuetype": {"name": "Bug"}
						}
					}
				]
			}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		StartDate: "2025-06-01",
		EndDate:   "2025-07-31",
	}

	issues, err := clients.GetAllAssignedIssues("user-abc", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2", len(issues))
	}

	// First issue
	if issues[0].Key != "PROJ-1" {
		t.Errorf("issues[0].Key = %q, want PROJ-1", issues[0].Key)
	}
	if issues[0].Fields.Summary != "First issue" {
		t.Errorf("issues[0].Fields.Summary = %q, want 'First issue'", issues[0].Fields.Summary)
	}
	if !strings.Contains(issues[0].URL, "PROJ-1") {
		t.Errorf("issues[0].URL = %q, should contain PROJ-1", issues[0].URL)
	}
	if issues[0].Fields.Status.Name != "Done" {
		t.Errorf("issues[0].Status = %q, want Done", issues[0].Fields.Status.Name)
	}
	if issues[0].AssigneeAccountID != "user-abc" {
		t.Errorf("issues[0].AssigneeAccountID = %q, want user-abc", issues[0].AssigneeAccountID)
	}
	if issues[0].IssueType != "Story" {
		t.Errorf("issues[0].IssueType = %q, want Story", issues[0].IssueType)
	}

	// Created field should be RFC3339 formatted
	if issues[0].Fields.Created == "" {
		t.Error("issues[0].Fields.Created should not be empty")
	}

	// Resolved field
	if issues[0].Fields.Resolved == "" {
		t.Error("issues[0].Fields.Resolved should not be empty")
	}

	// Second issue — no resolutiondate
	if issues[1].Key != "PROJ-2" {
		t.Errorf("issues[1].Key = %q, want PROJ-2", issues[1].Key)
	}
	if issues[1].Fields.Resolved != "" {
		t.Errorf("issues[1].Fields.Resolved = %q, want empty (no resolution)", issues[1].Fields.Resolved)
	}
}

func TestGetAllAssignedIssues_EmptyResults(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/search/jql": {
			method:     "GET",
			statusCode: 200,
			body:       `{"total": 0, "issues": []}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		StartDate: "2025-01-01",
		EndDate:   "2025-12-31",
	}

	issues, err := clients.GetAllAssignedIssues("user-xyz", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("got %d issues, want 0", len(issues))
	}
}

func TestGetAllAssignedIssues_NilOptionalFields(t *testing.T) {
	// Issues with nil assignee, status, resolutiondate, issuetype
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/search/jql": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"total": 1,
				"issues": [
					{
						"id": "2001",
						"key": "MIN-1",
						"fields": {
							"summary": "Minimal issue",
							"created": "2025-01-01T00:00:00.000+0000"
						}
					}
				]
			}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		StartDate: "2025-01-01",
		EndDate:   "2025-12-31",
	}

	issues, err := clients.GetAllAssignedIssues("user-abc", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(issues))
	}

	// Nil fields should not panic
	if issues[0].AssigneeAccountID != "" {
		t.Errorf("AssigneeAccountID = %q, want empty for nil assignee", issues[0].AssigneeAccountID)
	}
	if issues[0].IssueType != "" {
		t.Errorf("IssueType = %q, want empty for nil issuetype", issues[0].IssueType)
	}
	if issues[0].Fields.Status.Name != "" {
		t.Errorf("Status.Name = %q, want empty for nil status", issues[0].Fields.Status.Name)
	}
	if issues[0].Fields.Resolved != "" {
		t.Errorf("Resolved = %q, want empty for nil resolutiondate", issues[0].Fields.Resolved)
	}
}

func TestGetAllAssignedIssues_APIError(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/search/jql": {
			method:     "GET",
			statusCode: 500,
			body:       `{"errorMessages":["Internal error"]}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		StartDate: "2025-01-01",
		EndDate:   "2025-12-31",
	}

	_, err := clients.GetAllAssignedIssues("user-abc", cfg)
	if err == nil {
		t.Fatal("expected error for API failure")
	}
}

// ---------------------------------------------------------------------------
// GetAllIssuesByProjects
// ---------------------------------------------------------------------------

func TestGetAllIssuesByProjects_Success(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/search/jql": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"total": 2,
				"issues": [
					{
						"id": "3001",
						"key": "ALPHA-1",
						"fields": {
							"summary": "Alpha feature",
							"created": "2025-06-01T10:00:00.000+0000",
							"updated": "2025-06-15T12:00:00.000+0000",
							"status": {"name": "Done"},
							"project": {"key": "ALPHA"},
							"assignee": {
								"displayName": "Bob Jones",
								"emailAddress": "bob@example.com",
								"accountId": "bob-123"
							},
							"issuetype": {"name": "Task"}
						}
					},
					{
						"id": "3002",
						"key": "BETA-5",
						"fields": {
							"summary": "Beta bugfix",
							"created": "2025-07-01T08:00:00.000+0000",
							"updated": "2025-07-10T14:00:00.000+0000",
							"status": {"name": "In Progress"},
							"project": {"key": "BETA"},
							"issuetype": {"name": "Bug"}
						}
					}
				]
			}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		ProjectKeys: []string{"ALPHA", "BETA"},
		StartDate:   "2025-06-01",
		EndDate:     "2025-07-31",
	}

	issues, err := clients.GetAllIssuesByProjects(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2", len(issues))
	}

	// First issue — project and assignee fields
	if issues[0].ProjectKey != "ALPHA" {
		t.Errorf("issues[0].ProjectKey = %q, want ALPHA", issues[0].ProjectKey)
	}
	if issues[0].Assignee != "Bob Jones" {
		t.Errorf("issues[0].Assignee = %q, want 'Bob Jones'", issues[0].Assignee)
	}
	if issues[0].AssigneeEmail != "bob@example.com" {
		t.Errorf("issues[0].AssigneeEmail = %q, want 'bob@example.com'", issues[0].AssigneeEmail)
	}
	if issues[0].AssigneeAccountID != "bob-123" {
		t.Errorf("issues[0].AssigneeAccountID = %q, want 'bob-123'", issues[0].AssigneeAccountID)
	}
	if issues[0].IssueType != "Task" {
		t.Errorf("issues[0].IssueType = %q, want Task", issues[0].IssueType)
	}

	// Second issue — nil assignee
	if issues[1].ProjectKey != "BETA" {
		t.Errorf("issues[1].ProjectKey = %q, want BETA", issues[1].ProjectKey)
	}
	if issues[1].Assignee != "" {
		t.Errorf("issues[1].Assignee = %q, want empty for nil assignee", issues[1].Assignee)
	}
}

func TestGetAllIssuesByProjects_NilFields(t *testing.T) {
	// Issue with nil project, nil assignee
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/search/jql": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"total": 1,
				"issues": [
					{
						"id": "4001",
						"key": "X-1",
						"fields": {
							"summary": "Orphan issue",
							"created": "2025-01-01T00:00:00.000+0000"
						}
					}
				]
			}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		ProjectKeys: []string{"X"},
		StartDate:   "2025-01-01",
		EndDate:     "2025-12-31",
	}

	issues, err := clients.GetAllIssuesByProjects(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(issues))
	}

	// Nil project and assignee should not panic
	if issues[0].ProjectKey != "" {
		t.Errorf("ProjectKey = %q, want empty for nil project", issues[0].ProjectKey)
	}
	if issues[0].Assignee != "" {
		t.Errorf("Assignee = %q, want empty for nil assignee", issues[0].Assignee)
	}
}

func TestGetAllIssuesByProjects_APIError(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/search/jql": {
			method:     "GET",
			statusCode: 500,
			body:       `{"errorMessages":["Internal error"]}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		ProjectKeys: []string{"PROJ"},
		StartDate:   "2025-01-01",
		EndDate:     "2025-12-31",
	}

	_, err := clients.GetAllIssuesByProjects(cfg)
	if err == nil {
		t.Fatal("expected error for API failure")
	}
}
