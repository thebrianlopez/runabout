package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	confluence "github.com/ctreminiom/go-atlassian/v2/confluence"
	jira "github.com/ctreminiom/go-atlassian/v2/jira/v3"
	"golang.org/x/time/rate"
)

// testRoute defines a stubbed HTTP route for contract tests.
type testRoute struct {
	method     string
	statusCode int
	body       string
}

// newTestAtlassianClients creates an AtlassianClients pointed at an httptest server
// with the given route stubs. Returns the clients and a cleanup function.
func newTestAtlassianClients(t *testing.T, routes map[string]testRoute) (*AtlassianClients, func()) {
	t.Helper()

	mux := http.NewServeMux()
	for path, route := range routes {
		route := route // capture
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(route.statusCode)
			w.Write([]byte(route.body))
		})
	}

	server := httptest.NewServer(mux)

	jiraClient, err := jira.New(nil, server.URL)
	if err != nil {
		t.Fatalf("creating test jira client: %v", err)
	}

	confClient, err := confluence.New(nil, server.URL)
	if err != nil {
		t.Fatalf("creating test confluence client: %v", err)
	}

	clients := &AtlassianClients{
		Jira:        jiraClient,
		Confluence:  confClient,
		domain:      "test.atlassian.net",
		rateLimiter: rate.NewLimiter(rate.Inf, 1), // no rate limit in tests
	}

	return clients, server.Close
}
