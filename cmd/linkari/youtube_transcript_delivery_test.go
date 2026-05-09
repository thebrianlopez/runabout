// EPIC-110 M1: contract tests for F3 — YouTube transcript delivery (vnote + FCM push).
// Tests FAIL in M1 (events not yet emitted); they pass after M2 and M3 are implemented.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func newDeliveryEventLogger(t *testing.T) (*EventLogger, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "events.jsonl")
	el, err := NewEventLogger(p)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	t.Cleanup(func() { el.Close() })
	return el, p
}

func readDeliveryLog(t *testing.T, el *EventLogger, path string) string {
	t.Helper()
	el.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	return string(raw)
}

// installYtdlpTranscriptStub overrides execYtdlp with a stub returning fixed values.
func installYtdlpTranscriptStub(t *testing.T, transcript string, meta ytVideoMeta) {
	t.Helper()
	prev := execYtdlp
	execYtdlp = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return transcript, meta, nil
	}
	t.Cleanup(func() { execYtdlp = prev })
}

// installPushStub overrides enqueueTranscriptPushFn; returns &called counter.
func installPushStub(t *testing.T, returnErr error) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	prev := enqueueTranscriptPushFn
	enqueueTranscriptPushFn = func(_ *Queue, _, _, _, _ string) error {
		n.Add(1)
		return returnErr
	}
	t.Cleanup(func() { enqueueTranscriptPushFn = prev })
	return &n
}

// enqueueDeliveryReq enqueues a minimal URL share row and returns the req with QueueRowID set.
func enqueueDeliveryReq(t *testing.T, q *Queue, url string) ShareRequest {
	t.Helper()
	req := ShareRequest{Type: "url", Action: "vnote_auto", URL: url, Profile: "test"}
	rowID, err := q.Enqueue(&req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	req.QueueRowID = rowID
	return req
}

// ─── CT-1: empty transcript is rejected; yt_transcript_empty_guard emitted ───

// TestF3CT_EmptyTranscriptGuard verifies that an empty transcript_text is caught
// before any FCM push attempt. Contract (FDD §5): "F3 reads transcript_text only
// after a *_ok event; never processes a partial/empty transcript."
//
// RG-F3-1: guards against regression where empty transcript reaches FCM push.
func TestF3CT_EmptyTranscriptGuard(t *testing.T) {
	q := newTestQueue(t)
	el, evtPath := newDeliveryEventLogger(t)

	prevDir := transcriptDir
	transcriptDir = filepath.Join(t.TempDir(), "transcripts")
	t.Cleanup(func() { transcriptDir = prevDir })

	// yt-dlp returns exit 0 with no subtitle content.
	installYtdlpTranscriptStub(t, "", ytVideoMeta{Title: "Empty", ID: "empty1", Duration: 60, SubtitleType: "auto"})
	pushCalled := installPushStub(t, nil)

	req := enqueueDeliveryReq(t, q, "https://www.youtube.com/watch?v=empty1")
	transcribeYouTubeAsync(req, q, "yt-dlp", el, "")

	raw := readDeliveryLog(t, el, evtPath)

	if !strings.Contains(raw, `"yt_transcript_empty_guard"`) {
		t.Errorf("CT-1: yt_transcript_empty_guard not in events:\n%s", raw)
	}
	if n := pushCalled.Load(); n != 0 {
		t.Errorf("CT-1: EnqueueTranscriptPush called %d time(s), want 0", n)
	}

	row, err := q.GetByID(req.QueueRowID)
	if err != nil {
		t.Fatalf("CT-1: GetByID: %v", err)
	}
	if row == nil || row.Status != "failed" {
		t.Errorf("CT-1: row.Status = %q, want failed", func() string {
			if row == nil {
				return "<nil>"
			}
			return row.Status
		}())
	}
}

// ─── CT-2: successful delivery emits yt_transcript_delivered ─────────────────

// TestF3CT_DeliveryEmitsEvent verifies that a non-empty transcript and a
// successful EnqueueTranscriptPush emit yt_transcript_delivered on the bus.
//
// RG-F3-2: guards against regression where successful delivery emits no event.
func TestF3CT_DeliveryEmitsEvent(t *testing.T) {
	q := newTestQueue(t)
	el, evtPath := newDeliveryEventLogger(t)

	prevDir := transcriptDir
	transcriptDir = filepath.Join(t.TempDir(), "transcripts")
	t.Cleanup(func() { transcriptDir = prevDir })

	installYtdlpTranscriptStub(t, "This is the transcript.", ytVideoMeta{Title: "Good Video", ID: "good1", Duration: 120, SubtitleType: "auto"})
	pushCalled := installPushStub(t, nil)

	req := enqueueDeliveryReq(t, q, "https://www.youtube.com/watch?v=good1")
	transcribeYouTubeAsync(req, q, "yt-dlp", el, "")

	raw := readDeliveryLog(t, el, evtPath)

	if !strings.Contains(raw, `"yt_transcript_delivered"`) {
		t.Errorf("CT-2: yt_transcript_delivered not in events:\n%s", raw)
	}
	if n := pushCalled.Load(); n != 1 {
		t.Errorf("CT-2: EnqueueTranscriptPush called %d time(s), want 1", n)
	}

	row, err := q.GetByID(req.QueueRowID)
	if err != nil {
		t.Fatalf("CT-2: GetByID: %v", err)
	}
	if row != nil && row.Status == "failed" {
		t.Errorf("CT-2: row.Status = failed; row must not be failed on successful delivery")
	}
}

// ─── CT-3: FCM push failure emits fcm_push_failed; row is not failed ─────────

// TestF3CT_FCMFailureEmitsEvent verifies that an EnqueueTranscriptPush error
// emits fcm_push_failed and does NOT mark the row failed. The vnote is stored;
// push failure is non-fatal per FDD §5 error taxonomy.
//
// RG-F3-3: guards against regression where FCM failure is silently swallowed.
func TestF3CT_FCMFailureEmitsEvent(t *testing.T) {
	q := newTestQueue(t)
	el, evtPath := newDeliveryEventLogger(t)

	prevDir := transcriptDir
	transcriptDir = filepath.Join(t.TempDir(), "transcripts")
	t.Cleanup(func() { transcriptDir = prevDir })

	installYtdlpTranscriptStub(t, "This is the transcript.", ytVideoMeta{Title: "Push Fail", ID: "pfail1", Duration: 90, SubtitleType: "auto"})
	pushCalled := installPushStub(t, fmt.Errorf("sqlite: table push_outbox is locked"))

	req := enqueueDeliveryReq(t, q, "https://www.youtube.com/watch?v=pfail1")
	transcribeYouTubeAsync(req, q, "yt-dlp", el, "")

	raw := readDeliveryLog(t, el, evtPath)

	if !strings.Contains(raw, `"fcm_push_failed"`) {
		t.Errorf("CT-3: fcm_push_failed not in events:\n%s", raw)
	}
	if strings.Contains(raw, `"yt_transcript_delivered"`) {
		t.Errorf("CT-3: yt_transcript_delivered must NOT be emitted when push fails:\n%s", raw)
	}
	if n := pushCalled.Load(); n != 1 {
		t.Errorf("CT-3: EnqueueTranscriptPush called %d time(s), want 1", n)
	}

	// Push failure must not mark the row failed — vnote is stored.
	row, err := q.GetByID(req.QueueRowID)
	if err != nil {
		t.Fatalf("CT-3: GetByID: %v", err)
	}
	if row != nil && row.Status == "failed" {
		t.Errorf("CT-3: row.Status = failed; FCM push failure must not fail the row")
	}
}
