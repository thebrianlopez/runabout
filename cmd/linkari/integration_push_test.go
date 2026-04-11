//go:build integration

// EPIC-051 M8: end-to-end integration test that spans the CLI → queue →
// push_outbox boundary. This is the missing coverage that would have
// caught the EPIC-050 Push Path Gap before merge.
//
// Run with:
//
//	go test -tags=integration ./cmd/linkari/...
//
// The test invokes the `linkari score` command as a child process via
// `go run` against a throwaway SQLite database and asserts that the
// composed chain (score → archive → digest enqueue) left the expected
// state. This catches the class of silent dual-writer drift where one
// scoring writer skips the push enqueue entirely.
package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPushChainEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "queue.db")

	// Seed the queue with a relayed row so ScoreByURL finds it. This
	// simulates an Android share that landed via /share and is now being
	// triaged by the CLI scorer.
	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	if _, err := q.Enqueue(&ShareRequest{
		Type:    "url",
		URL:     "https://example.com/post-1",
		Profile: "eng",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Promote to relayed so ScoreByURL updates (mimics /share → worker flow).
	pending, _ := q.Pending()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending row, got %d", len(pending))
	}
	if err := q.MarkRelayed(pending[0].ID); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}
	q.Close()

	// Invoke `linkari score` as a child process. `go run` rebuilds the
	// binary on every call — slow (~3s) but it exercises the real cobra
	// wiring, including the command's LINKARI_QUEUE_DB env var resolution
	// and cmd_score.go's archive + EnqueueDigestIfDue path.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/linkari",
		"score",
		"--url", "https://example.com/post-1",
		"--score", "90",
		"--verdict", "Worth reading",
		"--profile", "eng",
		"--slug", "post-1",
		"--tags", "ai,ml",
	)
	cmd.Env = append(cmd.Environ(),
		"LINKARI_QUEUE_DB="+dbPath,
	)
	// Run from repo root so `./cmd/linkari` resolves.
	cmd.Dir = repoRoot(t)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("linkari score failed: %v\noutput:\n%s", err, string(out))
	}
	if !strings.Contains(string(out), `"status": "archived"`) {
		t.Errorf("expected archived status in CLI output:\n%s", string(out))
	}

	// Reopen the queue and assert the composed-chain state.
	q2, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("reopen queue: %v", err)
	}
	defer q2.Close()

	// 1. Queue row should be archived.
	archived, err := q2.ListArchived("eng", 10)
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("expected 1 archived row, got %d", len(archived))
	}
	if archived[0].URL != "https://example.com/post-1" {
		t.Errorf("archived URL mismatch: %q", archived[0].URL)
	}

	// 2. push_outbox should contain exactly one pending digest row.
	// This is the regression assertion — EPIC-050 shipped without this
	// row being written, and no test caught it.
	pushes, err := q2.PendingPushes(10)
	if err != nil {
		t.Fatalf("PendingPushes: %v", err)
	}
	if len(pushes) != 1 {
		t.Fatalf("expected exactly 1 pending push row, got %d: %+v", len(pushes), pushes)
	}
	p := pushes[0]
	if p.Kind != "digest" {
		t.Errorf("kind=%q want digest", p.Kind)
	}
	if p.Slug != "post-1" {
		t.Errorf("slug=%q want post-1", p.Slug)
	}
	if p.Score != 90 {
		t.Errorf("score=%d want 90", p.Score)
	}
}

// EPIC-057 M4: auto-scored ginit rows must NOT produce push_outbox entries.
// This preserves the dual-writer invariant (EPIC-051) — ginit rows bypass
// the scoring pipeline entirely.
func TestAutoScoreDoesNotWritePushOutbox(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	req := &ShareRequest{
		Action:  "ginit_auto",
		Profile: "eng",
		Type:    "text",
		Text:    "PROJ-42",
	}
	_, err = q.EnqueueScored(req, "workspace_bootstrapped")
	if err != nil {
		t.Fatal(err)
	}

	pushes, err := q.PendingPushes(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pushes) != 0 {
		t.Errorf("auto-scored row should not produce push_outbox entries, got %d: %+v", len(pushes), pushes)
	}
}

// repoRoot walks up from the cmd/linkari test cwd until it finds the
// runabout go.mod, returning its directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	for i := 0; i < 5; i++ {
		if strings.HasSuffix(dir, "/runabout") {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("could not locate runabout root from %s", dir)
	return ""
}
