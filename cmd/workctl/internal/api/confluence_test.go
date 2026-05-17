package api

import (
	"context"
	"strings"
	"testing"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// ---------------------------------------------------------------------------
// HydratePageMetadata
// ---------------------------------------------------------------------------

func TestHydratePageMetadata_Success(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/content/12345": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"id": "12345",
				"title": "Test Page",
				"history": {
					"createdBy": {
						"accountId": "creator-abc",
						"displayName": "Creator User",
						"email": "creator@example.com"
					},
					"createdDate": "2025-06-01T10:00:00.000Z",
					"lastUpdated": {
						"by": {
							"accountId": "editor-xyz",
							"displayName": "Editor User"
						},
						"when": "2025-06-15T14:00:00.000Z"
					}
				},
				"space": {
					"key": "ENG",
					"name": "Engineering"
				}
			}`,
		},
	})
	defer cleanup()

	content, err := clients.HydratePageMetadata(context.Background(), "12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if content == nil {
		t.Fatal("expected non-nil content")
	}
	if content.ID != "12345" {
		t.Errorf("ID = %q, want 12345", content.ID)
	}
	if content.History == nil {
		t.Fatal("expected non-nil History")
	}
	if content.History.CreatedBy == nil {
		t.Fatal("expected non-nil CreatedBy")
	}
	if content.History.CreatedBy.AccountID != "creator-abc" {
		t.Errorf("CreatedBy.AccountID = %q, want creator-abc", content.History.CreatedBy.AccountID)
	}
	if content.History.CreatedBy.DisplayName != "Creator User" {
		t.Errorf("CreatedBy.DisplayName = %q, want 'Creator User'", content.History.CreatedBy.DisplayName)
	}
}

func TestHydratePageMetadata_EmptyPageID(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{})
	defer cleanup()

	_, err := clients.HydratePageMetadata(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty page ID")
	}
	if !strings.Contains(err.Error(), "page ID is required") {
		t.Errorf("error = %q, should mention 'page ID is required'", err.Error())
	}
}

func TestHydratePageMetadata_APIError(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/content/99999": {
			method:     "GET",
			statusCode: 404,
			body:       `{"message": "Page not found"}`,
		},
	})
	defer cleanup()

	_, err := clients.HydratePageMetadata(context.Background(), "99999")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	if !strings.Contains(err.Error(), "99999") {
		t.Errorf("error = %q, should mention page ID", err.Error())
	}
}

// ---------------------------------------------------------------------------
// GetAllConfluenceArticles
// ---------------------------------------------------------------------------

func TestGetAllConfluenceArticles_Success(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"size": 2,
				"results": [
					{
						"content": {
							"id": "100",
							"title": "Architecture Overview",
							"space": {"key": "ENG", "name": "Engineering"},
							"body": {
								"storage": {"value": "<p>Architecture details</p>"}
							},
							"history": {
								"createdBy": {
									"accountId": "user-001",
									"displayName": "Alice"
								},
								"createdDate": "2025-05-01T10:00:00.000Z"
							}
						}
					},
					{
						"content": {
							"id": "200",
							"title": "API Guide",
							"space": {"key": "DEV", "name": "Development"},
							"history": {
								"createdDate": "2025-06-01T09:00:00.000Z"
							}
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

	articles, err := clients.GetAllConfluenceArticles("user-001", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(articles) != 2 {
		t.Fatalf("got %d articles, want 2", len(articles))
	}

	// First article — full fields
	if articles[0].ID != "100" {
		t.Errorf("articles[0].ID = %q, want 100", articles[0].ID)
	}
	if articles[0].Title != "Architecture Overview" {
		t.Errorf("articles[0].Title = %q, want 'Architecture Overview'", articles[0].Title)
	}
	if !strings.Contains(articles[0].URL, "ENG") {
		t.Errorf("articles[0].URL = %q, should contain space key ENG", articles[0].URL)
	}
	if articles[0].Body.Storage.Value != "<p>Architecture details</p>" {
		t.Errorf("articles[0].Body = %q, want body content", articles[0].Body.Storage.Value)
	}
	if articles[0].CreatedBy.AccountID != "user-001" {
		t.Errorf("articles[0].CreatedBy.AccountID = %q, want user-001", articles[0].CreatedBy.AccountID)
	}
	if articles[0].CreatorAccountID != "user-001" {
		t.Errorf("articles[0].CreatorAccountID = %q, want user-001", articles[0].CreatorAccountID)
	}
	if articles[0].CreatedDate != "2025-05-01T10:00:00.000Z" {
		t.Errorf("articles[0].CreatedDate = %q", articles[0].CreatedDate)
	}

	// Second article — no body, no creator
	if articles[1].ID != "200" {
		t.Errorf("articles[1].ID = %q, want 200", articles[1].ID)
	}
	if articles[1].Body.Storage.Value != "" {
		t.Errorf("articles[1].Body should be empty, got %q", articles[1].Body.Storage.Value)
	}
}

func TestGetAllConfluenceArticles_NilContent(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"size": 1,
				"results": [
					{
						"content": null
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

	articles, err := clients.GetAllConfluenceArticles("user-001", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have 1 article slot but with empty fields (nil Content is skipped)
	if len(articles) != 1 {
		t.Fatalf("got %d articles, want 1", len(articles))
	}
	if articles[0].ID != "" {
		t.Errorf("articles[0].ID = %q, want empty for nil content", articles[0].ID)
	}
}

func TestGetAllConfluenceArticles_DefaultContentType(t *testing.T) {
	// When ConfluenceType is empty, should default to "page"
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search": {
			method:     "GET",
			statusCode: 200,
			body:       `{"size": 0, "results": []}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		ConfluenceType: "", // empty → default "page"
		StartDate:      "2025-01-01",
		EndDate:        "2025-12-31",
	}

	_, err := clients.GetAllConfluenceArticles("user-001", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No panic, no error — content type defaulted to "page"
}

func TestGetAllConfluenceArticles_CustomContentType(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search": {
			method:     "GET",
			statusCode: 200,
			body:       `{"size": 0, "results": []}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		ConfluenceType: "blogpost",
		StartDate:      "2025-01-01",
		EndDate:        "2025-12-31",
	}

	_, err := clients.GetAllConfluenceArticles("user-001", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetAllConfluenceArticles_APIError(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search": {
			method:     "GET",
			statusCode: 500,
			body:       `{"message": "Internal Server Error"}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		StartDate: "2025-01-01",
		EndDate:   "2025-12-31",
	}

	_, err := clients.GetAllConfluenceArticles("user-001", cfg)
	if err == nil {
		t.Fatal("expected error for API failure")
	}
}

// ---------------------------------------------------------------------------
// GetAllPagesBySpaces
// ---------------------------------------------------------------------------

func TestGetAllPagesBySpaces_Success(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"size": 1,
				"results": [
					{
						"content": {
							"id": "500",
							"title": "Runbook: Incident Response",
							"space": {"key": "OPS", "name": "Operations"},
							"body": {
								"storage": {"value": "<p>Step 1: Triage</p>"}
							},
							"history": {
								"createdBy": {
									"accountId": "creator-001",
									"displayName": "Jane Doe",
									"email": "jane@example.com"
								},
								"createdDate": "2025-03-01T10:00:00.000Z",
								"lastUpdated": {
									"by": {
										"accountId": "editor-002",
										"displayName": "John Smith"
									},
									"when": "2025-06-01T16:00:00.000Z"
								}
							}
						}
					}
				]
			}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		SpaceKeys: []string{"OPS"},
		StartDate: "2025-01-01",
		EndDate:   "2025-12-31",
	}

	articles, err := clients.GetAllPagesBySpaces(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(articles) != 1 {
		t.Fatalf("got %d articles, want 1", len(articles))
	}

	a := articles[0]
	if a.ID != "500" {
		t.Errorf("ID = %q, want 500", a.ID)
	}
	if a.SpaceKey != "OPS" {
		t.Errorf("SpaceKey = %q, want OPS", a.SpaceKey)
	}
	if a.SpaceName != "Operations" {
		t.Errorf("SpaceName = %q, want Operations", a.SpaceName)
	}
	if a.Creator != "Jane Doe" {
		t.Errorf("Creator = %q, want 'Jane Doe'", a.Creator)
	}
	if a.CreatorEmail != "jane@example.com" {
		t.Errorf("CreatorEmail = %q, want 'jane@example.com'", a.CreatorEmail)
	}
	if a.CreatorAccountID != "creator-001" {
		t.Errorf("CreatorAccountID = %q, want creator-001", a.CreatorAccountID)
	}
	if a.LastEditor != "John Smith" {
		t.Errorf("LastEditor = %q, want 'John Smith'", a.LastEditor)
	}
	if a.LastModifiedDate != "2025-06-01T16:00:00.000Z" {
		t.Errorf("LastModifiedDate = %q, want 2025-06-01T16:00:00.000Z", a.LastModifiedDate)
	}
	if !strings.Contains(a.URL, "OPS") {
		t.Errorf("URL = %q, should contain space key OPS", a.URL)
	}
	if a.Body.Storage.Value != "<p>Step 1: Triage</p>" {
		t.Errorf("Body = %q, want body content", a.Body.Storage.Value)
	}
}

func TestGetAllPagesBySpaces_NilContent(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"size": 1,
				"results": [{"content": null}]
			}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		SpaceKeys: []string{"ENG"},
		StartDate: "2025-01-01",
		EndDate:   "2025-12-31",
	}

	articles, err := clients.GetAllPagesBySpaces(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(articles) != 1 {
		t.Fatalf("got %d articles, want 1", len(articles))
	}
	// Nil content → fields stay empty
	if articles[0].ID != "" {
		t.Errorf("ID = %q, want empty for nil content", articles[0].ID)
	}
}

func TestGetAllPagesBySpaces_WithHydration(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"size": 1,
				"results": [
					{
						"content": {
							"id": "600",
							"title": "Hydration Test",
							"space": {"key": "ENG"},
							"history": {
								"createdDate": "2025-01-01T10:00:00.000Z"
							}
						}
					}
				]
			}`,
		},
		"/wiki/rest/api/content/600": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"id": "600",
				"title": "Hydration Test",
				"history": {
					"createdBy": {
						"accountId": "hydrated-creator",
						"displayName": "Hydrated Creator",
						"email": "hydrated@example.com"
					},
					"lastUpdated": {
						"by": {
							"accountId": "hydrated-editor",
							"displayName": "Hydrated Editor"
						}
					}
				}
			}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		SpaceKeys:         []string{"ENG"},
		StartDate:         "2025-01-01",
		EndDate:           "2025-12-31",
		ConfluenceHydrate: true,
	}

	articles, err := clients.GetAllPagesBySpaces(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(articles) != 1 {
		t.Fatalf("got %d articles, want 1", len(articles))
	}

	a := articles[0]
	// After hydration, creator and editor should be updated
	if a.Creator != "Hydrated Creator" {
		t.Errorf("Creator = %q, want 'Hydrated Creator' (from hydration)", a.Creator)
	}
	if a.CreatorAccountID != "hydrated-creator" {
		t.Errorf("CreatorAccountID = %q, want hydrated-creator", a.CreatorAccountID)
	}
	if a.LastEditor != "Hydrated Editor" {
		t.Errorf("LastEditor = %q, want 'Hydrated Editor' (from hydration)", a.LastEditor)
	}
	if a.LastEditorAccountID != "hydrated-editor" {
		t.Errorf("LastEditorAccountID = %q, want hydrated-editor", a.LastEditorAccountID)
	}
}

func TestGetAllPagesBySpaces_HydrationError(t *testing.T) {
	// Hydration fails but original data is preserved (graceful degradation)
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"size": 1,
				"results": [
					{
						"content": {
							"id": "700",
							"title": "Hydration Fail Test",
							"space": {"key": "ENG"},
							"history": {
								"createdBy": {
									"accountId": "original-creator",
									"displayName": "Original Creator"
								},
								"createdDate": "2025-01-01T10:00:00.000Z"
							}
						}
					}
				]
			}`,
		},
		"/wiki/rest/api/content/700": {
			method:     "GET",
			statusCode: 500,
			body:       `{"message": "Internal Server Error"}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		SpaceKeys:         []string{"ENG"},
		StartDate:         "2025-01-01",
		EndDate:           "2025-12-31",
		ConfluenceHydrate: true,
	}

	articles, err := clients.GetAllPagesBySpaces(cfg)
	if err != nil {
		t.Fatalf("unexpected error (hydration errors should not propagate): %v", err)
	}

	if len(articles) != 1 {
		t.Fatalf("got %d articles, want 1", len(articles))
	}

	// Original data should be preserved despite hydration failure
	if articles[0].CreatorAccountID != "original-creator" {
		t.Errorf("CreatorAccountID = %q, want original-creator (preserved despite hydration failure)", articles[0].CreatorAccountID)
	}
}

func TestGetAllPagesBySpaces_APIError(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search": {
			method:     "GET",
			statusCode: 500,
			body:       `{"message": "Internal Server Error"}`,
		},
	})
	defer cleanup()

	cfg := &models.QueryConfig{
		SpaceKeys: []string{"ENG"},
		StartDate: "2025-01-01",
		EndDate:   "2025-12-31",
	}

	_, err := clients.GetAllPagesBySpaces(cfg)
	if err == nil {
		t.Fatal("expected error for API failure")
	}
}
