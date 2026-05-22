package main

// F5 contract tests: device management and observability.
//
// Covers GET /devices and POST /devices/{device_id}/disable HTTP endpoints.
//
// TDD: PerDevicePushRouting_F5_DeviceObservability_TDD.md
// CT-1 through CT-8.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// getDevices issues GET /devices with the given session token.
func getDevices(t *testing.T, ts *httptest.Server, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/devices", ts.URL), nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /devices: %v", err)
	}
	return resp
}

// disableDevice issues POST /devices/{id}/disable with the given session token.
func disableDevice(t *testing.T, ts *httptest.Server, token, deviceID string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/devices/%s/disable", ts.URL, deviceID), nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /devices/%s/disable: %v", deviceID, err)
	}
	return resp
}

// decodeDeviceList decodes a GET /devices response body into a slice of maps.
func decodeDeviceList(t *testing.T, resp *http.Response) []map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var body struct {
		Devices []map[string]any `json:"devices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /devices response: %v", err)
	}
	return body.Devices
}

// CT-1: GET /devices returns registered devices with correct fields.
func TestF5CT1_ListDevicesReturnsRegistered(t *testing.T) {
	_, ts, token, userID, _ := newAttributionServer(t)
	ctx := context.Background()
	q := newTestQueue(t)

	// Register a second device via the queue helper on a fresh queue with a known user.
	userID2 := insertTestUser(t, q, "f5ct1-sub", "f5ct1@example.com")
	_ = userID2

	// Use the attribution server's session + device (already registered by newAttributionServer).
	_ = ctx
	_ = userID

	resp := getDevices(t, ts, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	devices := decodeDeviceList(t, resp)
	if len(devices) == 0 {
		t.Fatal("expected at least one device")
	}
	d := devices[0]
	if d["device_id"] == nil || d["device_id"] == "" {
		t.Errorf("device_id missing from response: %v", d)
	}
	if d["platform"] == nil {
		t.Errorf("platform missing from response: %v", d)
	}
	if d["enabled"] != true {
		t.Errorf("enabled = %v, want true", d["enabled"])
	}
}

// CT-2: GET /devices is user-scoped — user A cannot see user B's devices.
// Uses a single server with two authenticated users and distinct device IDs.
func TestF5CT2_ListDevicesUserScoped(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: 0})
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("operator-token", router, q, NewRingLog(10), false, nil)
	ts := httptest.NewServer(srv.Mux())
	t.Cleanup(ts.Close)

	userA := insertTestUser(t, q, "f5ct2-sub-a", "ct2a@example.com")
	userB := insertTestUser(t, q, "f5ct2-sub-b", "ct2b@example.com")
	tokenA, _ := srv.issueSession(userA, "f5ct2-sub-a")
	tokenB, _ := srv.issueSession(userB, "f5ct2-sub-b")

	_, _ = q.RegisterDevice(ctx, userA, deviceRegisterRequest{DeviceID: "device-ct2-a", FCMToken: "tok-a", Platform: "android"})
	_, _ = q.RegisterDevice(ctx, userB, deviceRegisterRequest{DeviceID: "device-ct2-b", FCMToken: "tok-b", Platform: "android"})

	devicesA := decodeDeviceList(t, getDevices(t, ts, tokenA))
	devicesB := decodeDeviceList(t, getDevices(t, ts, tokenB))

	for _, d := range devicesA {
		if d["device_id"] == "device-ct2-b" {
			t.Error("user A can see user B's device")
		}
	}
	for _, d := range devicesB {
		if d["device_id"] == "device-ct2-a" {
			t.Error("user B can see user A's device")
		}
	}
	if len(devicesA) == 0 || devicesA[0]["device_id"] != "device-ct2-a" {
		t.Errorf("user A devices = %v, want [{device_id: device-ct2-a}]", devicesA)
	}
	if len(devicesB) == 0 || devicesB[0]["device_id"] != "device-ct2-b" {
		t.Errorf("user B devices = %v, want [{device_id: device-ct2-b}]", devicesB)
	}
}

// CT-3: GET /devices includes disabled devices with enabled: false.
func TestF5CT3_ListIncludesDisabledDevices(t *testing.T) {
	_, ts, token, _, deviceID := newAttributionServer(t)

	// Disable via API.
	dresp := disableDevice(t, ts, token, deviceID)
	if dresp.StatusCode != http.StatusOK {
		dresp.Body.Close()
		t.Fatalf("disable status = %d, want 200", dresp.StatusCode)
	}
	dresp.Body.Close()

	devices := decodeDeviceList(t, getDevices(t, ts, token))
	var found bool
	for _, d := range devices {
		if d["device_id"] == deviceID {
			found = true
			if d["enabled"] != false {
				t.Errorf("device %q enabled = %v, want false after disable", deviceID, d["enabled"])
			}
		}
	}
	if !found {
		t.Errorf("disabled device %q missing from GET /devices list", deviceID)
	}
}

// CT-4: POST /devices/{id}/disable disables device; GET /devices reflects change.
func TestF5CT4_DisableDeviceRoundtrip(t *testing.T) {
	_, ts, token, _, deviceID := newAttributionServer(t)

	resp := disableDevice(t, ts, token, deviceID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable status = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	if body["enabled"] != false {
		t.Errorf("response enabled = %v, want false", body["enabled"])
	}
	if body["device_id"] != deviceID {
		t.Errorf("response device_id = %v, want %q", body["device_id"], deviceID)
	}

	// Confirm via GET /devices.
	devices := decodeDeviceList(t, getDevices(t, ts, token))
	for _, d := range devices {
		if d["device_id"] == deviceID && d["enabled"] != false {
			t.Errorf("device %q still enabled after disable", deviceID)
		}
	}
}

// CT-5: POST /devices/{id}/disable is idempotent — second call returns 200.
func TestF5CT5_DisableIdempotent(t *testing.T) {
	_, ts, token, _, deviceID := newAttributionServer(t)

	for i := 0; i < 2; i++ {
		resp := disableDevice(t, ts, token, deviceID)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("call %d: status = %d, want 200", i+1, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// CT-6: GET /devices unauthenticated → 401.
func TestF5CT6_ListUnauthenticated(t *testing.T) {
	_, ts, _, _, _ := newAttributionServer(t)

	resp := getDevices(t, ts, "" /* no token */)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
}

// CT-7: POST /devices/{id}/disable unauthenticated → 401; device state unchanged.
func TestF5CT7_DisableUnauthenticated(t *testing.T) {
	_, ts, token, _, deviceID := newAttributionServer(t)

	resp := disableDevice(t, ts, "" /* no token */, deviceID)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Device must still be enabled.
	devices := decodeDeviceList(t, getDevices(t, ts, token))
	for _, d := range devices {
		if d["device_id"] == deviceID && d["enabled"] != true {
			t.Errorf("device %q was disabled by unauthenticated request", deviceID)
		}
	}
}

// CT-8: Disabled device excluded from LookupDeviceToken (push routing).
// Complements F4 CT-4 (drain path). Verifies at the helper level.
func TestF5CT8_DisabledDeviceExcludedFromPushLookup(t *testing.T) {
	ctx := context.Background()
	_, ts, token, userID, deviceID := newAttributionServer(t)
	q := newTestQueue(t)

	// Mirror the device in our direct queue for assertion.
	_ = ts
	_ = token

	// Use the server's queue via the attribution server fixture.
	srv, _, _, _, _ := newAttributionServer(t)
	userID2 := insertTestUser(t, srv.queue, "f5ct8-sub", "f5ct8@example.com")
	if _, err := srv.queue.RegisterDevice(ctx, userID2, deviceRegisterRequest{
		DeviceID: "android-f5ct8", FCMToken: "fcm-f5ct8", Platform: "android",
	}); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	if err := srv.queue.DisableDevice(ctx, userID2, "android-f5ct8"); err != nil {
		t.Fatalf("DisableDevice: %v", err)
	}
	tok, err := srv.queue.LookupDeviceToken(ctx, userID2, "android-f5ct8")
	if err != nil {
		t.Fatalf("LookupDeviceToken: %v", err)
	}
	if tok != "" {
		t.Errorf("disabled device token = %q, want empty", tok)
	}

	_ = userID
	_ = deviceID
	_ = q
}
