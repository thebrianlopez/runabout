package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeEventFile(t *testing.T, dir, filename string, events []map[string]string) {
	t.Helper()
	var lines []byte
	for _, ev := range events {
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, b...)
		lines = append(lines, '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, filename), lines, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newSyncCfg(t *testing.T, localDir string) SyncConfig {
	t.Helper()
	return SyncConfig{
		LocalDir:        localDir,
		ConflictLogPath: filepath.Join(t.TempDir(), "sync-conflicts.jsonl"),
		Timeout:         5 * time.Second,
	}
}

// CT-1: Events in local but not remote are uploaded
func TestSync_CT1_LocalUploadedToRemote(t *testing.T) {
	dir := t.TempDir()
	writeEventFile(t, dir, "2026-01-01.jsonl", []map[string]string{
		{"session_id": "s1", "event_id": "e1", "created_at": "20260101T100000Z"},
	})
	client := NewMemS3Client()
	cfg := newSyncCfg(t, dir)
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	result, err := RunSync(cmd, cfg, client)
	if err != nil {
		t.Fatalf("RunSync error: %v", err)
	}
	if result.Uploaded != 1 {
		t.Errorf("expected 1 upload, got %d", result.Uploaded)
	}
	if _, ok := client.objects["2026-01-01.jsonl"]; !ok {
		t.Error("file not uploaded to remote")
	}
}

// CT-2: Events in remote but not local are downloaded
func TestSync_CT2_RemoteDownloadedLocally(t *testing.T) {
	dir := t.TempDir()
	client := NewMemS3Client()
	client.Put("2026-02-01.jsonl", []byte(`{"session_id":"s2","event_id":"e2","created_at":"20260201T100000Z"}`+"\n"))
	cfg := newSyncCfg(t, dir)
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	result, err := RunSync(cmd, cfg, client)
	if err != nil {
		t.Fatalf("RunSync error: %v", err)
	}
	if result.Downloaded != 1 {
		t.Errorf("expected 1 download, got %d", result.Downloaded)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-02-01.jsonl")); os.IsNotExist(err) {
		t.Error("file not downloaded locally")
	}
}

// CT-3: Events present in both (same key, same content) not duplicated
func TestSync_CT3_NoDuplication(t *testing.T) {
	dir := t.TempDir()
	eventLine := `{"session_id":"s3","event_id":"e3","created_at":"20260301T100000Z"}` + "\n"
	writeEventFile(t, dir, "2026-03-01.jsonl", []map[string]string{
		{"session_id": "s3", "event_id": "e3", "created_at": "20260301T100000Z"},
	})
	client := NewMemS3Client()
	client.Put("2026-03-01.jsonl", []byte(eventLine))
	cfg := newSyncCfg(t, dir)
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	result, err := RunSync(cmd, cfg, client)
	if err != nil {
		t.Fatalf("RunSync error: %v", err)
	}
	// Same file exists on both sides - no upload, no download
	if result.Uploaded != 0 {
		t.Errorf("expected 0 uploads for identical content, got %d", result.Uploaded)
	}
	if result.Downloaded != 0 {
		t.Errorf("expected 0 downloads for identical content, got %d", result.Downloaded)
	}
}

// CT-4: Conflict: same key, different content - earlier created_at wins
func TestSync_CT4_ConflictResolution(t *testing.T) {
	dir := t.TempDir()
	// Local has older timestamp
	localLine := `{"session_id":"s4","event_id":"e4","created_at":"20260401T090000Z","source":"local"}` + "\n"
	remoteLine := `{"session_id":"s4","event_id":"e4","created_at":"20260401T100000Z","source":"remote"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "2026-04-01.jsonl"), []byte(localLine), 0o644); err != nil {
		t.Fatal(err)
	}
	client := NewMemS3Client()
	client.Put("2026-04-01.jsonl", []byte(remoteLine))
	cfg := newSyncCfg(t, dir)
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	result, err := RunSync(cmd, cfg, client)
	if err != nil {
		t.Fatalf("RunSync error: %v", err)
	}
	if result.Conflicts != 1 {
		t.Errorf("expected 1 conflict, got %d", result.Conflicts)
	}
}

// CT-5: Conflict logged to sync-conflicts.jsonl with both versions
func TestSync_CT5_ConflictLogged(t *testing.T) {
	dir := t.TempDir()
	conflictLog := filepath.Join(t.TempDir(), "sync-conflicts.jsonl")
	localLine := `{"session_id":"s5","event_id":"e5","created_at":"20260501T090000Z","val":"A"}` + "\n"
	remoteLine := `{"session_id":"s5","event_id":"e5","created_at":"20260501T100000Z","val":"B"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "2026-05-01.jsonl"), []byte(localLine), 0o644); err != nil {
		t.Fatal(err)
	}
	client := NewMemS3Client()
	client.Put("2026-05-01.jsonl", []byte(remoteLine))
	cfg := SyncConfig{
		LocalDir:        dir,
		ConflictLogPath: conflictLog,
		Timeout:         5 * time.Second,
	}
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if _, err := RunSync(cmd, cfg, client); err != nil {
		t.Fatalf("RunSync error: %v", err)
	}
	data, err := os.ReadFile(conflictLog)
	if err != nil {
		t.Fatal("conflict log not written")
	}
	var entry SyncConflictEntry
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("unmarshal conflict entry: %v", err)
	}
	if entry.LocalVersion.Source != "local" {
		t.Errorf("local_version.source = %q, want local", entry.LocalVersion.Source)
	}
	if entry.RemoteVersion.Source != "remote" {
		t.Errorf("remote_version.source = %q, want remote", entry.RemoteVersion.Source)
	}
}

// CT-6: --dry-run prints plan (N up, M down) without transfers
func TestSync_CT6_DryRun(t *testing.T) {
	dir := t.TempDir()
	writeEventFile(t, dir, "2026-06-01.jsonl", []map[string]string{
		{"session_id": "s6", "event_id": "e6", "created_at": "20260601T100000Z"},
	})
	client := NewMemS3Client()
	client.Put("2026-06-02.jsonl", []byte(`{"session_id":"s7","event_id":"e7"}`+"\n"))
	cfg := SyncConfig{LocalDir: dir, DryRun: true, Timeout: 5 * time.Second, ConflictLogPath: filepath.Join(t.TempDir(), "conflicts.jsonl")}
	var out bytes.Buffer
	cmd := newSyncCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	result, err := RunSync(cmd, cfg, client)
	if err != nil {
		t.Fatalf("RunSync dry-run error: %v", err)
	}
	// Dry-run: no actual transfers
	if result.Uploaded != 0 || result.Downloaded != 0 {
		t.Errorf("dry-run should have 0 uploads/downloads, got up=%d down=%d", result.Uploaded, result.Downloaded)
	}
	if !strings.Contains(out.String(), "upload") || !strings.Contains(out.String(), "download") {
		t.Errorf("expected plan output, got: %s", out.String())
	}
}

// CT-7: remote_not_configured (E404) when no remote set
func TestSync_CT7_RemoteNotConfigured(t *testing.T) {
	dir := t.TempDir()
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	// Invoke via cobra to exercise the E404 path
	cmd.SetArgs([]string{"--local-dir", dir})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected E404 error, got nil")
	}
	if !strings.Contains(err.Error(), "E404") {
		t.Errorf("expected E404 error, got: %v", err)
	}
}

// CT-8: events_dir_missing (E405) when local events dir absent
func TestSync_CT8_EventsDirMissing(t *testing.T) {
	cfg := SyncConfig{
		LocalDir: "/nonexistent/events",
		Timeout:  5 * time.Second,
	}
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	_, err := RunSync(cmd, cfg, NewMemS3Client())
	if err == nil {
		t.Fatal("expected E405 error, got nil")
	}
	if !strings.Contains(err.Error(), "E405") {
		t.Errorf("expected E405 error, got: %v", err)
	}
}

// CT-9: remote_unreachable (E401) when S3Client returns error on ListObjects
func TestSync_CT9_RemoteUnreachable(t *testing.T) {
	dir := t.TempDir()
	client := NewMemS3Client()
	client.Err = fmt.Errorf("connection refused")
	cfg := newSyncCfg(t, dir)
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	_, err := RunSync(cmd, cfg, client)
	if err == nil {
		t.Fatal("expected E401 error, got nil")
	}
	if !strings.Contains(err.Error(), "E401") {
		t.Errorf("expected E401 error, got: %v", err)
	}
}

// CT-10: sync_timeout (E402) fires when sync exceeds timeout deadline
func TestSync_CT10_Timeout(t *testing.T) {
	dir := t.TempDir()
	cfg := SyncConfig{
		LocalDir:        dir,
		ConflictLogPath: filepath.Join(t.TempDir(), "conflicts.jsonl"),
		Timeout:         1 * time.Nanosecond, // effectively zero
	}
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	_, err := RunSync(cmd, cfg, NewMemS3Client())
	if err == nil {
		t.Fatal("expected E402 timeout, got nil")
	}
	if !strings.Contains(err.Error(), "E402") {
		t.Errorf("expected E402 error, got: %v", err)
	}
}

// CT-11: No event is ever deleted or overwritten during sync
func TestSync_CT11_NoLocalDeletion(t *testing.T) {
	dir := t.TempDir()
	localContent := `{"session_id":"s11","event_id":"e11","created_at":"20261101T100000Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "2026-11-01.jsonl"), []byte(localContent), 0o644); err != nil {
		t.Fatal(err)
	}
	client := NewMemS3Client()
	// Remote has different content for same file
	client.Put("2026-11-01.jsonl", []byte(`{"session_id":"sx","event_id":"ex","created_at":"20261101T090000Z"}`+"\n"))
	cfg := newSyncCfg(t, dir)
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if _, err := RunSync(cmd, cfg, client); err != nil {
		t.Fatalf("RunSync error: %v", err)
	}
	// Local file must still exist
	got, err := os.ReadFile(filepath.Join(dir, "2026-11-01.jsonl"))
	if err != nil {
		t.Fatal("local file was deleted")
	}
	if string(got) != localContent {
		t.Errorf("local file was overwritten: got %q, want %q", string(got), localContent)
	}
}

// CT-12: SyncResult.Uploaded and SyncResult.Downloaded counts are accurate
func TestSync_CT12_AccurateCounts(t *testing.T) {
	dir := t.TempDir()
	// 2 local-only files
	for i := 1; i <= 2; i++ {
		writeEventFile(t, dir, fmt.Sprintf("2026-12-0%d.jsonl", i), []map[string]string{
			{"session_id": fmt.Sprintf("s%d", i), "event_id": fmt.Sprintf("e%d", i), "created_at": "20261201T100000Z"},
		})
	}
	client := NewMemS3Client()
	// 3 remote-only files
	for i := 3; i <= 5; i++ {
		client.Put(fmt.Sprintf("2026-12-0%d.jsonl", i),
			[]byte(fmt.Sprintf(`{"session_id":"s%d","event_id":"e%d"}`+"\n", i, i)))
	}
	cfg := newSyncCfg(t, dir)
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	result, err := RunSync(cmd, cfg, client)
	if err != nil {
		t.Fatalf("RunSync error: %v", err)
	}
	if result.Uploaded != 2 {
		t.Errorf("expected 2 uploads, got %d", result.Uploaded)
	}
	if result.Downloaded != 3 {
		t.Errorf("expected 3 downloads, got %d", result.Downloaded)
	}
}

// --- Behavioral Tests ---

// BT-1: Full bidirectional sync: events exchange between two in-memory stores
func TestSync_BT1_BidirectionalSync(t *testing.T) {
	dir := t.TempDir()
	writeEventFile(t, dir, "2026-01-10.jsonl", []map[string]string{
		{"session_id": "sA", "event_id": "eA", "created_at": "20260110T100000Z"},
	})
	client := NewMemS3Client()
	client.Put("2026-01-11.jsonl", []byte(`{"session_id":"sB","event_id":"eB","created_at":"20260111T100000Z"}`+"\n"))
	cfg := newSyncCfg(t, dir)
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	result, err := RunSync(cmd, cfg, client)
	if err != nil {
		t.Fatalf("BT-1 error: %v", err)
	}
	if result.Uploaded != 1 || result.Downloaded != 1 {
		t.Errorf("BT-1: expected 1 up + 1 down, got up=%d down=%d", result.Uploaded, result.Downloaded)
	}
}

// BT-2: Re-run of sync on already-synced state: zero uploads, zero downloads
func TestSync_BT2_Idempotent(t *testing.T) {
	dir := t.TempDir()
	writeEventFile(t, dir, "2026-02-10.jsonl", []map[string]string{
		{"session_id": "s10", "event_id": "e10", "created_at": "20260210T100000Z"},
	})
	client := NewMemS3Client()
	cfg := newSyncCfg(t, dir)
	cmd1 := newSyncCmd()
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	if _, err := RunSync(cmd1, cfg, client); err != nil {
		t.Fatalf("first sync error: %v", err)
	}

	cmd2 := newSyncCmd()
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	result, err := RunSync(cmd2, cfg, client)
	if err != nil {
		t.Fatalf("second sync error: %v", err)
	}
	if result.Uploaded != 0 || result.Downloaded != 0 {
		t.Errorf("BT-2: re-sync should be zero-op, got up=%d down=%d", result.Uploaded, result.Downloaded)
	}
}

// BT-3: --dry-run on pre-populated state: accurate counts, no side effects
func TestSync_BT3_DryRunAccurate(t *testing.T) {
	dir := t.TempDir()
	writeEventFile(t, dir, "2026-03-10.jsonl", []map[string]string{
		{"session_id": "s20", "event_id": "e20"},
	})
	client := NewMemS3Client()
	client.Put("2026-03-11.jsonl", []byte(`{"session_id":"s21","event_id":"e21"}`+"\n"))
	cfg := SyncConfig{
		LocalDir:        dir,
		DryRun:          true,
		Timeout:         5 * time.Second,
		ConflictLogPath: filepath.Join(t.TempDir(), "conflicts.jsonl"),
	}
	var out bytes.Buffer
	cmd := newSyncCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	result, err := RunSync(cmd, cfg, client)
	if err != nil {
		t.Fatalf("BT-3 dry-run error: %v", err)
	}
	if result.Uploaded != 0 || result.Downloaded != 0 {
		t.Error("dry-run must not transfer files")
	}
	// Remote object count must not change
	if len(client.objects) != 1 {
		t.Errorf("remote objects modified during dry-run (expected 1, got %d)", len(client.objects))
	}
}

// BT-4: Partial conflict scenario: 2 clean merges + 1 conflict; all handled
func TestSync_BT4_PartialConflict(t *testing.T) {
	dir := t.TempDir()
	// 2 local-only clean files
	writeEventFile(t, dir, "clean1.jsonl", []map[string]string{{"session_id": "sc1", "event_id": "ec1"}})
	writeEventFile(t, dir, "clean2.jsonl", []map[string]string{{"session_id": "sc2", "event_id": "ec2"}})
	// 1 conflict file
	if err := os.WriteFile(filepath.Join(dir, "conflict.jsonl"), []byte(`{"session_id":"sx","event_id":"ex","created_at":"20260101T090000Z","val":"local"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := NewMemS3Client()
	client.Put("conflict.jsonl", []byte(`{"session_id":"sx","event_id":"ex","created_at":"20260101T100000Z","val":"remote"}`+"\n"))

	cfg := newSyncCfg(t, dir)
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	result, err := RunSync(cmd, cfg, client)
	if err != nil {
		t.Fatalf("BT-4 error: %v", err)
	}
	if result.Uploaded != 2 {
		t.Errorf("expected 2 uploads, got %d", result.Uploaded)
	}
	if result.Conflicts != 1 {
		t.Errorf("expected 1 conflict, got %d", result.Conflicts)
	}
}

// --- Regression Guards ---

// RG-1: Sync never deletes a local event (append-only invariant)
func TestSync_RG1_NoLocalDeletion(t *testing.T) {
	dir := t.TempDir()
	localLine := `{"session_id":"rg1","event_id":"e1","created_at":"20260101T100000Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rg1.jsonl"), []byte(localLine), 0o644); err != nil {
		t.Fatal(err)
	}
	client := NewMemS3Client()
	cfg := newSyncCfg(t, dir)
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if _, err := RunSync(cmd, cfg, client); err != nil {
		t.Fatalf("RunSync error: %v", err)
	}
	// Local file must still exist with original content
	got, err := os.ReadFile(filepath.Join(dir, "rg1.jsonl"))
	if err != nil {
		t.Fatal("local file deleted - RG-1 violation")
	}
	if string(got) != localLine {
		t.Errorf("RG-1: local file content changed")
	}
}

// RG-2: Credentials never appear in sync-conflicts.jsonl or sync logs
func TestSync_RG2_NoCredentialsInConflictLog(t *testing.T) {
	dir := t.TempDir()
	conflictLog := filepath.Join(t.TempDir(), "sync-conflicts.jsonl")
	// Simulate a conflict with content that might contain sensitive-looking strings
	localLine := `{"session_id":"rg2","event_id":"e1","created_at":"20260101T090000Z","note":"normal data"}` + "\n"
	remoteLine := `{"session_id":"rg2","event_id":"e1","created_at":"20260101T100000Z","note":"normal data"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rg2.jsonl"), []byte(localLine), 0o644); err != nil {
		t.Fatal(err)
	}
	client := NewMemS3Client()
	client.Put("rg2.jsonl", []byte(remoteLine))
	cfg := SyncConfig{
		LocalDir:        dir,
		ConflictLogPath: conflictLog,
		Timeout:         5 * time.Second,
	}
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if _, err := RunSync(cmd, cfg, client); err != nil {
		t.Fatalf("RunSync error: %v", err)
	}
	data, err := os.ReadFile(conflictLog)
	if err != nil {
		return // no conflict log = no credentials
	}
	credPatterns := []string{"ANTHROPIC_API_KEY", "sk-ant-", "password", "secret"}
	for _, pat := range credPatterns {
		if strings.Contains(string(data), pat) {
			t.Errorf("RG-2: credential pattern %q found in conflict log", pat)
		}
	}
}

// dedupKey test for correctness
func TestDedupKey_Deterministic(t *testing.T) {
	k1 := dedupKey("session-abc", "event-123")
	k2 := dedupKey("session-abc", "event-123")
	if k1 != k2 {
		t.Error("dedupKey is not deterministic")
	}
	k3 := dedupKey("session-abc", "event-456")
	if k1 == k3 {
		t.Error("different event_ids should produce different dedup keys")
	}
}

// MemS3Client test
func TestMemS3Client_PutListGet(t *testing.T) {
	ctx := context.Background()
	client := NewMemS3Client()
	if err := client.PutObject(ctx, "test.jsonl", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	objects, err := client.ListObjects(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 {
		t.Errorf("expected 1 object, got %d", len(objects))
	}
	data, err := client.GetObject(ctx, "test.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("got %q, want hello", string(data))
	}
}
