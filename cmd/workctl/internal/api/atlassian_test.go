package api

import (
	"testing"
)

// ---------------------------------------------------------------------------
// GetJiraUserAccountID
// ---------------------------------------------------------------------------

func TestGetJiraUserAccountID_Success(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/user/search": {
			method:     "GET",
			statusCode: 200,
			body: `[
				{
					"accountId": "557058:abc123",
					"displayName": "Alice Smith",
					"emailAddress": "alice@example.com",
					"active": true
				}
			]`,
		},
	})
	defer cleanup()

	accountID, err := clients.GetJiraUserAccountID("alice@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if accountID != "557058:abc123" {
		t.Errorf("accountID = %q, want %q", accountID, "557058:abc123")
	}
}

func TestGetJiraUserAccountID_MultipleUsers(t *testing.T) {
	// Should return the first user's account ID
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/user/search": {
			method:     "GET",
			statusCode: 200,
			body: `[
				{"accountId": "first-user", "displayName": "First"},
				{"accountId": "second-user", "displayName": "Second"}
			]`,
		},
	})
	defer cleanup()

	accountID, err := clients.GetJiraUserAccountID("user@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if accountID != "first-user" {
		t.Errorf("accountID = %q, want first-user", accountID)
	}
}

func TestGetJiraUserAccountID_NoUsersFound(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/user/search": {
			method:     "GET",
			statusCode: 200,
			body:       `[]`,
		},
	})
	defer cleanup()

	_, err := clients.GetJiraUserAccountID("nobody@example.com")
	if err == nil {
		t.Fatal("expected error for no users found")
	}
	if got := err.Error(); got != "no Jira user found with the given email" {
		t.Errorf("error = %q, want 'no Jira user found with the given email'", got)
	}
}

func TestGetJiraUserAccountID_APIError(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/rest/api/3/user/search": {
			method:     "GET",
			statusCode: 500,
			body:       `{"message": "Internal Server Error"}`,
		},
	})
	defer cleanup()

	_, err := clients.GetJiraUserAccountID("alice@example.com")
	if err == nil {
		t.Fatal("expected error for API failure")
	}
}

// ---------------------------------------------------------------------------
// GetConfluenceUserAccountID
// ---------------------------------------------------------------------------

func TestGetConfluenceUserAccountID_Success(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search/user": {
			method:     "GET",
			statusCode: 200,
			body: `{
				"size": 1,
				"results": [
					{
						"user": {
							"accountId": "conf-user-123",
							"displayName": "Alice Smith"
						}
					}
				]
			}`,
		},
	})
	defer cleanup()

	accountID, err := clients.GetConfluenceUserAccountID("alice.smith@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if accountID != "conf-user-123" {
		t.Errorf("accountID = %q, want %q", accountID, "conf-user-123")
	}
}

func TestGetConfluenceUserAccountID_NoResults(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search/user": {
			method:     "GET",
			statusCode: 200,
			body:       `{"size": 0, "results": []}`,
		},
	})
	defer cleanup()

	_, err := clients.GetConfluenceUserAccountID("nobody@example.com")
	if err == nil {
		t.Fatal("expected error for no users found")
	}
	if got := err.Error(); got != "no Confluence user found with the given email" {
		t.Errorf("error = %q, want 'no Confluence user found with the given email'", got)
	}
}

func TestGetConfluenceUserAccountID_NilUser(t *testing.T) {
	// Result has entries but user field is nil
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search/user": {
			method:     "GET",
			statusCode: 200,
			body:       `{"size": 1, "results": [{"user": null}]}`,
		},
	})
	defer cleanup()

	_, err := clients.GetConfluenceUserAccountID("user@example.com")
	if err == nil {
		t.Fatal("expected error when user is nil")
	}
}

func TestGetConfluenceUserAccountID_APIError(t *testing.T) {
	clients, cleanup := newTestAtlassianClients(t, map[string]testRoute{
		"/wiki/rest/api/search/user": {
			method:     "GET",
			statusCode: 500,
			body:       `{"message": "Internal Server Error"}`,
		},
	})
	defer cleanup()

	_, err := clients.GetConfluenceUserAccountID("alice@example.com")
	if err == nil {
		t.Fatal("expected error for API failure")
	}
}

// ---------------------------------------------------------------------------
// EscapeCQLString
// ---------------------------------------------------------------------------

func TestEscapeCQLString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no quotes", "alice smith", "alice smith"},
		{"embedded quote", `alice "admin" smith`, `alice \"admin\" smith`},
		{"only quotes", `""`, `\"\"`},
		{"empty string", "", ""},
		{"CQL injection attempt", `" OR type = "secret`, `\" OR type = \"secret`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EscapeCQLString(tt.input)
			if got != tt.want {
				t.Errorf("EscapeCQLString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
