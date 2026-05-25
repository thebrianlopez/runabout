package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// helpers

func insertTestUser(t *testing.T, q *Queue, sub, email string) int64 {
	t.Helper()
	res, err := q.db.Exec(
		`INSERT INTO users (google_sub, email, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		sub, email, "Test User", 0, 0,
	)
	if err != nil {
		t.Fatalf("insertTestUser: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func testServerWithSession(t *testing.T) (*Server, *httptest.Server, string, int64) {
	t.Helper()
	q := newTestQueue(t)
	ring := NewRingLog(10)
	srv := NewServer("operator-token", nil, q, ring, false, nil)
	userID := insertTestUser(t, q, "device-test-sub", "device@example.com")
	sessionToken, err := srv.issueSession(userID, "device-test-sub")
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}
	ts := httptest.NewServer(srv.Mux())
	t.Cleanup(ts.Close)
	return srv, ts, sessionToken, userID
}

func postRegisterDevice(t *testing.T, ts *httptest.Server, token string, body map[string]any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/devices/register", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /devices/register: %v", err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	resp.Body.Close()
	return m
}

// CT-1: Register a new device — row inserted, response ok.
func TestDeviceCT1_RegisterNew(t *testing.T) {
	_, ts, token, _ := testServerWithSession(t)

	resp := postRegisterDevice(t, ts, token, map[string]any{
		"device_id":   "android-ct1-device",
		"fcm_token":   "fcm-token-ct1",
		"device_name": "Pixel CT1",
		"platform":    "android",
		"app_version": "0.2.1",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	if body["device_id"] != "android-ct1-device" {
		t.Errorf("device_id = %v, want android-ct1-device", body["device_id"])
	}
	if body["registered"] != true {
		t.Errorf("registered = %v, want true", body["registered"])
	}
}

// CT-2: Re-register same device with new FCM token — row updated, row count unchanged.
func TestDeviceCT2_TokenRotation(t *testing.T) {
	srv, ts, token, userID := testServerWithSession(t)
	ctx := t.Context()

	postRegisterDevice(t, ts, token, map[string]any{
		"device_id": "android-ct2",
		"fcm_token": "fcm-token-original",
		"platform":  "android",
	})
	postRegisterDevice(t, ts, token, map[string]any{
		"device_id": "android-ct2",
		"fcm_token": "fcm-token-rotated",
		"platform":  "android",
	})

	// Row count must still be 1 for this (user, device) pair.
	var count int
	srv.queue.db.QueryRow(`SELECT COUNT(*) FROM devices WHERE user_id=? AND device_id=?`, userID, "android-ct2").Scan(&count)
	if count != 1 {
		t.Errorf("device row count = %d, want 1", count)
	}
	// Token must be the rotated value.
	tok, _ := srv.queue.LookupDeviceToken(ctx, userID, "android-ct2")
	if tok != "fcm-token-rotated" {
		t.Errorf("token = %q, want fcm-token-rotated", tok)
	}
}

// CT-3: Two devices for same user — two independent rows.
func TestDeviceCT3_TwoDevices(t *testing.T) {
	srv, ts, token, userID := testServerWithSession(t)
	ctx := t.Context()

	postRegisterDevice(t, ts, token, map[string]any{
		"device_id": "android-phone-a",
		"fcm_token": "fcm-phone-a",
		"platform":  "android",
	})
	postRegisterDevice(t, ts, token, map[string]any{
		"device_id": "android-phone-b",
		"fcm_token": "fcm-phone-b",
		"platform":  "android",
	})

	tokA, _ := srv.queue.LookupDeviceToken(ctx, userID, "android-phone-a")
	tokB, _ := srv.queue.LookupDeviceToken(ctx, userID, "android-phone-b")
	if tokA != "fcm-phone-a" {
		t.Errorf("phone-a token = %q, want fcm-phone-a", tokA)
	}
	if tokB != "fcm-phone-b" {
		t.Errorf("phone-b token = %q, want fcm-phone-b", tokB)
	}
}

// CT-4: Missing device_id → 400.
func TestDeviceCT4_MissingDeviceID(t *testing.T) {
	_, ts, token, _ := testServerWithSession(t)

	resp := postRegisterDevice(t, ts, token, map[string]any{
		"fcm_token": "fcm-token-ct4",
		"platform":  "android",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	if body["message"] != "device_id_missing" {
		t.Errorf("message = %v, want device_id_missing", body["message"])
	}
}

// CT-5: device_id > 128 chars → 400 (fails regex validation).
func TestDeviceCT5_DeviceIDTooLong(t *testing.T) {
	_, ts, token, _ := testServerWithSession(t)

	resp := postRegisterDevice(t, ts, token, map[string]any{
		"device_id": strings.Repeat("a", 129),
		"fcm_token": "fcm-token-ct5",
		"platform":  "android",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// CT-6: DeviceBelongsToUser returns false for a device owned by another user.
// Share attribution must not credit the unregistered device.
func TestDeviceCT6_CrossUserDeviceNotAttributed(t *testing.T) {
	q := newTestQueue(t)
	ring := NewRingLog(10)
	srv := NewServer("operator-token", nil, q, ring, false, nil)
	ctx := t.Context()

	userA := insertTestUser(t, q, "sub-ct6-a", "a@example.com")
	userB := insertTestUser(t, q, "sub-ct6-b", "b@example.com")

	// Register device under user A.
	_, err := q.RegisterDevice(ctx, userA, deviceRegisterRequest{
		DeviceID: "shared-device-ct6", FCMToken: "fcm-a", Platform: "android",
	})
	if err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	// User B tries to look up that same device_id — should not find a token.
	tok, _ := srv.queue.LookupDeviceToken(ctx, userB, "shared-device-ct6")
	if tok != "" {
		t.Errorf("user B should not see user A's device token, got %q", tok)
	}

	// DeviceBelongsToUser for user B must return false.
	belongs, _ := srv.queue.DeviceBelongsToUser(ctx, userB, "shared-device-ct6")
	if belongs {
		t.Errorf("DeviceBelongsToUser(userB, device-a) should be false")
	}
}

// CT-7: Unauthenticated request → 401.
func TestDeviceCT7_Unauthenticated(t *testing.T) {
	_, ts, _, _ := testServerWithSession(t)

	resp := postRegisterDevice(t, ts, "" /* no token */, map[string]any{
		"device_id": "android-ct7",
		"fcm_token": "fcm-ct7",
		"platform":  "android",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
}

// CT-8: Disabled device — LookupDeviceToken returns empty string.
func TestDeviceCT8_DisabledDeviceExcluded(t *testing.T) {
	_, ts, token, userID := testServerWithSession(t)
	ctx := t.Context()
	q := newTestQueue(t)

	// Register via API.
	postRegisterDevice(t, ts, token, map[string]any{
		"device_id": "android-ct8",
		"fcm_token": "fcm-ct8",
		"platform":  "android",
	})

	// Disable directly via SQL on the test server's queue.
	_, ts2, token2, userID2 := testServerWithSession(t)
	_ = ts2
	_ = token2
	_ = userID2

	// Use a fresh queue to verify DisableDevice path independently.
	userID3 := insertTestUser(t, q, "sub-ct8-c", "c@example.com")
	_, err := q.RegisterDevice(ctx, userID3, deviceRegisterRequest{
		DeviceID: "android-ct8-c", FCMToken: "fcm-ct8-c", Platform: "android",
	})
	if err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	if err := q.DisableDevice(ctx, userID3, "android-ct8-c"); err != nil {
		t.Fatalf("DisableDevice: %v", err)
	}
	tok, _ := q.LookupDeviceToken(ctx, userID3, "android-ct8-c")
	if tok != "" {
		t.Errorf("disabled device token = %q, want empty", tok)
	}
	_ = userID // suppress unused
}

// CT-9: Register with empty fcm_token → 400 fcm_token_missing.
func TestDeviceCT9_EmptyFCMToken(t *testing.T) {
	_, ts, token, _ := testServerWithSession(t)

	resp := postRegisterDevice(t, ts, token, map[string]any{
		"device_id": "android-ct9",
		"fcm_token": "",
		"platform":  "android",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	if body["message"] != "fcm_token_missing" {
		t.Errorf("message = %v, want fcm_token_missing", body["message"])
	}
}

// CT-10: ListDevices returns only devices for the authenticated user.
func TestDeviceCT10_ListDevicesScoped(t *testing.T) {
	q := newTestQueue(t)
	ctx := t.Context()

	userA := insertTestUser(t, q, "sub-ct10-a", "ct10a@example.com")
	userB := insertTestUser(t, q, "sub-ct10-b", "ct10b@example.com")

	_, _ = q.RegisterDevice(ctx, userA, deviceRegisterRequest{DeviceID: "device-a1", FCMToken: "tok-a1", Platform: "android"})
	_, _ = q.RegisterDevice(ctx, userA, deviceRegisterRequest{DeviceID: "device-a2", FCMToken: "tok-a2", Platform: "android"})
	_, _ = q.RegisterDevice(ctx, userB, deviceRegisterRequest{DeviceID: "device-b1", FCMToken: "tok-b1", Platform: "android"})

	devicesA, err := q.ListDevices(ctx, userA)
	if err != nil {
		t.Fatalf("ListDevices userA: %v", err)
	}
	if len(devicesA) != 2 {
		t.Errorf("userA device count = %d, want 2", len(devicesA))
	}
	for _, d := range devicesA {
		if !strings.HasPrefix(d.DeviceID, "device-a") {
			t.Errorf("unexpected device %q in userA list", d.DeviceID)
		}
	}

	devicesB, err := q.ListDevices(ctx, userB)
	if err != nil {
		t.Fatalf("ListDevices userB: %v", err)
	}
	if len(devicesB) != 1 {
		t.Errorf("userB device count = %d, want 1", len(devicesB))
	}

	_ = fmt.Sprintf("") // keep fmt import used
}

// CT-11: Re-registering a token under a regenerated Android device_id is idempotent.
func TestDeviceCT11_SameTokenNewDeviceIDReassigns(t *testing.T) {
	srv, ts, token, userID := testServerWithSession(t)
	ctx := t.Context()

	resp1 := postRegisterDevice(t, ts, token, map[string]any{
		"device_id": "android-old-install",
		"fcm_token": "fcm-token-stable",
		"platform":  "android",
	})
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", resp1.StatusCode)
	}
	resp2 := postRegisterDevice(t, ts, token, map[string]any{
		"device_id": "android-new-install",
		"fcm_token": "fcm-token-stable",
		"platform":  "android",
	})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200", resp2.StatusCode)
	}

	var count int
	srv.queue.db.QueryRow(`SELECT COUNT(*) FROM devices WHERE token=?`, "fcm-token-stable").Scan(&count)
	if count != 1 {
		t.Fatalf("token row count = %d, want 1", count)
	}
	oldTok, _ := srv.queue.LookupDeviceToken(ctx, userID, "android-old-install")
	if oldTok != "" {
		t.Errorf("old device token = %q, want empty", oldTok)
	}
	newTok, _ := srv.queue.LookupDeviceToken(ctx, userID, "android-new-install")
	if newTok != "fcm-token-stable" {
		t.Errorf("new device token = %q, want fcm-token-stable", newTok)
	}
}
