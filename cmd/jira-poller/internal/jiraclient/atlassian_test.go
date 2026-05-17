package jiraclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/jiraclient"
)

// newTestClient builds an atlassianClient pointed at the given httptest server.
// sleepFn is injected so tests never incur real delays.
func newTestClient(t *testing.T, srv *httptest.Server, sleepFn func(time.Duration)) jiraclient.Client {
	t.Helper()
	hc := srv.Client()
	c, err := jiraclient.NewAtlassianClientForTest(hc, srv.URL, "test", sleepFn)
	if err != nil {
		t.Fatalf("NewAtlassianClientForTest: %v", err)
	}
	return c
}

func noopSleep(_ time.Duration) {}

// loadFixture reads a JSON test fixture and returns the raw bytes.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("loadFixture(%q): %v", name, err)
	}
	return b
}

// staticJSON returns an http.HandlerFunc that always responds with the given
// JSON body at the given HTTP status.
func staticJSON(status int, body []byte, extraHeaders ...http.Header) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, h := range extraHeaders {
			for k, vs := range h {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

// sequenceHandler returns an http.HandlerFunc that serves successive responses
// from the slice. The last response is repeated after the slice is exhausted.
func sequenceHandler(responses []http.HandlerFunc) http.HandlerFunc {
	var idx int32
	return func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&idx, 1)) - 1
		if i >= len(responses) {
			i = len(responses) - 1
		}
		responses[i](w, r)
	}
}

// countingHandler wraps a handler and increments counter on each request.
func countingHandler(counter *int32, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(counter, 1)
		h(w, r)
	}
}

// CT-1: Status-field items extracted; non-status items filtered.
func TestSearchTransitions_CT1_StatusFieldFiltered(t *testing.T) {
	body := `{
		"startAt": 0, "maxResults": 1, "total": 1,
		"issues": [{
			"key": "PROJ-1", "self": "https://jira.example.com/PROJ-1",
			"fields": {"summary": "S", "issuetype": {"name": "Bug"}, "labels": []},
			"changelog": {"histories": [{
				"id": "h1",
				"created": "2026-04-28T10:00:00.000+0000",
				"author": {"accountId": "u1", "displayName": "Alice"},
				"items": [
					{"field": "status",   "fromString": "To Do",   "toString": "In Progress"},
					{"field": "assignee", "fromString": "",        "toString": "Alice"}
				]
			}]}
		}]
	}`
	srv := httptest.NewServer(staticJSON(http.StatusOK, []byte(body)))
	defer srv.Close()

	c := newTestClient(t, srv, noopSleep)
	res, err := c.SearchTransitions(context.Background(), jiraclient.SearchRequest{
		Projects:        []string{"PROJ"},
		LookbackMinutes: 30,
		MaxResults:      1,
	})
	if err != nil {
		t.Fatalf("SearchTransitions: %v", err)
	}
	if len(res.Issues) != 1 {
		t.Fatalf("want 1 issue, got %d", len(res.Issues))
	}
	if len(res.Issues[0].Changelog) != 1 {
		t.Fatalf("want 1 changelog entry (status only), got %d", len(res.Issues[0].Changelog))
	}
	if res.Issues[0].Changelog[0].ToStatus != "In Progress" {
		t.Errorf("ToStatus = %q, want In Progress", res.Issues[0].Changelog[0].ToStatus)
	}
}

// CT-2: Null fromString normalised to empty string.
func TestSearchTransitions_CT2_NullFromStatus(t *testing.T) {
	// go-atlassian omits empty strings via omitempty, so fromString won't be
	// present when null. We simulate by omitting it from the fixture.
	body := `{
		"startAt": 0, "maxResults": 1, "total": 1,
		"issues": [{
			"key": "PROJ-1", "self": "https://jira.example.com/PROJ-1",
			"fields": {"summary": "S", "issuetype": {"name": "Bug"}, "labels": []},
			"changelog": {"histories": [{
				"id": "h1",
				"created": "2026-04-28T10:00:00.000+0000",
				"author": {"accountId": "u1", "displayName": "Alice"},
				"items": [{"field": "status", "toString": "In Progress"}]
			}]}
		}]
	}`
	srv := httptest.NewServer(staticJSON(http.StatusOK, []byte(body)))
	defer srv.Close()

	c := newTestClient(t, srv, noopSleep)
	res, err := c.SearchTransitions(context.Background(), jiraclient.SearchRequest{
		Projects: []string{"PROJ"}, LookbackMinutes: 30, MaxResults: 1,
	})
	if err != nil {
		t.Fatalf("SearchTransitions: %v", err)
	}
	entry := res.Issues[0].Changelog[0]
	if entry.FromStatus != "" {
		t.Errorf("FromStatus = %q, want empty string for null fromString", entry.FromStatus)
	}
}

// CT-3: 429 triggers retry with Retry-After honour; sleep duration recorded.
func TestSearchTransitions_CT3_RateLimitRetry(t *testing.T) {
	var slept []time.Duration
	fakeSleep := func(d time.Duration) { slept = append(slept, d) }

	ok := loadFixture(t, "search_ok_empty.json")
	var calls int32
	handler := sequenceHandler([]http.HandlerFunc{
		countingHandler(&calls, staticJSON(http.StatusTooManyRequests, []byte(`{}`),
			http.Header{"Retry-After": {"2"}})),
		countingHandler(&calls, staticJSON(http.StatusOK, ok)),
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	c := newTestClient(t, srv, fakeSleep)
	_, err := c.SearchTransitions(context.Background(), jiraclient.SearchRequest{
		Projects: []string{"PROJ"}, LookbackMinutes: 30,
	})
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if int(atomic.LoadInt32(&calls)) != 2 {
		t.Errorf("want 2 calls (1 retry), got %d", calls)
	}
	if len(slept) != 1 {
		t.Fatalf("want 1 sleep, got %d", len(slept))
	}
	if slept[0] < 2*time.Second {
		t.Errorf("sleep = %v, want >= 2s (Retry-After: 2)", slept[0])
	}
}

// CT-4: 5xx triggers retries up to maxRetries → ErrUpstream.
func TestSearchTransitions_CT4_5xxUpstreamError(t *testing.T) {
	var calls int32
	handler := countingHandler(&calls, staticJSON(http.StatusServiceUnavailable, []byte(`{}`)))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	c := newTestClient(t, srv, noopSleep)
	_, err := c.SearchTransitions(context.Background(), jiraclient.SearchRequest{
		Projects: []string{"PROJ"}, LookbackMinutes: 30,
	})
	if !errors.Is(err, jiraclient.ErrUpstream) {
		t.Errorf("err = %v, want ErrUpstream", err)
	}
	if n := int(atomic.LoadInt32(&calls)); n != 3 {
		t.Errorf("want 3 requests (maxRetries), got %d", n)
	}
}

// CT-5: 4xx (non-429) not retried → single request.
func TestSearchTransitions_CT5_NotFound_NoRetry(t *testing.T) {
	var calls int32
	handler := countingHandler(&calls, staticJSON(http.StatusNotFound, []byte(`{}`)))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	c := newTestClient(t, srv, noopSleep)
	_, err := c.SearchTransitions(context.Background(), jiraclient.SearchRequest{
		Projects: []string{"PROJ"}, LookbackMinutes: 30,
	})
	if !errors.Is(err, jiraclient.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if n := int(atomic.LoadInt32(&calls)); n != 1 {
		t.Errorf("want exactly 1 request, got %d", n)
	}
}

// CT-6: nextPageToken drives pagination (second request includes startAt).
func TestSearchTransitions_CT6_Pagination(t *testing.T) {
	var (
		page1 = loadFixture(t, "search_ok_page1.json")
		page2 = loadFixture(t, "search_ok_page2.json")
	)

	// Capture the startAt from the second request body.
	var capturedStartAt int
	handler := func() http.HandlerFunc {
		var callIdx int32
		return func(w http.ResponseWriter, r *http.Request) {
			n := int(atomic.AddInt32(&callIdx, 1))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if n == 1 {
				_, _ = w.Write(page1)
			} else {
				var payload struct {
					StartAt int `json:"startAt"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				capturedStartAt = payload.StartAt
				_, _ = w.Write(page2)
			}
		}
	}()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	c := newTestClient(t, srv, noopSleep)

	res1, err := c.SearchTransitions(context.Background(), jiraclient.SearchRequest{
		Projects: []string{"PROJ"}, LookbackMinutes: 30, MaxResults: 2,
	})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if res1.NextToken == "" {
		t.Fatal("expected non-empty NextToken after page 1")
	}

	_, err = c.SearchTransitions(context.Background(), jiraclient.SearchRequest{
		Projects: []string{"PROJ"}, LookbackMinutes: 30, MaxResults: 2,
		NextToken: res1.NextToken,
	})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if capturedStartAt != 2 {
		t.Errorf("page 2 startAt = %d, want 2", capturedStartAt)
	}
}

// CT-7: Missing assignee → Issue.Assignee == nil.
func TestSearchTransitions_CT7_NilAssignee(t *testing.T) {
	body := `{
		"startAt": 0, "maxResults": 1, "total": 1,
		"issues": [{
			"key": "PROJ-1", "self": "https://jira.example.com/PROJ-1",
			"fields": {"summary": "S", "issuetype": {"name": "Bug"}, "labels": []}
		}]
	}`
	srv := httptest.NewServer(staticJSON(http.StatusOK, []byte(body)))
	defer srv.Close()

	c := newTestClient(t, srv, noopSleep)
	res, err := c.SearchTransitions(context.Background(), jiraclient.SearchRequest{
		Projects: []string{"PROJ"}, LookbackMinutes: 30, MaxResults: 1,
	})
	if err != nil {
		t.Fatalf("SearchTransitions: %v", err)
	}
	if res.Issues[0].Assignee != nil {
		t.Errorf("Assignee = %+v, want nil", res.Issues[0].Assignee)
	}
}

// CT-8: Missing emailAddress (privacy) → User.Email == "".
func TestSearchTransitions_CT8_EmptyEmail(t *testing.T) {
	body := `{
		"startAt": 0, "maxResults": 1, "total": 1,
		"issues": [{
			"key": "PROJ-1", "self": "https://jira.example.com/PROJ-1",
			"fields": {
				"summary": "S",
				"issuetype": {"name": "Bug"},
				"assignee": {"accountId": "u1", "displayName": "Alice"},
				"labels": []
			}
		}]
	}`
	srv := httptest.NewServer(staticJSON(http.StatusOK, []byte(body)))
	defer srv.Close()

	c := newTestClient(t, srv, noopSleep)
	res, err := c.SearchTransitions(context.Background(), jiraclient.SearchRequest{
		Projects: []string{"PROJ"}, LookbackMinutes: 30, MaxResults: 1,
	})
	if err != nil {
		t.Fatalf("SearchTransitions: %v", err)
	}
	if res.Issues[0].Assignee == nil {
		t.Fatal("expected non-nil Assignee")
	}
	if res.Issues[0].Assignee.Email != "" {
		t.Errorf("Email = %q, want empty", res.Issues[0].Assignee.Email)
	}
}

// CT-9: User-Agent header set on every request.
func TestSearchTransitions_CT9_UserAgent(t *testing.T) {
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"startAt":0,"maxResults":0,"total":0,"issues":[]}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, noopSleep)
	_, err := c.SearchTransitions(context.Background(), jiraclient.SearchRequest{
		Projects: []string{"PROJ"}, LookbackMinutes: 30,
	})
	if err != nil {
		t.Fatalf("SearchTransitions: %v", err)
	}
	if !hasPrefix(captured, "jira-transition-poller/") {
		t.Errorf("User-Agent = %q, want prefix jira-transition-poller/", captured)
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// CT-10: 401 returns ErrAuthFailure, not retried.
func TestSearchTransitions_CT10_AuthFailure(t *testing.T) {
	var calls int32
	handler := countingHandler(&calls, staticJSON(http.StatusUnauthorized, []byte(`{}`)))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	c := newTestClient(t, srv, noopSleep)
	_, err := c.SearchTransitions(context.Background(), jiraclient.SearchRequest{
		Projects: []string{"PROJ"}, LookbackMinutes: 30,
	})
	if !errors.Is(err, jiraclient.ErrAuthFailure) {
		t.Errorf("err = %v, want ErrAuthFailure", err)
	}
	if n := int(atomic.LoadInt32(&calls)); n != 1 {
		t.Errorf("want 1 request (no retry), got %d", n)
	}
}

// CT-11: Empty labels normalised to non-nil empty slice.
func TestSearchTransitions_CT11_EmptyLabels(t *testing.T) {
	body := `{
		"startAt": 0, "maxResults": 1, "total": 1,
		"issues": [{
			"key": "PROJ-1", "self": "https://jira.example.com/PROJ-1",
			"fields": {"summary": "S", "issuetype": {"name": "Bug"}}
		}]
	}`
	srv := httptest.NewServer(staticJSON(http.StatusOK, []byte(body)))
	defer srv.Close()

	c := newTestClient(t, srv, noopSleep)
	res, err := c.SearchTransitions(context.Background(), jiraclient.SearchRequest{
		Projects: []string{"PROJ"}, LookbackMinutes: 30, MaxResults: 1,
	})
	if err != nil {
		t.Fatalf("SearchTransitions: %v", err)
	}
	if res.Issues[0].Labels == nil {
		t.Error("Labels is nil, want non-nil empty slice []string{}")
	}
}

// BT-2: MockClient records calls.
func TestMockClient_RecordsCalls(t *testing.T) {
	m := &jiraclient.MockClient{
		Results: []jiraclient.SearchResult{
			{Issues: []jiraclient.Issue{{Key: "PROJ-1"}}},
		},
	}
	req1 := jiraclient.SearchRequest{Projects: []string{"PROJ"}, LookbackMinutes: 10}
	req2 := jiraclient.SearchRequest{Projects: []string{"INFRA"}, LookbackMinutes: 20}

	_, _ = m.SearchTransitions(context.Background(), req1)
	_, _ = m.SearchTransitions(context.Background(), req2)

	if len(m.Calls) != 2 {
		t.Fatalf("want 2 recorded calls, got %d", len(m.Calls))
	}
	if m.Calls[0].Projects[0] != "PROJ" || m.Calls[1].Projects[0] != "INFRA" {
		t.Errorf("calls = %+v", m.Calls)
	}
}
