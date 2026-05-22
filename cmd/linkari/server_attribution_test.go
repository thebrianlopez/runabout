package main

// F3 contract tests: share-origin device attribution.
//
// Verifies that POST /share correctly sets submitted_by_device_id and
// submitted_by_user_id on queue rows when a valid registered device_id is
// present, and that unregistered/invalid/absent device_id values degrade
// gracefully without blocking share submission.
//
// TDD: PerDevicePushRouting_F3_ShareOriginAttribution_TDD.md
// CT-1 through CT-5.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// postShare issues a POST /share with the given JSON body and session token.
// Uses the shield token path when sessionToken is empty.
func postShare(t *testing.T, ts *httptest.Server, sessionToken string, body map[string]any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/share", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if sessionToken != "" {
		req.Header.Set("Authorization", "Bearer "+sessionToken)
	} else {
		req.Header.Set("Authorization", "Bearer operator-token")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /share: %v", err)
	}
	return resp
}

// firstQueueItem returns the first queued row, or fails if the queue is empty.
func firstQueueItem(t *testing.T, q *Queue) QueueItem {
	t.Helper()
	items, err := q.List("", 5)
	if err != nil {
		t.Fatalf("queue.List: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least one queue item")
	}
	return items[0]
}

// newAttributionServer returns a Server, httptest.Server, session token, userID,
// and registered device_id ready for attribution tests.
func newAttributionServer(t *testing.T) (*Server, *httptest.Server, string, int64, string) {
	t.Helper()
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: 0})
	ring := NewRingLog(10)
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("operator-token", router, q, ring, false, nil)

	userID := insertTestUser(t, q, "attr-test-sub", "attr@example.com")
	sessionToken, err := srv.issueSession(userID, "attr-test-sub")
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}

	const deviceID = "android-attr-test-device"
	ctx := context.Background()
	if _, err := q.RegisterDevice(ctx, userID, deviceRegisterRequest{
		DeviceID: deviceID, FCMToken: "fcm-attr-test", Platform: "android",
	}); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	ts := httptest.NewServer(srv.Mux())
	t.Cleanup(ts.Close)
	return srv, ts, sessionToken, userID, deviceID
}

// CT-1: Share with valid registered device_id — queue row attributed correctly.
func TestF3CT1_AttributedShare(t *testing.T) {
	isolateEventsDir(t)
	srv, ts, token, userID, deviceID := newAttributionServer(t)

	resp := postShare(t, ts, token, map[string]any{
		"url":       "https://example.com/ct1",
		"action":    "uinit_life",
		"device_id": deviceID,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("share status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	item := firstQueueItem(t, srv.queue)
	if item.SubmittedByDeviceID != deviceID {
		t.Errorf("submitted_by_device_id = %q, want %q", item.SubmittedByDeviceID, deviceID)
	}
	if item.SubmittedByUserID != userID {
		t.Errorf("submitted_by_user_id = %d, want %d", item.SubmittedByUserID, userID)
	}
}

// CT-2: Share with unregistered device_id — share accepted, attribution cleared.
func TestF3CT2_UnregisteredDeviceCleared(t *testing.T) {
	isolateEventsDir(t)
	srv, ts, token, _, _ := newAttributionServer(t)

	resp := postShare(t, ts, token, map[string]any{
		"url":       "https://example.com/ct2",
		"action":    "uinit_life",
		"device_id": "android-not-registered",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("share status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	item := firstQueueItem(t, srv.queue)
	if item.SubmittedByDeviceID != "" {
		t.Errorf("submitted_by_device_id = %q, want empty (unregistered device)", item.SubmittedByDeviceID)
	}
}

// CT-3: Share without device_id (legacy client) — share accepted, attribution empty.
func TestF3CT3_LegacyShareNoDeviceID(t *testing.T) {
	isolateEventsDir(t)
	srv, ts, token, _, _ := newAttributionServer(t)

	resp := postShare(t, ts, token, map[string]any{
		"url":    "https://example.com/ct3",
		"action": "uinit_life",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("share status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	item := firstQueueItem(t, srv.queue)
	if item.SubmittedByDeviceID != "" {
		t.Errorf("submitted_by_device_id = %q, want empty (no device_id sent)", item.SubmittedByDeviceID)
	}
}

// CT-4: Share with invalid device_id format — attribution cleared, share accepted.
func TestF3CT4_InvalidDeviceIDFormatCleared(t *testing.T) {
	isolateEventsDir(t)
	srv, ts, token, _, _ := newAttributionServer(t)

	resp := postShare(t, ts, token, map[string]any{
		"url":       "https://example.com/ct4",
		"action":    "uinit_life",
		"device_id": "bad device id with spaces!",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("share status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	item := firstQueueItem(t, srv.queue)
	if item.SubmittedByDeviceID != "" {
		t.Errorf("submitted_by_device_id = %q, want empty (invalid format)", item.SubmittedByDeviceID)
	}
}

// CT-5: Share from non-session client (shield token) — attribution skipped, share accepted.
func TestF3CT5_NonSessionShareNoAttribution(t *testing.T) {
	isolateEventsDir(t)
	srv, ts, _, _, deviceID := newAttributionServer(t)

	// Use shield token (operator-token), not a session token.
	resp := postShare(t, ts, "" /* uses operator-token */, map[string]any{
		"url":       "https://example.com/ct5",
		"action":    "uinit_life",
		"device_id": deviceID,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("share status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	item := firstQueueItem(t, srv.queue)
	if item.SubmittedByDeviceID != "" {
		t.Errorf("submitted_by_device_id = %q, want empty (non-session share)", item.SubmittedByDeviceID)
	}
}
