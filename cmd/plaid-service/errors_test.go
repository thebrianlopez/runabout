package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	plaid "github.com/plaid/plaid-go/v29/plaid"
)

func plaidErr(errorType, errorCode string) plaid.PlaidError {
	return plaid.PlaidError{
		ErrorType:    plaid.PlaidErrorType(errorType),
		ErrorCode:    errorCode,
		ErrorMessage: "test error",
	}
}

// ── CT-1: ITEM_LOGIN_REQUIRED → vendor_auth_required ─────────────────────────

func TestErrCT1_ItemLoginRequired(t *testing.T) {
	if got := classifyPlaidError(plaidErr("ITEM_ERROR", "ITEM_LOGIN_REQUIRED")); got != "vendor_auth_required" {
		t.Errorf("got %q, want vendor_auth_required", got)
	}
}

// ── CT-2: INSTITUTION_DOWN → vendor_unavailable ───────────────────────────────

func TestErrCT2_InstitutionDown(t *testing.T) {
	if got := classifyPlaidError(plaidErr("INSTITUTION_ERROR", "INSTITUTION_DOWN")); got != "vendor_unavailable" {
		t.Errorf("got %q, want vendor_unavailable", got)
	}
}

// ── CT-3: RATE_LIMIT_EXCEEDED → vendor_rate_limited ──────────────────────────

func TestErrCT3_RateLimitExceeded(t *testing.T) {
	if got := classifyPlaidError(plaidErr("RATE_LIMIT_EXCEEDED", "RATE_LIMIT_EXCEEDED")); got != "vendor_rate_limited" {
		t.Errorf("got %q, want vendor_rate_limited", got)
	}
}

// ── CT-4: unknown type/code → vendor_unknown ──────────────────────────────────

func TestErrCT4_UnknownCodeReturnsVendorUnknown(t *testing.T) {
	if got := classifyPlaidError(plaidErr("UNKNOWN_TYPE", "FOOBAR")); got != "vendor_unknown" {
		t.Errorf("got %q, want vendor_unknown", got)
	}
}

// ── CT-5: zero-value PlaidError → vendor_unknown (no panic) ──────────────────

func TestErrCT5_ZeroValueNoDefault(t *testing.T) {
	got := classifyPlaidError(plaid.PlaidError{})
	if got != "vendor_unknown" {
		t.Errorf("got %q, want vendor_unknown", got)
	}
}

// ── CT-6: emitToolFailure writes tool_failure event with required fields ──────

func TestErrCT6_EmitToolFailureFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	emitToolFailure("run_1", "item_1", "vendor_auth_required", "ITEM_LOGIN_REQUIRED", "bad token")

	events := readEvents(t, dir)
	failures := eventsOfType(events, "tool_failure")
	if len(failures) != 1 {
		t.Fatalf("expected 1 tool_failure, got %d", len(failures))
	}
	meta := failures[0]["metadata"].(map[string]any)
	for _, field := range []string{"tool_name", "error_class", "error_code", "error_message", "item_id"} {
		if _, ok := meta[field]; !ok {
			t.Errorf("tool_failure metadata missing field %q", field)
		}
	}
	if meta["error_class"] != "vendor_auth_required" {
		t.Errorf("error_class: got %v, want vendor_auth_required", meta["error_class"])
	}
}

// ── CT-7: error_message truncated to 200 chars ────────────────────────────────

func TestErrCT7_ErrorMessageTruncated(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	long := string(make([]byte, 300))
	for i := range long {
		long = long[:i] + "x" + long[i+1:]
	}
	emitToolFailure("run_1", "item_1", "vendor_unknown", "UNKNOWN", long)

	events := readEvents(t, dir)
	failures := eventsOfType(events, "tool_failure")
	if len(failures) == 0 {
		t.Fatal("no tool_failure emitted")
	}
	meta := failures[0]["metadata"].(map[string]any)
	msg, _ := meta["error_message"].(string)
	if len(msg) > 200 {
		t.Errorf("error_message len %d exceeds 200", len(msg))
	}
}

// ── CT-8: vendor_auth_required sets plaid_items.status = login_required ───────

func TestErrCT8_AuthRequiredSetsLoginRequired(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	s := newScheduler(db, &mockPlaidClient{}, nil)
	s.handleSyncError(context.Background(), "run_1", "item_1",
		&SyncError{EventClass: "vendor_auth_required", Err: errStub("login required")})

	var status string
	db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_1'`).Scan(&status)
	if status != "login_required" {
		t.Errorf("status: got %q, want login_required", status)
	}
}

// ── CT-9: vendor_auth_required does NOT write backoff entry ───────────────────

func TestErrCT9_AuthRequiredNoBackoff(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	s := newScheduler(db, &mockPlaidClient{}, nil)
	s.handleSyncError(context.Background(), "run_1", "item_1",
		&SyncError{EventClass: "vendor_auth_required", Err: errStub("login required")})

	var retries int
	var nextSync string
	db.QueryRow(`SELECT retries, COALESCE(next_sync_at, '') FROM plaid_sync_state WHERE item_id = 'item_1'`).Scan(&retries, &nextSync)
	if retries != 0 {
		t.Errorf("retries: got %d, want 0 (no backoff for auth errors)", retries)
	}
	if nextSync != "" {
		t.Errorf("next_sync_at should not be set for auth error, got %q", nextSync)
	}
}

// ── BT-1: session_id in tool_failure equals sync_run_id ──────────────────────

func TestErrBT1_SessionIDEqualsRunID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	emitToolFailure("my-run-id", "item_1", "vendor_unavailable", "INSTITUTION_DOWN", "down")

	events := readEvents(t, dir)
	for _, e := range eventsOfType(events, "tool_failure") {
		if sid, _ := e["session_id"].(string); sid != "my-run-id" {
			t.Errorf("session_id: got %q, want my-run-id", sid)
		}
	}
}

// ── BT-2: INSTITUTION_NOT_RESPONDING → vendor_unavailable ────────────────────

func TestErrBT2_InstitutionNotResponding(t *testing.T) {
	if got := classifyPlaidError(plaidErr("INSTITUTION_ERROR", "INSTITUTION_NOT_RESPONDING")); got != "vendor_unavailable" {
		t.Errorf("got %q, want vendor_unavailable", got)
	}
}

// ── BT-3: INVALID_ACCESS_TOKEN → vendor_auth_required + login_required status ─

func TestErrBT3_InvalidAccessTokenEscalates(t *testing.T) {
	if got := classifyPlaidError(plaidErr("ITEM_ERROR", "INVALID_ACCESS_TOKEN")); got != "vendor_auth_required" {
		t.Errorf("classifyPlaidError: got %q, want vendor_auth_required", got)
	}

	db := mustOpenDB(t)
	seedItem(t, db, "item_1")
	s := newScheduler(db, &mockPlaidClient{}, nil)
	s.handleSyncError(context.Background(), "run_1", "item_1",
		&SyncError{EventClass: "vendor_auth_required", Err: errStub("invalid token")})

	var status string
	db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_1'`).Scan(&status)
	if status != "login_required" {
		t.Errorf("status: got %q, want login_required", status)
	}
}

// ── BT-4: vendor_unavailable does NOT change plaid_items.status ──────────────

func TestErrBT4_UnavailableDoesNotChangeStatus(t *testing.T) {
	db := mustOpenDB(t)
	seedItem(t, db, "item_1")

	s := newScheduler(db, &mockPlaidClient{}, nil)
	s.handleSyncError(context.Background(), "run_1", "item_1",
		&SyncError{EventClass: "vendor_unavailable", Err: errStub("institution down")})

	var status string
	db.QueryRow(`SELECT status FROM plaid_items WHERE item_id = 'item_1'`).Scan(&status)
	if status != "active" {
		t.Errorf("status: got %q, want active (transient error must not escalate)", status)
	}
}

// ── RG-1: arbitrary codes never panic; always return a string ────────────────

func TestErrRG1_UnknownCodesNoPanic(t *testing.T) {
	codes := []struct{ t, c string }{
		{"", ""},
		{"FOOBAR", "BAZZLE"},
		{"ITEM_ERROR", ""},
		{"", "SOMETHING"},
		{"NUMERIC_123", "456_789"},
		{"VERY_LONG_" + string(make([]byte, 100)), "ALSO_LONG"},
	}
	for _, tc := range codes {
		got := classifyPlaidError(plaidErr(tc.t, tc.c))
		if got == "" {
			t.Errorf("classifyPlaidError(%q, %q) returned empty string", tc.t, tc.c)
		}
	}
}

// ── RG-2: tool_failure emitted even for vendor_unknown errors ─────────────────

func TestErrRG2_ToolFailureEmittedForVendorUnknown(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTOMATION_METRICS_DIR", dir)

	emitToolFailure("run_1", "item_1", "vendor_unknown", "WEIRD_CODE", "something weird")

	failures := eventsOfType(readEvents(t, dir), "tool_failure")
	if len(failures) == 0 {
		t.Error("tool_failure must be emitted even for vendor_unknown errors")
	}
	if cls := failures[0]["metadata"].(map[string]any)["error_class"]; cls != "vendor_unknown" {
		t.Errorf("error_class: got %v, want vendor_unknown", cls)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

type errStub string

func (e errStub) Error() string { return string(e) }

// readEventsAt reads JSONL events from a specific date file for time-sensitive tests.
func readEventsAt(t *testing.T, dir string, date time.Time) []map[string]any {
	t.Helper()
	path := filepath.Join(dir, "events", date.UTC().Format("2006-01-02")+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read events: %v", err)
	}
	_ = data
	return readEvents(t, dir)
}
