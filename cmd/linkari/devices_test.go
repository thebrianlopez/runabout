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
	// Close is idempotent; this covers call sites that discard the response,
	// which otherwise strand the connection outside the idle pool (goleak M4).
	t.Cleanup(func() { resp.Body.Close() })
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

// CT-1: Register a new device  -  row inserted, response ok.
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

// CT-2: Re-register same device with new FCM token  -  row updated, row count unchanged.
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

// CT-3: Two devices for same user  -  two independent rows.
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

	// User B tries to look up that same device_id  -  should not find a token.
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

// CT-8: Disabled device  -  LookupDeviceToken returns empty string.
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

// EPIC-178: Device management observability contract tests.

func getDevicesReq(t *testing.T, ts *httptest.Server, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/devices", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /devices: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func postDisableDevice(t *testing.T, ts *httptest.Server, token, deviceID string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/devices/"+deviceID+"/disable", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /devices/%s/disable: %v", deviceID, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// CT-1: GET /devices returns registered devices for the authenticated user.
func TestGetDevices_ReturnsRegisteredDevices(t *testing.T) {
	_, ts, token, _ := testServerWithSession(t)
	postRegisterDevice(t, ts, token, map[string]any{
		"device_id": "android-obs-ct1",
		"fcm_token": "tok-obs-ct1",
		"platform":  "android",
	})
	resp := getDevicesReq(t, ts, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /devices status = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	devices, ok := body["devices"].([]any)
	if !ok || len(devices) < 1 {
		t.Errorf("devices = %v, want non-empty list", body["devices"])
	}
}

// CT-2: GET /devices returns only the authenticated user's devices.
func TestGetDevices_UserScoped(t *testing.T) {
	q := newTestQueue(t)
	ctx := t.Context()
	userA := insertTestUser(t, q, "obs-ct2-a", "obs-ct2-a@example.com")
	userB := insertTestUser(t, q, "obs-ct2-b", "obs-ct2-b@example.com")
	_, _ = q.RegisterDevice(ctx, userA, deviceRegisterRequest{DeviceID: "dev-ct2-a", FCMToken: "tok-ct2-a", Platform: "android"})
	_, _ = q.RegisterDevice(ctx, userB, deviceRegisterRequest{DeviceID: "dev-ct2-b", FCMToken: "tok-ct2-b", Platform: "android"})

	devsA, _ := q.ListDevices(ctx, userA)
	if len(devsA) != 1 || devsA[0].DeviceID != "dev-ct2-a" {
		t.Errorf("userA devices = %v, want [{dev-ct2-a}]", devsA)
	}
	devsB, _ := q.ListDevices(ctx, userB)
	if len(devsB) != 1 || devsB[0].DeviceID != "dev-ct2-b" {
		t.Errorf("userB devices = %v, want [{dev-ct2-b}]", devsB)
	}
}

// CT-3: GET /devices includes disabled devices with enabled: false.
func TestGetDevices_IncludesDisabledDevices(t *testing.T) {
	q := newTestQueue(t)
	ctx := t.Context()
	uid := insertTestUser(t, q, "obs-ct3", "obs-ct3@example.com")
	_, _ = q.RegisterDevice(ctx, uid, deviceRegisterRequest{DeviceID: "dev-ct3", FCMToken: "tok-ct3", Platform: "android"})
	if err := q.DisableDevice(ctx, uid, "dev-ct3"); err != nil {
		t.Fatalf("DisableDevice: %v", err)
	}
	devs, err := q.ListDevices(ctx, uid)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("device count = %d, want 1", len(devs))
	}
	if devs[0].Enabled {
		t.Errorf("disabled device Enabled = true, want false")
	}
}

// CT-4: POST /devices/{id}/disable disables the device.
func TestDisableDevice_DisablesDevice(t *testing.T) {
	_, ts, token, _ := testServerWithSession(t)
	postRegisterDevice(t, ts, token, map[string]any{
		"device_id": "android-obs-ct4",
		"fcm_token": "tok-obs-ct4",
		"platform":  "android",
	})
	resp := postDisableDevice(t, ts, token, "android-obs-ct4")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify via GET /devices: disabled device appears with enabled: false.
	listResp := getDevicesReq(t, ts, token)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /devices after disable status = %d", listResp.StatusCode)
	}
	body := decodeJSON(t, listResp)
	devList, ok := body["devices"].([]any)
	if !ok {
		t.Fatalf("response devices not array: %v", body["devices"])
	}
	found := false
	for _, d := range devList {
		dm, _ := d.(map[string]any)
		if dm["device_id"] == "android-obs-ct4" {
			found = true
			if en, _ := dm["enabled"].(bool); en {
				t.Errorf("device enabled = true after disable, want false")
			}
		}
	}
	if !found {
		t.Errorf("device android-obs-ct4 not found in GET /devices after disable")
	}
}

// CT-5: POST /devices/{id}/disable is idempotent  -  second call still returns 200.
func TestDisableDevice_Idempotent(t *testing.T) {
	_, ts, token, _ := testServerWithSession(t)
	postRegisterDevice(t, ts, token, map[string]any{
		"device_id": "android-obs-ct5",
		"fcm_token": "tok-obs-ct5",
		"platform":  "android",
	})
	for i := 1; i <= 2; i++ {
		resp := postDisableDevice(t, ts, token, "android-obs-ct5")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("call %d: disable status = %d, want 200", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// CT-6: Unauthenticated GET /devices → 401.
func TestGetDevices_Unauthenticated(t *testing.T) {
	_, ts, _, _ := testServerWithSession(t)
	resp := getDevicesReq(t, ts, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /devices without auth status = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
}

// CT-7: Unauthenticated POST /devices/{id}/disable → 401.
func TestDisableDevice_Unauthenticated(t *testing.T) {
	_, ts, _, _ := testServerWithSession(t)
	resp := postDisableDevice(t, ts, "", "android-any-ct7")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST disable without auth status = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
}

// M4 CT-8: LookupDeviceToken returns empty string for a disabled device.
func TestLookupDeviceToken_DisabledReturnsEmpty(t *testing.T) {
	q := newTestQueue(t)
	ctx := t.Context()
	uid := insertTestUser(t, q, "obs-m4-ct8", "obs-m4-ct8@example.com")
	if _, err := q.RegisterDevice(ctx, uid, deviceRegisterRequest{DeviceID: "dev-m4-ct8", FCMToken: "tok-m4-ct8", Platform: "android"}); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	if err := q.DisableDevice(ctx, uid, "dev-m4-ct8"); err != nil {
		t.Fatalf("DisableDevice: %v", err)
	}
	tok, err := q.LookupDeviceToken(ctx, uid, "dev-m4-ct8")
	if err != nil {
		t.Fatalf("LookupDeviceToken: %v", err)
	}
	if tok != "" {
		t.Errorf("disabled device token = %q, want empty", tok)
	}
}

// RG-6 (PA-6): Legacy devices row (user_id=NULL, device_id=”) must not cause 503 on
// a valid registration. Regression guard for the a207e45 cleanup path.
func TestRegisterDevice_LegacyNullRowDoesNotCause503(t *testing.T) {
	srv, ts, token, _ := testServerWithSession(t)

	// Seed a legacy row mimicking the NULL/empty stale entry that caused the original 503.
	if _, err := srv.queue.db.Exec(
		`INSERT INTO devices(token, user_id, device_id, platform, enabled, created_at, updated_at, token_updated_at, last_seen_at)
		 VALUES('', NULL, '', 'android', 1, 0, 0, 0, 0)`,
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	resp := postRegisterDevice(t, ts, token, map[string]any{
		"device_id": "android-pa6",
		"fcm_token": "fcm-pa6-token",
		"platform":  "android",
	})
	if resp.StatusCode == http.StatusServiceUnavailable {
		body := decodeJSON(t, resp)
		t.Fatalf("RG-6: got 503 device_registry_unavailable; legacy NULL row caused regression: %v", body)
	}
	if resp.StatusCode != http.StatusOK {
		body := decodeJSON(t, resp)
		t.Fatalf("RG-6: status = %d, want 200; body: %v", resp.StatusCode, body)
	}
	resp.Body.Close()

	// Verify new row was written.
	tok, err := srv.queue.LookupDeviceToken(t.Context(), 0, "android-pa6")
	_ = err // userID mismatch expected; just verify row exists via direct query
	var count int
	srv.queue.db.QueryRow(`SELECT COUNT(*) FROM devices WHERE device_id='android-pa6' AND token='fcm-pa6-token'`).Scan(&count)
	if count != 1 {
		t.Errorf("RG-6: expected 1 row for android-pa6, got %d", count)
	}
	_ = tok
}
