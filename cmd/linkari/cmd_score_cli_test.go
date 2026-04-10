package main

// EPIC-053 M3: tests for `linkari score` (prompt-iteration CLI).
//
// These tests stub execHaiku so no real `claude` invocation is made. The
// stubbed Haiku returns a canned markdown response that parseTriageMarkdown
// converts into a TriageResult with a fixed Score/Verdict.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// stubHaiku installs a canned execHaiku stub for the duration of the test.
// lastPrompt captures the system prompt used on the most recent call so tests
// can assert --prompt-file overrides flow through end-to-end.
type haikuCall struct {
	prompt  string
	content string
}

func stubHaiku(t *testing.T, score int, verdict string) *atomic.Pointer[haikuCall] {
	t.Helper()
	orig := execHaiku
	var last atomic.Pointer[haikuCall]
	execHaiku = func(ctx context.Context, systemPrompt, content string) (string, error) {
		last.Store(&haikuCall{prompt: systemPrompt, content: content})
		md := "## Score: " + itoa(score) + "/100\n\n## Verdict\n" + verdict + "\n\nTags: cli, test\n"
		return md, nil
	}
	t.Cleanup(func() { execHaiku = orig })
	return &last
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// writePromptFile writes a minimal non-empty prompt to a temp file and
// returns its path.
func writePromptFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	return p
}

// runScore runs the score subcommand with a stubbed Haiku and content on
// stdin.
func runScore(t *testing.T, dbPath, content string, args ...string) (string, error) {
	t.Helper()
	cmd := scoreCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader(content))
	full := append([]string{"--queue-db", dbPath, "--content-file", "-"}, args...)
	cmd.SetArgs(full)
	err := cmd.Execute()
	return out.String(), err
}

func TestScoreCLI_DryRun_NoWrites(t *testing.T) {
	stubHaiku(t, 95, "great post")
	prompt := writePromptFile(t, "system prompt v1")
	dbPath := filepath.Join(t.TempDir(), "queue.db")

	if _, err := runScore(t, dbPath, "the body",
		"https://example.com/dry",
		"--profile", "eng",
		"--prompt-file", prompt,
		"--dry-run",
	); err != nil {
		t.Fatalf("score: %v", err)
	}

	// --dry-run must not create the queue db at all, or if it does, it
	// must contain zero rows.
	if _, err := os.Stat(dbPath); err == nil {
		q, err := NewQueue(dbPath, false)
		if err != nil {
			t.Fatalf("reopen queue: %v", err)
		}
		defer q.Close()
		items, err := q.ListCursor("", 0, 100)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(items) != 0 {
			t.Errorf("queue rows = %d, want 0 (dry-run)", len(items))
		}
		pushes, err := q.PendingPushes(100)
		if err != nil {
			t.Fatalf("pending pushes: %v", err)
		}
		if len(pushes) != 0 {
			t.Errorf("push_outbox rows = %d, want 0 (dry-run)", len(pushes))
		}
	}
}

func TestScoreCLI_NoPush_QueueOnly(t *testing.T) {
	stubHaiku(t, 95, "great post")
	prompt := writePromptFile(t, "system prompt v1")
	dbPath := filepath.Join(t.TempDir(), "queue.db")

	if _, err := runScore(t, dbPath, "the body",
		"https://example.com/nopush",
		"--profile", "eng",
		"--prompt-file", prompt,
		"--no-push",
	); err != nil {
		t.Fatalf("score: %v", err)
	}

	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("reopen queue: %v", err)
	}
	defer q.Close()

	items, err := q.ListCursor("", 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("queue rows = %d, want 1", len(items))
	}

	pushes, err := q.PendingPushes(100)
	if err != nil {
		t.Fatalf("pending pushes: %v", err)
	}
	if len(pushes) != 0 {
		t.Errorf("push_outbox rows = %d, want 0 (--no-push)", len(pushes))
	}
}

func TestScoreCLI_Default_FullPath(t *testing.T) {
	stubHaiku(t, 95, "great post")
	prompt := writePromptFile(t, "system prompt v1")
	dbPath := filepath.Join(t.TempDir(), "queue.db")

	if _, err := runScore(t, dbPath, "the body",
		"https://example.com/full",
		"--profile", "eng",
		"--prompt-file", prompt,
	); err != nil {
		t.Fatalf("score: %v", err)
	}

	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("reopen queue: %v", err)
	}
	defer q.Close()

	items, err := q.ListCursor("", 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("queue rows = %d, want 1", len(items))
	}

	pushes, err := q.PendingPushes(100)
	if err != nil {
		t.Fatalf("pending pushes: %v", err)
	}
	if len(pushes) != 1 {
		t.Fatalf("push_outbox rows = %d, want 1 (default full path)", len(pushes))
	}
	if pushes[0].Kind != "digest" {
		t.Errorf("push kind = %q, want digest", pushes[0].Kind)
	}
	if pushes[0].Score != 95 {
		t.Errorf("push score = %d, want 95", pushes[0].Score)
	}
}

// TestScoreCLI_DualWriterInvariant asserts that push_outbox writes on the CLI
// score path go through EnqueueDigestIfDue (EPIC-051 M3). We verify this by
// observing that the helper's side effects are present: the row carries the
// profile column written by EnqueueDigestIfDue, and a second invocation inside
// the same throttle window does NOT produce a second row (a proof that the
// throttle guard ran — that guard lives only inside EnqueueDigestIfDue).
func TestScoreCLI_DualWriterInvariant(t *testing.T) {
	stubHaiku(t, 95, "great post")
	prompt := writePromptFile(t, "system prompt v1")
	dbPath := filepath.Join(t.TempDir(), "queue.db")

	if _, err := runScore(t, dbPath, "first",
		"https://example.com/one",
		"--profile", "eng",
		"--prompt-file", prompt,
	); err != nil {
		t.Fatalf("first score: %v", err)
	}

	// Second call, different URL, same profile — must be throttled by
	// EnqueueDigestIfDue. A fourth path that bypassed the helper would
	// insert a second row.
	if _, err := runScore(t, dbPath, "second",
		"https://example.com/two",
		"--profile", "eng",
		"--prompt-file", prompt,
	); err != nil {
		t.Fatalf("second score: %v", err)
	}

	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("reopen queue: %v", err)
	}
	defer q.Close()

	items, err := q.ListCursor("", 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("queue rows = %d, want 2 (both queued)", len(items))
	}

	pushes, err := q.PendingPushes(100)
	if err != nil {
		t.Fatalf("pending pushes: %v", err)
	}
	if len(pushes) != 1 {
		t.Errorf("push_outbox rows = %d, want 1 (second throttled by EnqueueDigestIfDue)", len(pushes))
	}
}

func TestScoreCLI_PromptFile_Override(t *testing.T) {
	last := stubHaiku(t, 95, "great post")
	override := "OVERRIDDEN-PROMPT-TOKEN-XYZ"
	prompt := writePromptFile(t, override)
	dbPath := filepath.Join(t.TempDir(), "queue.db")

	// Snapshot global state that could leak across invocations — here,
	// the archiveThresholdCfg pointer — and assert it does not change.
	archiveThresholdMu.RLock()
	cfgBefore := archiveThresholdCfg
	archiveThresholdMu.RUnlock()

	if _, err := runScore(t, dbPath, "the body",
		"https://example.com/override",
		"--profile", "eng",
		"--prompt-file", prompt,
		"--dry-run",
	); err != nil {
		t.Fatalf("score: %v", err)
	}

	call := last.Load()
	if call == nil {
		t.Fatalf("execHaiku was not called")
	}
	if !strings.Contains(call.prompt, override) {
		t.Errorf("prompt override not propagated to Haiku: got %q, want contains %q", call.prompt, override)
	}

	archiveThresholdMu.RLock()
	cfgAfter := archiveThresholdCfg
	archiveThresholdMu.RUnlock()
	if cfgBefore != cfgAfter {
		t.Errorf("archiveThresholdCfg mutated by --prompt-file run (before=%p after=%p)", cfgBefore, cfgAfter)
	}

	// --prompt-file with an empty file must error clearly.
	empty := writePromptFile(t, "   \n  ")
	_, err := runScore(t, dbPath, "body",
		"https://example.com/empty-prompt",
		"--profile", "eng",
		"--prompt-file", empty,
		"--dry-run",
	)
	if err == nil {
		t.Errorf("expected error for empty --prompt-file, got nil")
	}

	// --prompt-file with a missing path must error clearly.
	_, err = runScore(t, dbPath, "body",
		"https://example.com/missing-prompt",
		"--profile", "eng",
		"--prompt-file", filepath.Join(t.TempDir(), "does-not-exist.md"),
		"--dry-run",
	)
	if err == nil {
		t.Errorf("expected error for missing --prompt-file, got nil")
	}
}

// TestScoreCLI_BelowThreshold_StillPushes asserts that a score below the
// archive threshold (default 80) still produces a push_outbox row.
// EPIC-059: push is decoupled from archive gate.
func TestScoreCLI_BelowThreshold_StillPushes(t *testing.T) {
	stubHaiku(t, 5, "not actionable")
	prompt := writePromptFile(t, "system prompt v1")
	dbPath := filepath.Join(t.TempDir(), "queue.db")

	// Force the builtin config (eng threshold=80) to prevent the test
	// from picking up the user's disk config which may have a lower threshold.
	archiveThresholdMu.Lock()
	origCfg := archiveThresholdCfg
	archiveThresholdCfg = builtinConfig()
	archiveThresholdMu.Unlock()
	t.Cleanup(func() {
		archiveThresholdMu.Lock()
		archiveThresholdCfg = origCfg
		archiveThresholdMu.Unlock()
	})

	if _, err := runScore(t, dbPath, "the body",
		"https://example.com/below-threshold",
		"--profile", "eng",
		"--prompt-file", prompt,
	); err != nil {
		t.Fatalf("score: %v", err)
	}

	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("reopen queue: %v", err)
	}
	defer q.Close()

	// Queue row should exist but NOT be archived (score 5 < threshold 80).
	items, err := q.ListCursor("", 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("queue rows = %d, want 1", len(items))
	}
	if items[0].Status == "archived" {
		t.Errorf("item status = archived, want non-archived (score 5 < threshold 80)")
	}

	// Push row MUST exist despite being below archive threshold (EPIC-059).
	pushes, err := q.PendingPushes(100)
	if err != nil {
		t.Fatalf("pending pushes: %v", err)
	}
	if len(pushes) != 1 {
		t.Fatalf("push_outbox rows = %d, want 1 (EPIC-059: push decoupled from archive)", len(pushes))
	}
	if pushes[0].Score != 5 {
		t.Errorf("push score = %d, want 5", pushes[0].Score)
	}
}
