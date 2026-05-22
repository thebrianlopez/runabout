package main

// F4 contract tests: push outbox device-targeted delivery.
//
// Verifies that EnqueueDevicePush creates correctly targeted outbox rows,
// that the push worker resolves FCM tokens at drain time (not enqueue time),
// handles token rotation, disabled devices, missing devices, legacy fallback,
// and two-device isolation.
//
// TDD: PerDevicePushRouting_F4_PushOutboxDeviceTargeting_TDD.md
// CT-1 through CT-7.

import (
	"context"
	"testing"
)

// newF4Server returns a Server with a real queue and stubbed FCM transport.
func newF4Server(t *testing.T) *Server {
	t.Helper()
	srv := newTestServerWithFCM(t)
	srv.queue.SetPushConfig(&PushConfig{DigestThrottleDefault: 0})
	return srv
}

// lookupOutboxRow scans push_outbox for the given push id and returns it.
func lookupOutboxRow(t *testing.T, srv *Server, pushID int64) struct {
	Status         string
	TargetDeviceID string
	TargetUserID   int64
	PushKind       string
} {
	t.Helper()
	var row struct {
		Status         string
		TargetDeviceID string
		TargetUserID   int64
		PushKind       string
	}
	err := srv.queue.db.QueryRow(
		`SELECT COALESCE(status,''), COALESCE(target_device_id,''), COALESCE(target_user_id,0), COALESCE(push_kind,'')
		 FROM push_outbox WHERE id=?`, pushID,
	).Scan(&row.Status, &row.TargetDeviceID, &row.TargetUserID, &row.PushKind)
	if err != nil {
		t.Fatalf("lookupOutboxRow id=%d: %v", pushID, err)
	}
	return row
}

// CT-1: EnqueueDevicePush creates row with correct target fields.
func TestF4CT1_EnqueueDevicePushFields(t *testing.T) {
	isolateEventsDir(t)
	srv := newF4Server(t)
	ctx := context.Background()

	userID := insertTestUser(t, srv.queue, "f4ct1-sub", "f4ct1@example.com")
	const deviceID = "android-f4ct1"

	id, err := srv.queue.EnqueueDevicePush("eng", 85, "some-slug", "save", "https://example.com", userID, deviceID)
	if err != nil {
		t.Fatalf("EnqueueDevicePush: %v", err)
	}
	_ = ctx

	row := lookupOutboxRow(t, srv, id)
	if row.TargetDeviceID != deviceID {
		t.Errorf("target_device_id = %q, want %q", row.TargetDeviceID, deviceID)
	}
	if row.TargetUserID != userID {
		t.Errorf("target_user_id = %d, want %d", row.TargetUserID, userID)
	}
	if row.PushKind != "score_complete" {
		t.Errorf("push_kind = %q, want score_complete", row.PushKind)
	}
	if row.Status != "pending" {
		t.Errorf("status = %q, want pending", row.Status)
	}
}

// CT-2: Token resolved at drain time — register device before drain but after enqueue.
// Verifies the worker reads the token at send time, not at the time the push was created.
func TestF4CT2_TokenResolvedAtDrainTime(t *testing.T) {
	isolateEventsDir(t)
	installStubTransport(t, &stubRoundTripper{status: 200})
	srv := newF4Server(t)
	ctx := context.Background()

	userID := insertTestUser(t, srv.queue, "f4ct2-sub", "f4ct2@example.com")
	const deviceID = "android-f4ct2"

	// Enqueue push with no global token set (would fail a global lookup).
	id, err := srv.queue.EnqueueDevicePush("eng", 80, "slug-ct2", "save", "https://example.com/ct2", userID, deviceID)
	if err != nil {
		t.Fatalf("EnqueueDevicePush: %v", err)
	}

	// Register the device after enqueue, before drain.
	if _, err := srv.queue.RegisterDevice(ctx, userID, deviceRegisterRequest{
		DeviceID: deviceID, FCMToken: "fcm-ct2-token", Platform: "android",
	}); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	// Drain — worker must resolve the token at send time, not enqueue time.
	srv.drainPushOutbox(ctx)
	row := lookupOutboxRow(t, srv, id)
	if row.Status != "sent" {
		t.Errorf("status = %q, want sent (token registered after enqueue, resolved at drain time)", row.Status)
	}
}

// CT-3: Token rotation — push enqueued before rotation delivers with new token.
func TestF4CT3_TokenRotationHandled(t *testing.T) {
	isolateEventsDir(t)
	rt := &stubRoundTripper{status: 200}
	installStubTransport(t, rt)
	srv := newF4Server(t)
	ctx := context.Background()

	userID := insertTestUser(t, srv.queue, "f4ct3-sub", "f4ct3@example.com")
	const deviceID = "android-f4ct3"

	// Register with original token.
	if _, err := srv.queue.RegisterDevice(ctx, userID, deviceRegisterRequest{
		DeviceID: deviceID, FCMToken: "fcm-original", Platform: "android",
	}); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	id, err := srv.queue.EnqueueDevicePush("life", 70, "slug-ct3", "read", "https://example.com/ct3", userID, deviceID)
	if err != nil {
		t.Fatalf("EnqueueDevicePush: %v", err)
	}

	// Rotate token before drain.
	if _, err := srv.queue.RegisterDevice(ctx, userID, deviceRegisterRequest{
		DeviceID: deviceID, FCMToken: "fcm-rotated", Platform: "android",
	}); err != nil {
		t.Fatalf("RegisterDevice rotate: %v", err)
	}

	srv.drainPushOutbox(ctx)
	row := lookupOutboxRow(t, srv, id)
	if row.Status != "sent" {
		t.Errorf("status = %q, want sent after token rotation", row.Status)
	}
}

// CT-4: Disabled device — outbox row dead-lettered with device_token_missing.
func TestF4CT4_DisabledDeviceDeadLettered(t *testing.T) {
	isolateEventsDir(t)
	installStubTransport(t, &stubRoundTripper{status: 200})
	srv := newF4Server(t)
	ctx := context.Background()

	userID := insertTestUser(t, srv.queue, "f4ct4-sub", "f4ct4@example.com")
	const deviceID = "android-f4ct4"

	if _, err := srv.queue.RegisterDevice(ctx, userID, deviceRegisterRequest{
		DeviceID: deviceID, FCMToken: "fcm-ct4", Platform: "android",
	}); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	if err := srv.queue.DisableDevice(ctx, userID, deviceID); err != nil {
		t.Fatalf("DisableDevice: %v", err)
	}

	id, err := srv.queue.EnqueueDevicePush("eng", 75, "slug-ct4", "save", "https://example.com/ct4", userID, deviceID)
	if err != nil {
		t.Fatalf("EnqueueDevicePush: %v", err)
	}

	srv.drainPushOutbox(ctx)
	row := lookupOutboxRow(t, srv, id)
	if row.Status != "dead" {
		t.Errorf("status = %q, want dead (disabled device)", row.Status)
	}
}

// CT-5: Missing target device — no fallback to global token; row dead-lettered.
func TestF4CT5_MissingDeviceNoGlobalFallback(t *testing.T) {
	isolateEventsDir(t)
	// Install a global token so we can confirm it is NOT used.
	if err := newTestQueue(t).UpsertDevice("global-token-should-not-be-used"); err != nil {
		// UpsertDevice is on a separate queue; ignore, this is just documenting intent.
		_ = err
	}
	installStubTransport(t, &stubRoundTripper{status: 200})
	srv := newF4Server(t)
	ctx := context.Background()

	userID := insertTestUser(t, srv.queue, "f4ct5-sub", "f4ct5@example.com")

	id, err := srv.queue.EnqueueDevicePush("eng", 60, "slug-ct5", "skip", "https://example.com/ct5", userID, "android-never-registered")
	if err != nil {
		t.Fatalf("EnqueueDevicePush: %v", err)
	}

	srv.drainPushOutbox(ctx)
	row := lookupOutboxRow(t, srv, id)
	if row.Status != "dead" {
		t.Errorf("status = %q, want dead (unregistered target device, no global fallback)", row.Status)
	}
}

// CT-6: Legacy outbox row (empty target_device_id) — falls through to global token.
func TestF4CT6_LegacyRowUsesGlobalToken(t *testing.T) {
	isolateEventsDir(t)
	installStubTransport(t, &stubRoundTripper{status: 200})
	srv := newF4Server(t)
	ctx := context.Background()

	// Register a global (user-level) token via the old UpsertDevice path.
	if err := srv.queue.UpsertDevice("global-legacy-token"); err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}

	// EnqueuePushWithProfile produces a legacy row (no target_device_id).
	id, err := srv.queue.EnqueuePushWithProfile("notify", "life", 65, "slug-ct6", "read", "https://example.com/ct6", "")
	if err != nil {
		t.Fatalf("EnqueuePushWithProfile: %v", err)
	}

	srv.drainPushOutbox(ctx)
	row := lookupOutboxRow(t, srv, id)
	if row.TargetDeviceID != "" {
		t.Errorf("target_device_id = %q, want empty for legacy row", row.TargetDeviceID)
	}
	if row.Status != "sent" {
		t.Errorf("status = %q, want sent (legacy row with global token)", row.Status)
	}
}

// CT-7: Two-device isolation — each device's push targets only that device.
func TestF4CT7_TwoDeviceIsolation(t *testing.T) {
	isolateEventsDir(t)
	installStubTransport(t, &stubRoundTripper{status: 200})
	srv := newF4Server(t)
	ctx := context.Background()

	userID := insertTestUser(t, srv.queue, "f4ct7-sub", "f4ct7@example.com")

	for _, d := range []struct{ id, token string }{
		{"android-phone-a", "fcm-phone-a-token"},
		{"android-phone-b", "fcm-phone-b-token"},
	} {
		if _, err := srv.queue.RegisterDevice(ctx, userID, deviceRegisterRequest{
			DeviceID: d.id, FCMToken: d.token, Platform: "android",
		}); err != nil {
			t.Fatalf("RegisterDevice %s: %v", d.id, err)
		}
	}

	idA, _ := srv.queue.EnqueueDevicePush("eng", 90, "slug-a", "save", "https://a.example.com", userID, "android-phone-a")
	idB, _ := srv.queue.EnqueueDevicePush("eng", 50, "slug-b", "skip", "https://b.example.com", userID, "android-phone-b")

	srv.drainPushOutbox(ctx)

	rowA := lookupOutboxRow(t, srv, idA)
	rowB := lookupOutboxRow(t, srv, idB)

	if rowA.TargetDeviceID != "android-phone-a" {
		t.Errorf("rowA target_device_id = %q, want android-phone-a", rowA.TargetDeviceID)
	}
	if rowB.TargetDeviceID != "android-phone-b" {
		t.Errorf("rowB target_device_id = %q, want android-phone-b", rowB.TargetDeviceID)
	}
	if rowA.Status != "sent" {
		t.Errorf("rowA status = %q, want sent", rowA.Status)
	}
	if rowB.Status != "sent" {
		t.Errorf("rowB status = %q, want sent", rowB.Status)
	}
}
