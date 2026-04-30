package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// CT-7: compile-time assertion that JiraClient satisfies DomainClient.
var _ DomainClient = (*JiraClient)(nil)

// CT-1: ParseAtlassianURL with /browse/PROJ-123 → issueKey="PROJ-123".
func TestParseAtlassianURL_BrowseIssue(t *testing.T) {
	u, _ := url.Parse("https://acme.atlassian.net/browse/PROJ-123")
	issueKey, pageID, err := ParseAtlassianURL(u)
	if err != nil {
		t.Fatalf("CT-1: unexpected error: %v", err)
	}
	if issueKey != "PROJ-123" || pageID != "" {
		t.Errorf("CT-1: got issueKey=%q pageID=%q, want issueKey=PROJ-123 pageID=''", issueKey, pageID)
	}
}

// CT-2: ParseAtlassianURL with /wiki/spaces/ENG/pages/12345 → pageID="12345".
func TestParseAtlassianURL_WikiPage(t *testing.T) {
	u, _ := url.Parse("https://acme.atlassian.net/wiki/spaces/ENG/pages/12345/Some+Title")
	issueKey, pageID, err := ParseAtlassianURL(u)
	if err != nil {
		t.Fatalf("CT-2: unexpected error: %v", err)
	}
	if pageID != "12345" || issueKey != "" {
		t.Errorf("CT-2: got issueKey=%q pageID=%q, want issueKey='' pageID=12345", issueKey, pageID)
	}
}

// CT-3: ParseAtlassianURL with unsupported path → ErrAtlassianUnsupported.
func TestParseAtlassianURL_Unsupported(t *testing.T) {
	u, _ := url.Parse("https://acme.atlassian.net/jira/software/projects/FOO/boards")
	_, _, err := ParseAtlassianURL(u)
	if err != ErrAtlassianUnsupported {
		t.Errorf("CT-3: expected ErrAtlassianUnsupported, got %v", err)
	}
}

// CT-4: Fetch mock 200 Jira issue → (content, ContentTypePlain, nil).
func TestJiraClient_Fetch_IssueOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		desc := "Some description"
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"fields": map[string]interface{}{
				"summary":     "Test summary",
				"description": &desc,
			},
		})
	}))
	defer srv.Close()

	c := &JiraClient{Domain: "acme.atlassian.net", Username: "u", Password: "p", baseURL: srv.URL, httpClient: srv.Client()}
	u, _ := url.Parse("https://acme.atlassian.net/browse/PROJ-1")
	content, ct, err := c.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("CT-4: unexpected error: %v", err)
	}
	if ct != ContentTypePlain {
		t.Errorf("CT-4: expected ContentTypePlain, got %v", ct)
	}
	if content == "" {
		t.Error("CT-4: expected non-empty content")
	}
}

// CT-5: Fetch mock 401 → ErrAtlassianAuth.
func TestJiraClient_Fetch_Auth401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &JiraClient{Domain: "acme.atlassian.net", Username: "u", Password: "p", baseURL: srv.URL, httpClient: srv.Client()}
	u, _ := url.Parse("https://acme.atlassian.net/browse/PROJ-1")
	_, _, err := c.Fetch(context.Background(), u)
	if err != ErrAtlassianAuth {
		t.Errorf("CT-5: expected ErrAtlassianAuth, got %v", err)
	}
}

// CT-6: Fetch mock 404 → ErrAtlassianNotFound.
func TestJiraClient_Fetch_NotFound404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := &JiraClient{Domain: "acme.atlassian.net", Username: "u", Password: "p", baseURL: srv.URL, httpClient: srv.Client()}
	u, _ := url.Parse("https://acme.atlassian.net/browse/PROJ-1")
	_, _, err := c.Fetch(context.Background(), u)
	if err != ErrAtlassianNotFound {
		t.Errorf("CT-6: expected ErrAtlassianNotFound, got %v", err)
	}
}
