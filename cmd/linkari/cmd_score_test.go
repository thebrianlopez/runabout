package main

import (
	"bytes"
	"path/filepath"
	"testing"
)

// TestScoreCmd_AutoArchiveEnqueuesDigestPush verifies the EPIC-050 push gap
// fix: when `linkari score` auto-archives a row via the CLI path, it must
// also enqueue a digest row in push_outbox so the server's outbox worker
// flushes it via FCM. Previously the CLI bypassed this path entirely.
func TestScoreCmd_AutoArchiveEnqueuesDigestPush(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "queue.db")

	cmd := scoreCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"--queue-db", dbPath,
		"--url", "https://example.com/cli-push",
		"--score", "95",
		"--verdict", "cli verdict",
		"--profile", "eng",
		"--slug", "cli-push-slug",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("score cmd: %v", err)
	}

	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("reopen queue: %v", err)
	}
	defer q.Close()

	// Queue row should be archived.
	item, _, err := q.ScoreByURL("https://example.com/cli-push", 95, "cli verdict", "", "eng", "cli-push-slug")
	if err != nil {
		t.Fatalf("ScoreByURL: %v", err)
	}
	if item.Status != "archived" {
		t.Errorf("status = %q, want archived", item.Status)
	}

	// push_outbox should contain exactly one pending digest row.
	pushes, err := q.PendingPushes(10)
	if err != nil {
		t.Fatalf("PendingPushes: %v", err)
	}
	if len(pushes) != 1 {
		t.Fatalf("got %d push rows, want 1", len(pushes))
	}
	p := pushes[0]
	if p.Kind != "digest" {
		t.Errorf("kind = %q, want digest", p.Kind)
	}
	if p.Score != 95 {
		t.Errorf("score = %d, want 95", p.Score)
	}
	if p.Slug != "cli-push-slug" {
		t.Errorf("slug = %q, want cli-push-slug", p.Slug)
	}
}
