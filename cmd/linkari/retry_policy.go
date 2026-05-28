package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	ExtractionMaxAttempts = 3
	ScoringMaxAttempts    = 2

	extractionErrorClass = "extraction_retry_exhausted"
	scoringErrorClass    = "scoring_retry_exhausted"

	errorReasonMaxLen = 500
)

var (
	extractionBackoff = [2]time.Duration{30 * time.Second, 5 * time.Minute}
	scoringBackoff    = [1]time.Duration{60 * time.Second}
)

// retryOrFail transitions a queue row after a stage failure.
// stage must be "extraction" or "scoring".
// If attempts remain, increments retry_count and sets retry_after per the schedule.
// If exhausted, sets status=failed and error_reason with the error class + truncated cause.
func retryOrFail(ctx context.Context, db *sql.DB, rowID int64, stage string, cause error) error {
	var maxAttempts int
	var schedule []time.Duration
	var errorClass string

	switch stage {
	case "extraction":
		maxAttempts = ExtractionMaxAttempts
		schedule = extractionBackoff[:]
		errorClass = extractionErrorClass
	case "scoring":
		maxAttempts = ScoringMaxAttempts
		schedule = scoringBackoff[:]
		errorClass = scoringErrorClass
	default:
		return fmt.Errorf("retryOrFail: unknown stage %q", stage)
	}

	var currentCount int
	if err := db.QueryRowContext(ctx, "SELECT retry_count FROM queue WHERE id=?", rowID).Scan(&currentCount); err != nil {
		return fmt.Errorf("retryOrFail: read retry_count: %w", err)
	}

	newCount := currentCount + 1

	truncatedCause := cause.Error()
	if len(truncatedCause) > errorReasonMaxLen {
		truncatedCause = truncatedCause[:errorReasonMaxLen]
	}

	if newCount >= maxAttempts {
		reason := fmt.Sprintf("%s: %s", errorClass, truncatedCause)
		if stage == "scoring" {
			_, err := db.ExecContext(
				ctx,
				"UPDATE queue SET status='failed', error_reason=?, retry_count=?, progress='score_failed' WHERE id=?",
				reason, newCount, rowID,
			)
			return err
		}
		_, err := db.ExecContext(
			ctx,
			"UPDATE queue SET status='failed', error_reason=?, retry_count=? WHERE id=?",
			reason, newCount, rowID,
		)
		return err
	}

	// Retriable  -  reset to pending so the replay loop picks it up after the backoff.
	idx := newCount - 1
	if idx >= len(schedule) {
		idx = len(schedule) - 1
	}
	retryAfter := time.Now().Unix() + int64(schedule[idx].Seconds())
	_, err := db.ExecContext(
		ctx,
		"UPDATE queue SET status='pending', retry_count=?, retry_after=? WHERE id=?",
		newCount, retryAfter, rowID,
	)
	return err
}
