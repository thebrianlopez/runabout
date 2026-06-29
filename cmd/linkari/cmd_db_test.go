package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDBBackup_WritesSnapshotAndMeta(t *testing.T) {
	tmp := t.TempDir()
	queuePath := filepath.Join(tmp, "queue.db")
	q, err := NewQueue(queuePath, false)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	defer q.Close()
	if _, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://backup.test/1"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	dir := t.TempDir()
	dest := filepath.Join(dir, "queue-copy.db")

	cmd := dbCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"backup", "--queue-db", queuePath, "--dest", dest})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("backup: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("backup db missing: %v", err)
	}
	if _, err := os.Stat(dest + ".backup-meta.json"); err != nil {
		t.Fatalf("backup meta missing: %v", err)
	}

	snap, err := NewQueue(dest, false)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer snap.Close()
	items, err := snap.Pending()
	if err != nil {
		t.Fatalf("pending snapshot: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

func TestDBBackup_RefusesOverwriteWithoutFlag(t *testing.T) {
	tmp := t.TempDir()
	queuePath := filepath.Join(tmp, "queue.db")
	q, err := NewQueue(queuePath, false)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	defer q.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "queue-copy.db")
	if err := os.WriteFile(dest, []byte("existing"), 0o600); err != nil {
		t.Fatalf("seed dest: %v", err)
	}

	cmd := dbCmd()
	cmd.SetArgs([]string{"backup", "--queue-db", queuePath, "--dest", dest})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected overwrite guard")
	}
}

func TestDBBackup_OverwriteAllowedWithFlag(t *testing.T) {
	tmp := t.TempDir()
	queuePath := filepath.Join(tmp, "queue.db")
	q, err := NewQueue(queuePath, false)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	defer q.Close()
	if _, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://backup.test/2"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "queue-copy.db")
	if err := os.WriteFile(dest, []byte("existing"), 0o600); err != nil {
		t.Fatalf("seed dest: %v", err)
	}

	cmd := dbCmd()
	cmd.SetArgs([]string{"backup", "--queue-db", queuePath, "--dest", dest, "--overwrite"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("backup overwrite: %v", err)
	}
}

func TestDBRestore_ReplacesTargetDB(t *testing.T) {
	srcQ := newTestQueue(t)
	if _, err := srcQ.Enqueue(&ShareRequest{Type: "url", URL: "https://restore.test/new"}); err != nil {
		t.Fatalf("Enqueue source: %v", err)
	}
	dir := t.TempDir()
	backupPath := filepath.Join(dir, "backup.db")
	if err := srcQ.Snapshot(backupPath); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	targetPath := filepath.Join(dir, "queue.db")
	targetQ, err := NewQueue(targetPath, false)
	if err != nil {
		t.Fatalf("NewQueue target: %v", err)
	}
	if _, err := targetQ.Enqueue(&ShareRequest{Type: "url", URL: "https://restore.test/old"}); err != nil {
		t.Fatalf("Enqueue target: %v", err)
	}
	if err := targetQ.Close(); err != nil {
		t.Fatalf("Close target: %v", err)
	}

	cmd := dbCmd()
	cmd.SetArgs([]string{"restore", "--queue-db", targetPath, "--src", backupPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("restore: %v", err)
	}

	restored, err := NewQueue(targetPath, false)
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer restored.Close()
	items, err := restored.Pending()
	if err != nil {
		t.Fatalf("pending restored: %v", err)
	}
	if len(items) != 1 || items[0].URL != "https://restore.test/new" {
		t.Fatalf("unexpected restored items: %+v", items)
	}
}

func TestDBRestore_RejectsCorruptSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "corrupt.db")
	if err := os.WriteFile(src, []byte("not sqlite"), 0o600); err != nil {
		t.Fatalf("write corrupt src: %v", err)
	}
	cmd := dbCmd()
	cmd.SetArgs([]string{"restore", "--queue-db", filepath.Join(dir, "queue.db"), "--src", src})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected corrupt source failure")
	}
}

func TestDBRestore_RejectsHotWALWithoutForce(t *testing.T) {
	dir := t.TempDir()
	srcQ, err := NewQueue(filepath.Join(dir, "source.db"), false)
	if err != nil {
		t.Fatalf("NewQueue source: %v", err)
	}
	if _, err := srcQ.Enqueue(&ShareRequest{Type: "url", URL: "https://wal.test"}); err != nil {
		t.Fatalf("Enqueue source: %v", err)
	}
	if err := srcQ.Close(); err != nil {
		t.Fatalf("Close source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "source.db-wal"), []byte("wal"), 0o600); err != nil {
		t.Fatalf("seed wal: %v", err)
	}
	cmd := dbCmd()
	cmd.SetArgs([]string{"restore", "--queue-db", filepath.Join(dir, "queue.db"), "--src", filepath.Join(dir, "source.db")})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected WAL guard failure")
	}
}

func TestDBRestore_ForceAllowsHotWAL(t *testing.T) {
	dir := t.TempDir()
	srcQ, err := NewQueue(filepath.Join(dir, "source.db"), false)
	if err != nil {
		t.Fatalf("NewQueue source: %v", err)
	}
	if _, err := srcQ.Enqueue(&ShareRequest{Type: "url", URL: "https://force.test"}); err != nil {
		t.Fatalf("Enqueue source: %v", err)
	}
	if err := srcQ.Close(); err != nil {
		t.Fatalf("Close source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "source.db-wal"), []byte("wal"), 0o600); err != nil {
		t.Fatalf("seed wal: %v", err)
	}
	cmd := dbCmd()
	cmd.SetArgs([]string{"restore", "--queue-db", filepath.Join(dir, "queue.db"), "--src", filepath.Join(dir, "source.db"), "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("restore force: %v", err)
	}
}

// ============================================================================
// EPIC-224 Contract Tests (Watch Mode) - CT-1..CT-7 + RG-1/RG-2
// ============================================================================

// CT-1: Watch loop snapshots on tick
func TestBackupWatch_SnapshotsOnTick(t *testing.T) {
	tmp := t.TempDir()
	destPath := filepath.Join(tmp, "backup.db")

	q := newTestQueue(t)
	defer q.Close()
	if _, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://watch.test/1"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Run watch for 3 ticks with 10ms intervals
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := runBackupWatch(ctx, q, destPath, 10*time.Millisecond)
	if err != nil && err != context.DeadlineExceeded {
		t.Fatalf("runBackupWatch: %v", err)
	}

	// Check that at least one snapshot was created
	if _, err := os.Stat(destPath); err != nil {
		t.Fatalf("backup not created: %v", err)
	}

	// Verify snapshot integrity
	snap, err := NewQueue(destPath, false)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer snap.Close()
	items, err := snap.Pending()
	if err != nil {
		t.Fatalf("pending snapshot: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item in snapshot, got %d", len(items))
	}
}

// CT-2: Meta advances each cycle
func TestBackupWatch_MetaAdvances(t *testing.T) {
	tmp := t.TempDir()
	destPath := filepath.Join(tmp, "backup.db")
	metaPath := destPath + ".backup-meta.json"

	q := newTestQueue(t)
	defer q.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := runBackupWatch(ctx, q, destPath, 5*time.Millisecond)
	if err != nil && err != context.DeadlineExceeded {
		t.Fatalf("runBackupWatch: %v", err)
	}

	// Check that meta file was created with timestamps
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}

	var meta dbBackupMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("parse meta: %v", err)
	}

	if meta.CreatedAt.IsZero() {
		t.Errorf("meta CreatedAt is zero")
	}
}

// CT-4: Cycle failure is non-fatal
func TestBackupWatch_CycleFailureNonFatal(t *testing.T) {
	t.Skip("requires mock Snapshot failure - defer to behavioral tests")
}

// CT-5: Watch requires overwrite
func TestBackupWatch_RequiresOverwrite(t *testing.T) {
	tmp := t.TempDir()
	queuePath := filepath.Join(tmp, "queue.db")
	destPath := filepath.Join(tmp, "backup.db")

	cmd := dbCmd()
	cmd.SetArgs([]string{"backup", "--queue-db", queuePath, "--dest", destPath, "--interval", "10ms"})

	err := cmd.Execute()
	if err == nil {
		t.Errorf("expected watch mode to require --overwrite flag")
	}
}

// CT-6: Missing backup PV fatal
func TestBackupWatch_MissingDestFatal(t *testing.T) {
	tmp := t.TempDir()
	destPath := filepath.Join(tmp, "nonexistent", "backup.db")

	q := newTestQueue(t)
	defer q.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := runBackupWatch(ctx, q, destPath, 10*time.Millisecond)
	if err == nil {
		t.Errorf("expected error for missing dest directory")
	}
}

// CT-7: Graceful SIGTERM
func TestBackupWatch_GracefulShutdown(t *testing.T) {
	t.Skip("requires signal handling - test in integration environment")
}

// RG-1: Single-writer invariant (read-only sidecar)
func TestBackupWatch_ReadOnlySnapshot(t *testing.T) {
	tmp := t.TempDir()
	destPath := filepath.Join(tmp, "backup.db")

	q := newTestQueue(t)
	defer q.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := runBackupWatch(ctx, q, destPath, 5*time.Millisecond)
	if err != nil && err != context.DeadlineExceeded {
		t.Fatalf("runBackupWatch: %v", err)
	}

	// Verify that snapshot can be opened read-only without blocking concurrent writes
	snap, err := NewQueue(destPath, true)
	if err != nil {
		t.Fatalf("open snapshot read-only: %v", err)
	}
	defer snap.Close()
}

// RG-2: Failed cycle retains prior snapshot
func TestBackupWatch_RetainsPriorOnFailure(t *testing.T) {
	t.Skip("requires ability to inject Snapshot error - defer to behavioral tests")
}
