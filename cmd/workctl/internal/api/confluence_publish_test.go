package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	confluenceSDK "github.com/ctreminiom/go-atlassian/v2/confluence"
	jiraSDK "github.com/ctreminiom/go-atlassian/v2/jira/v3"
	"golang.org/x/time/rate"
)

// newPublishTestClients builds an AtlassianClients pointed at srv and stores
// the captured request bodies in the provided map (keyed by path).
// The server handler is caller-provided so tests can inspect the request body.
func newPublishTestClients(t *testing.T, srv *httptest.Server) *AtlassianClients {
	t.Helper()
	jiraClient, err := jiraSDK.New(srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("jira.New: %v", err)
	}
	jiraClient.Auth.SetBasicAuth("test@example.com", "token")

	confClient, err := confluenceSDK.New(srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("confluence.New: %v", err)
	}
	confClient.Auth.SetBasicAuth("test@example.com", "token")

	host := strings.TrimPrefix(srv.URL, "http://")
	return &AtlassianClients{
		Jira:        jiraClient,
		Confluence:  confClient,
		domain:      host,
		rateLimiter: rate.NewLimiter(rate.Inf, 1),
	}
}

// TestPublishPage_PayloadShape verifies the JSON body sent to Content.Create.
func TestPublishPage_PayloadShape(t *testing.T) {
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rest/api/content") {
			if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id":    "99999",
				"type":  "page",
				"title": "Test Standup",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	clients := newPublishTestClients(t, srv)
	host := strings.TrimPrefix(srv.URL, "http://")

	pageID, pageURL, err := clients.PublishPage(context.Background(), "~429790461", "4379148291", "Test Standup", "<h1>Hello</h1>")
	if err != nil {
		t.Fatalf("PublishPage error: %v", err)
	}

	// Returned values
	if pageID != "99999" {
		t.Errorf("pageID = %q, want \"99999\"", pageID)
	}
	wantURL := "https://" + host + "/wiki/spaces/~429790461/pages/99999"
	if pageURL != wantURL {
		t.Errorf("pageURL = %q, want %q", pageURL, wantURL)
	}

	// Payload shape
	if capturedBody == nil {
		t.Fatal("no request body captured")
	}
	if got := capturedBody["type"]; got != "page" {
		t.Errorf("type = %v, want \"page\"", got)
	}
	if got := capturedBody["title"]; got != "Test Standup" {
		t.Errorf("title = %v, want \"Test Standup\"", got)
	}

	space, _ := capturedBody["space"].(map[string]any)
	if space == nil {
		t.Fatal("missing space in payload")
	}
	if got := space["key"]; got != "~429790461" {
		t.Errorf("space.key = %v, want \"~429790461\"", got)
	}

	ancestors, _ := capturedBody["ancestors"].([]any)
	if len(ancestors) == 0 {
		t.Fatal("missing ancestors in payload")
	}
	anc, _ := ancestors[0].(map[string]any)
	if got := anc["id"]; got != "4379148291" {
		t.Errorf("ancestors[0].id = %v, want \"4379148291\"", got)
	}

	body, _ := capturedBody["body"].(map[string]any)
	if body == nil {
		t.Fatal("missing body in payload")
	}
	storage, _ := body["storage"].(map[string]any)
	if storage == nil {
		t.Fatal("missing body.storage in payload")
	}
	if got := storage["representation"]; got != "storage" {
		t.Errorf("representation = %v, want \"storage\"", got)
	}
	if got := storage["value"]; got != "<h1>Hello</h1>" {
		t.Errorf("body.storage.value = %v, want \"<h1>Hello</h1>\"", got)
	}
}

// TestPublishPage_APIError verifies error propagation on 4xx responses.
func TestPublishPage_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"statusCode":403,"message":"Forbidden"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	clients := newPublishTestClients(t, srv)
	_, _, err := clients.PublishPage(context.Background(), "SPACE", "1234", "Title", "<p>body</p>")
	if err == nil {
		t.Fatal("expected error on 403 response, got nil")
	}
}

// TestPublishPage_PageURLFormat verifies the URL uses domain/space/pageID.
func TestPublishPage_PageURLFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "42", "type": "page", "title": "T"})
	}))
	defer srv.Close()

	clients := newPublishTestClients(t, srv)
	host := strings.TrimPrefix(srv.URL, "http://")

	_, pageURL, err := clients.PublishPage(context.Background(), "MYSPACE", "100", "T", "<p>x</p>")
	if err != nil {
		t.Fatalf("PublishPage: %v", err)
	}
	want := "https://" + host + "/wiki/spaces/MYSPACE/pages/42"
	if pageURL != want {
		t.Errorf("pageURL = %q, want %q", pageURL, want)
	}
}
