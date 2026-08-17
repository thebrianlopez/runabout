package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// slotDoctorConfig writes config.toml with an optional [server.youtube.accounts] stanza.
// accountsToml is appended verbatim after the [server] block.
func slotDoctorConfig(t *testing.T, cfgDir, queuePath, accountsToml string) string {
	t.Helper()
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir cfgDir: %v", err)
	}
	tomlPath := filepath.Join(cfgDir, "config.toml")
	content := "[server]\n" +
		"token = \"test-literal-token\"\n" +
		fmt.Sprintf("queue_db = %q\n", queuePath) +
		accountsToml
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	return tomlPath
}

// CT-9: two configured slots emit two independent youtube_oauth[slot=*] checks.
func TestDoctorYouTubeSlots_CT9_TwoSlots(t *testing.T) {
	dir := t.TempDir()
	queuePath := filepath.Join(t.TempDir(), "q.db")
	cfgDir := filepath.Join(dir, ".config", "linkari")
	tomlPath := slotDoctorConfig(t, cfgDir, queuePath, `
[server.youtube.accounts.personal]
slot = "personal"
sources = ["watch_later"]

[server.youtube.accounts.work]
slot = "work"
sources = ["liked"]
`)

	out, run := newDoctorCmdForTestWith(t, dir, []string{"--path", tomlPath}, doctorDeps{ProbeYouTubeSlot: func(_ context.Context, _ string, _ int64, _ *Queue, _, _ string) error {
		return nil
	}})
	_ = run()
	got := out.String()

	if !strings.Contains(got, "youtube_oauth[slot=personal]") {
		t.Errorf("CT-9: expected personal slot check, got:\n%s", got)
	}
	if !strings.Contains(got, "youtube_oauth[slot=work]") {
		t.Errorf("CT-9: expected work slot check, got:\n%s", got)
	}
}

// CT-9b: both slots pass - two ok results.
func TestDoctorYouTubeSlots_CT9b_BothOk(t *testing.T) {
	dir := t.TempDir()
	queuePath := filepath.Join(t.TempDir(), "q.db")
	cfgDir := filepath.Join(dir, ".config", "linkari")
	tomlPath := slotDoctorConfig(t, cfgDir, queuePath, `
[server.youtube.accounts.personal]
slot = "personal"
sources = ["watch_later"]

[server.youtube.accounts.work]
slot = "work"
sources = ["liked"]
`)

	out, run := newDoctorCmdForTestWith(t, dir, []string{"--path", tomlPath}, doctorDeps{ProbeYouTubeSlot: func(_ context.Context, _ string, _ int64, _ *Queue, _, _ string) error {
		return nil
	}})
	_ = run()
	got := out.String()

	if !strings.Contains(got, "✓ youtube_oauth[slot=personal]") {
		t.Errorf("CT-9b: expected ok for personal slot, got:\n%s", got)
	}
	if !strings.Contains(got, "✓ youtube_oauth[slot=work]") {
		t.Errorf("CT-9b: expected ok for work slot, got:\n%s", got)
	}
}

// CT-9c: one slot ok, one slot invalid_grant - two checks with different statuses.
func TestDoctorYouTubeSlots_CT9c_OneOkOneExpired(t *testing.T) {
	dir := t.TempDir()
	queuePath := filepath.Join(t.TempDir(), "q.db")
	cfgDir := filepath.Join(dir, ".config", "linkari")
	tomlPath := slotDoctorConfig(t, cfgDir, queuePath, `
[server.youtube.accounts.personal]
slot = "personal"
sources = ["watch_later"]

[server.youtube.accounts.expired]
slot = "expired"
sources = ["liked"]
`)

	out, run := newDoctorCmdForTestWith(t, dir, []string{"--path", tomlPath}, doctorDeps{ProbeYouTubeSlot: func(_ context.Context, slot string, _ int64, _ *Queue, _, _ string) error {
		if slot == "expired" {
			return fmt.Errorf("oauth2: token refresh failed: invalid_grant")
		}
		return nil
	}})
	_ = run()
	got := out.String()

	if !strings.Contains(got, "✓ youtube_oauth[slot=personal]") {
		t.Errorf("CT-9c: expected ok for personal slot, got:\n%s", got)
	}
	if !strings.Contains(got, "✗ youtube_oauth[slot=expired]") {
		t.Errorf("CT-9c: expected fail for expired slot, got:\n%s", got)
	}
}

// CT-10 / RG-10: no [server.youtube.accounts] config - single youtube_oauth check emitted.
func TestDoctorYouTubeSlots_CT10_NoAccountsConfig(t *testing.T) {
	dir := t.TempDir()
	queuePath := filepath.Join(t.TempDir(), "q.db")
	cfgDir := filepath.Join(dir, ".config", "linkari")
	// No accounts stanza; enable a YouTube source so the legacy path fires.
	tomlPath := slotDoctorConfig(t, cfgDir, queuePath, `
[server.sources]
youtube_watch_later_enabled = true
`)

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath})
	_ = run()
	got := out.String()

	// RG-10: check name is youtube_oauth, NOT youtube_oauth[slot=default]
	if !strings.Contains(got, "youtube_oauth") {
		t.Errorf("CT-10: expected youtube_oauth check, got:\n%s", got)
	}
	if strings.Contains(got, "youtube_oauth[slot=") {
		t.Errorf("RG-10: expected no per-slot check names when no accounts configured, got:\n%s", got)
	}
}

// CT-10b: configured slot with no stored token - youtube_slot_missing warn with --slot hint.
func TestDoctorYouTubeSlots_CT10b_SlotMissingToken(t *testing.T) {
	dir := t.TempDir()
	queuePath := filepath.Join(t.TempDir(), "q.db")
	cfgDir := filepath.Join(dir, ".config", "linkari")
	tomlPath := slotDoctorConfig(t, cfgDir, queuePath, `
[server.youtube.accounts.personal]
slot = "personal"
sources = ["watch_later"]
`)

	out, run := newDoctorCmdForTestWith(t, dir, []string{"--path", tomlPath}, doctorDeps{ProbeYouTubeSlot: func(_ context.Context, _ string, _ int64, _ *Queue, _, _ string) error {
		return sql.ErrNoRows
	}})
	_ = run()
	got := out.String()

	if !strings.Contains(got, "youtube_slot_missing") {
		t.Errorf("CT-10b: expected youtube_slot_missing warn check, got:\n%s", got)
	}
	if !strings.Contains(got, "--slot personal") {
		t.Errorf("CT-10b: expected --slot personal remediation hint, got:\n%s", got)
	}
}

// CT-10c: same source in two account blocks - youtube_slot_conflict error check emitted.
func TestDoctorYouTubeSlots_CT10c_SlotConflict(t *testing.T) {
	dir := t.TempDir()
	queuePath := filepath.Join(t.TempDir(), "q.db")
	cfgDir := filepath.Join(dir, ".config", "linkari")
	tomlPath := slotDoctorConfig(t, cfgDir, queuePath, `
[server.youtube.accounts.personal]
slot = "personal"
sources = ["watch_later"]

[server.youtube.accounts.work]
slot = "work"
sources = ["watch_later"]
`)

	out, run := newDoctorCmdForTestWith(t, dir, []string{"--path", tomlPath}, doctorDeps{ProbeYouTubeSlot: func(_ context.Context, _ string, _ int64, _ *Queue, _, _ string) error {
		return nil
	}})
	_ = run()
	got := out.String()

	if !strings.Contains(got, "youtube_slot_conflict") {
		t.Errorf("CT-10c: expected youtube_slot_conflict check, got:\n%s", got)
	}
}

// RG-11: remediation hint contains --slot <name> with correct slot name.
func TestDoctorYouTubeSlots_RG11_RemediationHintSlotName(t *testing.T) {
	dir := t.TempDir()
	queuePath := filepath.Join(t.TempDir(), "q.db")
	cfgDir := filepath.Join(dir, ".config", "linkari")
	tomlPath := slotDoctorConfig(t, cfgDir, queuePath, `
[server.youtube.accounts.personal]
slot = "personal"
sources = ["watch_later"]
`)

	out, run := newDoctorCmdForTestWith(t, dir, []string{"--path", tomlPath}, doctorDeps{ProbeYouTubeSlot: func(_ context.Context, _ string, _ int64, _ *Queue, _, _ string) error {
		return fmt.Errorf("oauth2: token refresh failed: invalid_grant")
	}})
	_ = run()
	got := out.String()

	if !strings.Contains(got, "--slot personal") {
		t.Errorf("RG-11: expected --slot personal in remediation hint, got:\n%s", got)
	}
}

// RG-12: per-slot checks are independent - statuses come from separate probe calls.
func TestDoctorYouTubeSlots_RG12_SlotIndependence(t *testing.T) {
	dir := t.TempDir()
	queuePath := filepath.Join(t.TempDir(), "q.db")
	cfgDir := filepath.Join(dir, ".config", "linkari")
	tomlPath := slotDoctorConfig(t, cfgDir, queuePath, `
[server.youtube.accounts.personal]
slot = "personal"
sources = ["watch_later"]

[server.youtube.accounts.default]
slot = "default"
sources = ["liked"]
`)

	out, run := newDoctorCmdForTestWith(t, dir, []string{"--path", tomlPath}, doctorDeps{ProbeYouTubeSlot: func(_ context.Context, slot string, _ int64, _ *Queue, _, _ string) error {
		if slot == "personal" {
			return fmt.Errorf("oauth2: token refresh failed: invalid_grant")
		}
		return nil // default slot ok
	}})
	_ = run()
	got := out.String()

	// personal slot fails, default slot succeeds - they are independent
	if !strings.Contains(got, "✗ youtube_oauth[slot=personal]") {
		t.Errorf("RG-12: expected personal slot to fail, got:\n%s", got)
	}
	if !strings.Contains(got, "✓ youtube_oauth[slot=default]") {
		t.Errorf("RG-12: expected default slot to succeed independently, got:\n%s", got)
	}
}
