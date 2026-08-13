package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDBRestore_RenameFailureLeavesLiveDBUntouched(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "queue.db")
	targetQ, err := NewQueue(targetPath, false)
	if err != nil {
		t.Fatalf("NewQueue target: %v", err)
	}
	if _, err := targetQ.Enqueue(&ShareRequest{Type: "url", URL: "https://live.test/old"}); err != nil {
		t.Fatalf("enqueue old: %v", err)
	}
	if err := targetQ.Close(); err != nil {
		t.Fatalf("close target: %v", err)
	}

	srcQ := newTestQueue(t)
	if _, err := srcQ.Enqueue(&ShareRequest{Type: "url", URL: "https://live.test/new"}); err != nil {
		t.Fatalf("enqueue new: %v", err)
	}
	backupPath := filepath.Join(dir, "backup.db")
	if err := srcQ.Snapshot(backupPath); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	cmd := dbCmdWith(func(oldpath, newpath string) error { return os.ErrPermission })
	cmd.SetArgs([]string{"restore", "--queue-db", targetPath, "--src", backupPath})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected rename failure")
	}

	reopen, err := NewQueue(targetPath, false)
	if err != nil {
		t.Fatalf("reopen target: %v", err)
	}
	defer reopen.Close()
	items, err := reopen.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(items) != 1 || items[0].URL != "https://live.test/old" {
		t.Fatalf("live db changed on failed restore: %+v", items)
	}
}
