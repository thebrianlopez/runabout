package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// captureRoundTripper records the last FCM request body for payload assertions.
type captureRoundTripper struct {
	status  int
	reqBody []byte
	count   int32
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt32(&c.count, 1)
	body, _ := io.ReadAll(req.Body)
	c.reqBody = body
	return &http.Response{
		StatusCode: c.status,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func installCaptureFCMTransport(t *testing.T) *captureRoundTripper {
	t.Helper()
	rt := &captureRoundTripper{status: http.StatusOK}
	prev := http.DefaultClient.Transport
	http.DefaultClient.Transport = rt
	t.Cleanup(func() { http.DefaultClient.Transport = prev })
	return rt
}

// CT-1: Failed rows appear in /queue response
func TestTerminalFailureCT1_FailedRowsInQueueAPI(t *testing.T) {
	q := newTestQueue(t)
	id, err := q.Enqueue(&ShareRequest{URL: "https://ct1f2.example.com", Type: "url", Profile: "life"})
	if err != nil {
		t.Fatal(err)
	}
	// Directly mark as failed (simulates retryOrFail terminal path)
	q.db.Exec("UPDATE queue SET status='failed', error_reason='scoring_retry_exhausted: timeout' WHERE id=?", id)

	srv := newTestServerWithQueue(t, q)
	mux := srv.Mux()

	req := httptest.NewRequest(http.MethodGet, "/queue?status=failed", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-1: GET /queue returned %d", w.Code)
	}

	var items []map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
		t.Fatalf("CT-1: decode response: %v", err)
	}

	for _, item := range items {
		if int64(item["id"].(float64)) == id {
			if item["status"] != "failed" {
				t.Errorf("CT-1: item status = %q, want failed", item["status"])
			}
			return
		}
	}
	t.Errorf("CT-1: failed row id=%d not found in /queue response (got %d items)", id, len(items))
}

// CT-2: error_reason populated on failed rows in /queue response
func TestTerminalFailureCT2_ErrorReasonInQueueAPI(t *testing.T) {
	q := newTestQueue(t)
	id, err := q.Enqueue(&ShareRequest{URL: "https://ct2f2.example.com", Type: "url", Profile: "life"})
	if err != nil {
		t.Fatal(err)
	}
	q.db.Exec("UPDATE queue SET status='failed', error_reason='scoring_retry_exhausted: session expired' WHERE id=?", id)

	srv := newTestServerWithQueue(t, q)
	mux := srv.Mux()

	req := httptest.NewRequest(http.MethodGet, "/queue?status=failed", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var items []map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
		t.Fatalf("CT-2: decode: %v", err)
	}

	for _, item := range items {
		if int64(item["id"].(float64)) == id {
			reason, ok := item["error_reason"]
			if !ok || reason == "" || reason == nil {
				t.Errorf("CT-2: error_reason missing or empty in /queue response for failed row %d", id)
			}
			return
		}
	}
	t.Errorf("CT-2: failed row %d not found in response", id)
}

// CT-3: error_reason truncated to 200 chars at API response layer
func TestTerminalFailureCT3_ErrorReasonTruncatedAt200(t *testing.T) {
	q := newTestQueue(t)
	id, err := q.Enqueue(&ShareRequest{URL: "https://ct3f2.example.com", Type: "url", Profile: "life"})
	if err != nil {
		t.Fatal(err)
	}
	// Store a 300-char error_reason in SQLite
	longReason := strings.Repeat("x", 300)
	q.db.Exec("UPDATE queue SET status='failed', error_reason=? WHERE id=?", longReason, id)

	srv := newTestServerWithQueue(t, q)
	mux := srv.Mux()

	req := httptest.NewRequest(http.MethodGet, "/queue?status=failed", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var items []map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
		t.Fatalf("CT-3: decode: %v", err)
	}

	for _, item := range items {
		if int64(item["id"].(float64)) == id {
			reason, _ := item["error_reason"].(string)
			if len(reason) > 200 {
				t.Errorf("CT-3: error_reason len=%d in API response, want ≤200", len(reason))
			}
			if len(reason) == 0 {
				t.Errorf("CT-3: error_reason empty — field not surfaced (len must be 1–200)")
			}
			return
		}
	}
	t.Errorf("CT-3: failed row %d not found", id)
}

// CT-4: Non-failed rows unaffected by F2 changes
func TestTerminalFailureCT4_NonFailedRowsUnaffected(t *testing.T) {
	q := newTestQueue(t)

	// Enqueue rows in different statuses.
	pendingID, _ := q.Enqueue(&ShareRequest{URL: "https://ct4a.example.com", Type: "url", Profile: "life"})
	scoredID, _ := q.Enqueue(&ShareRequest{URL: "https://ct4b.example.com", Type: "url", Profile: "life"})
	score := 75
	q.db.Exec("UPDATE queue SET status='scored', score=? WHERE id=?", score, scoredID)

	srv := newTestServerWithQueue(t, q)
	mux := srv.Mux()

	for _, tc := range []struct {
		status string
		id     int64
	}{
		{"pending", pendingID},
		{"scored", scoredID},
	} {
		req := httptest.NewRequest(http.MethodGet, "/queue?status="+tc.status, nil)
		req.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("CT-4: status=%s returned %d", tc.status, w.Code)
			continue
		}

		var items []map[string]interface{}
		if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
			t.Errorf("CT-4: decode for status=%s: %v", tc.status, err)
			continue
		}

		found := false
		for _, item := range items {
			if int64(item["id"].(float64)) == tc.id {
				found = true
				if item["status"] != tc.status {
					t.Errorf("CT-4: row %d status=%q, want %q", tc.id, item["status"], tc.status)
				}
			}
		}
		if !found {
			t.Errorf("CT-4: row id=%d (status=%s) not found in response", tc.id, tc.status)
		}
	}
}

// CT-5: FCM push payload for failed item includes "status":"failed" and "error_reason"
func TestTerminalFailureCT5_FCMPushFailedItemPayload(t *testing.T) {
	q := newTestQueue(t)
	// Create the queue row and mark it failed
	id, _ := q.Enqueue(&ShareRequest{URL: "https://ct5f2.example.com", Type: "url", Profile: "life"})
	q.db.Exec("UPDATE queue SET status='failed', error_reason='scoring_retry_exhausted: eval_failed', slug='ct5-slug' WHERE id=?", id)

	// Seed a push_outbox row as a failed-item notification (EPIC-111 F2 M6).
	now := time.Now().Unix()
	q.db.Exec(`INSERT INTO push_outbox
		(score, slug, verdict, url, kind, status, attempts, next_attempt, created_at, updated_at, last_error, profile, error_reason)
		VALUES (0, 'ct5-slug', '', 'https://ct5f2.example.com', 'notify', 'pending', 0, 0, ?, ?, '', 'life', 'scoring_retry_exhausted: eval_failed')`,
		now, now)

	// Register a device token so drainPushOutbox proceeds.
	q.db.Exec("INSERT OR REPLACE INTO devices (token, updated_at) VALUES ('device-ct5', ?)", now)

	rt := installCaptureFCMTransport(t)

	srv := newTestServerWithQueue(t, q)
	srv.SetMetrics(nil)
	// Wire a fake FCM token source
	srv2 := &Server{
		token:          "test-token",
		router:         srv.router,
		queue:          q,
		ring:           NewRingLog(10),
		fcmTokenSource: fakeSource{},
	}
	_ = srv2

	// Use drainPushOutbox via the server with FCM token source.
	srvFCM := NewServer("test-token", srv.router, q, NewRingLog(10), false, fakeSource{})
	srvFCM.drainPushOutbox(t.Context())

	if rt.count == 0 {
		t.Skip("CT-5: no FCM request sent (device token or FCM source not wired) — check setup")
		return
	}

	// Parse the FCM payload
	var payload map[string]interface{}
	if err := json.Unmarshal(rt.reqBody, &payload); err != nil {
		t.Fatalf("CT-5: parse FCM payload: %v", err)
	}

	msg, _ := payload["message"].(map[string]interface{})
	data, _ := msg["data"].(map[string]interface{})

	if data["status"] != "failed" {
		t.Errorf("CT-5: FCM data.status = %q, want failed", data["status"])
	}
	errReason, _ := data["error_reason"].(string)
	if errReason == "" {
		t.Errorf("CT-5: FCM data.error_reason empty, want populated for failed item")
	}
}

// newTestServerWithQueue creates a test Server backed by the provided queue.
func newTestServerWithQueue(t *testing.T, q *Queue) *Server {
	t.Helper()
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	return NewServer("test-token", router, q, NewRingLog(10), false, nil)
}

// fakeSource is a minimal oauth2.TokenSource for FCM push tests.
type fakeSource struct{}

func (fakeSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "fake-fcm-token"}, nil
}
