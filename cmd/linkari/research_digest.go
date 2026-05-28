package main

// EPIC-072 M11: Research digest append mechanism.
// Appends scored URLs and verdicts to a research digest markdown file
// using atomic file append.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// appendToResearchDigest appends a scored item to the research digest file.
// Uses atomic append (O_APPEND) to prevent corruption from concurrent writes.
func appendToResearchDigest(path, url, verdict string, score int, profile string) error {
	if path == "" {
		return fmt.Errorf("research_digest_path not configured")
	}

	now := time.Now().UTC().Format("2006-01-02 15:04")
	entry := fmt.Sprintf("\n## %s (score: %d, profile: %s)\n\n- URL: %s\n- %s\n- Added: %s\n",
		truncateString(verdict, 100), score, profile, url, verdict, now)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open digest: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("append digest: %w", err)
	}
	return nil
}

// dispatchResearchDigest is the M11 handler wired into dispatchActionRoute.
func dispatchResearchDigest(ctx context.Context, sc *Scorecard, profile, url string, q *Queue, itemID int64, digestPath string) {
	if digestPath == "" {
		slog.DebugContext(ctx, "action_route: research_digest_path not configured, skipping")
		return
	}

	if err := appendToResearchDigest(digestPath, url, sc.Verdict, sc.Score, profile); err != nil {
		slog.WarnContext(
			ctx, "action_route: research digest append failed",
			"id", itemID,
			"error", err,
		)
		return
	}

	slog.InfoContext(
		ctx, "action_route: research digest appended",
		"event_type", "research_digest_appended",
		"id", itemID,
		"path", digestPath,
	)
}
