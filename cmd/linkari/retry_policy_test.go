package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// CT-1: Extraction retry on first failure → retry_count=1, retry_after=now+30s
func TestRetryPolicyCT1_ExtractionRetryFirst(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	id, err := q.Enqueue(&ShareRequest{URL: "https://ct1.example.com", Type: "link", Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}

	before := time.Now().Unix()
	if err := retryOrFail(context.Background(), q.db, id, "extraction", fmt.Errorf("ffmpeg exited 1")); err != nil {
		t.Fatal(err)
	}
	after := time.Now().Unix()

	var retryCount int
	var retryAfter int64
	q.db.QueryRow("SELECT retry_count, retry_after FROM queue WHERE id=?", id).Scan(&retryCount, &retryAfter)

	if retryCount != 1 {
		t.Errorf("CT-1: retry_count=%d, want 1", retryCount)
	}
	wantMin := before + 30
	wantMax := after + 30
	if retryAfter < wantMin || retryAfter > wantMax {
		t.Errorf("CT-1: retry_after=%d, want in [%d, %d] (now+30s)", retryAfter, wantMin, wantMax)
	}

	var status string
	q.db.QueryRow("SELECT status FROM queue WHERE id=?", id).Scan(&status)
	if status != "pending" {
		t.Errorf("CT-1: status=%q, want pending (reset for replay loop)", status)
	}
}

// CT-2: Extraction terminal on third failure (retry_count=2 before call) →
// status=failed, error_reason contains extraction_retry_exhausted
func TestRetryPolicyCT2_ExtractionTerminal(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	id, err := q.Enqueue(&ShareRequest{URL: "https://ct2.example.com", Type: "link", Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}
	// Seed retry_count=2 (two prior failures)
	q.db.Exec("UPDATE queue SET retry_count=2 WHERE id=?", id)

	if err := retryOrFail(context.Background(), q.db, id, "extraction", fmt.Errorf("ffmpeg exited 1: corrupt input")); err != nil {
		t.Fatal(err)
	}

	var status, errorReason string
	q.db.QueryRow("SELECT status, error_reason FROM queue WHERE id=?", id).Scan(&status, &errorReason)

	if status != "failed" {
		t.Errorf("CT-2: status=%q, want failed", status)
	}
	if !strings.Contains(errorReason, "extraction_retry_exhausted") {
		t.Errorf("CT-2: error_reason=%q, want to contain extraction_retry_exhausted", errorReason)
	}
}

// CT-3: Scoring retry on first failure → retry_count=1, retry_after=now+60s
func TestRetryPolicyCT3_ScoringRetryFirst(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	id, err := q.Enqueue(&ShareRequest{URL: "https://ct3.example.com", Type: "link", Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}

	before := time.Now().Unix()
	if err := retryOrFail(context.Background(), q.db, id, "scoring", fmt.Errorf("claude CLI exited 1: session expired")); err != nil {
		t.Fatal(err)
	}
	after := time.Now().Unix()

	var retryCount int
	var retryAfter int64
	q.db.QueryRow("SELECT retry_count, retry_after FROM queue WHERE id=?", id).Scan(&retryCount, &retryAfter)

	if retryCount != 1 {
		t.Errorf("CT-3: retry_count=%d, want 1", retryCount)
	}
	wantMin := before + 60
	wantMax := after + 60
	if retryAfter < wantMin || retryAfter > wantMax {
		t.Errorf("CT-3: retry_after=%d, want in [%d, %d] (now+60s)", retryAfter, wantMin, wantMax)
	}

	var status string
	q.db.QueryRow("SELECT status FROM queue WHERE id=?", id).Scan(&status)
	if status != "pending" {
		t.Errorf("CT-3: status=%q, want pending (reset for replay loop)", status)
	}
}

// CT-4: Scoring terminal on second failure (retry_count=1 before call) →
// status=failed, error_reason contains scoring_retry_exhausted
func TestRetryPolicyCT4_ScoringTerminal(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	id, err := q.Enqueue(&ShareRequest{URL: "https://ct4.example.com", Type: "link", Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}
	// Seed retry_count=1 (one prior scoring failure)
	q.db.Exec("UPDATE queue SET retry_count=1 WHERE id=?", id)

	if err := retryOrFail(context.Background(), q.db, id, "scoring", fmt.Errorf("claude CLI exited 1")); err != nil {
		t.Fatal(err)
	}

	var status, errorReason string
	q.db.QueryRow("SELECT status, error_reason FROM queue WHERE id=?", id).Scan(&status, &errorReason)

	if status != "failed" {
		t.Errorf("CT-4: status=%q, want failed", status)
	}
	if !strings.Contains(errorReason, "scoring_retry_exhausted") {
		t.Errorf("CT-4: error_reason=%q, want to contain scoring_retry_exhausted", errorReason)
	}
}

// CT-5: Queue loop skips rows with retry_after > now()
func TestRetryPolicyCT5_DeferredRowSkipped(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	id, err := q.Enqueue(&ShareRequest{URL: "https://ct5.example.com", Type: "link", Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}
	// Set retry_after to far future
	q.db.Exec("UPDATE queue SET retry_after=? WHERE id=?", time.Now().Unix()+3600, id)

	pending, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range pending {
		if item.ID == id {
			t.Error("CT-5: deferred row appeared in Pending() before retry_after elapsed")
		}
	}
}

// CT-6: Terminal failure preserves error cause (truncated to 500 chars) in error_reason
func TestRetryPolicyCT6_ErrorReasonTruncated(t *testing.T) {
	q, _, cleanup := setupTestQueue(t)
	defer cleanup()

	id, err := q.Enqueue(&ShareRequest{URL: "https://ct6.example.com", Type: "link", Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}
	// Seed retry_count=2 → next call is terminal (extraction, attempt 3)
	q.db.Exec("UPDATE queue SET retry_count=2 WHERE id=?", id)

	longCause := strings.Repeat("x", 600) // longer than 500-char limit
	if err := retryOrFail(context.Background(), q.db, id, "extraction", fmt.Errorf("%s", longCause)); err != nil {
		t.Fatal(err)
	}

	var errorReason string
	q.db.QueryRow("SELECT error_reason FROM queue WHERE id=?", id).Scan(&errorReason)

	// TDD: cause truncated to 500 chars; total = error_class_prefix + truncated_cause
	// 600-char input must not appear in full, but the result can exceed 500 due to prefix.
	if len(errorReason) > 550 {
		t.Errorf("CT-6: error_reason len=%d, want ≤550 (500-char cause + class prefix)", len(errorReason))
	}
	if strings.Contains(errorReason, longCause) {
		t.Error("CT-6: error_reason contains full 600-char cause — truncation did not happen")
	}
	// Must still contain the error class
	if !strings.Contains(errorReason, "extraction_retry_exhausted") {
		t.Errorf("CT-6: error_reason=%q missing error class", errorReason)
	}
}
