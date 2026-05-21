package main

// EPIC-127 F5: Scoring prompt tag injection.
//
// formatUserTags converts user-applied tags to a prompt metadata section.
// parseUserTags parses the queue row user_tags JSON string (used when
// rebuilding a ShareRequest from a QueueItem outside the normal score path).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// formatUserTags formats user-applied tags as a scoring prompt metadata section.
//
// Returns "" when tags is nil or empty  -  no prompt change (backward compatible).
// Caps at 10 tags; extras are silently dropped.
//
// For non-empty input returns:
//
//	"User-Applied Tags: tag1, tag2\n" +
//	"Note: These tags were explicitly applied by the user at share time. " +
//	"They represent deliberate intent and should be weighted accordingly.\n"
func formatUserTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	capped := tags
	if len(capped) > 10 {
		capped = capped[:10]
	}
	return fmt.Sprintf(
		"User-Applied Tags: %s\n"+
			"Note: These tags were explicitly applied by the user at share time. "+
			"They represent deliberate intent and should be weighted accordingly.\n",
		strings.Join(capped, ", "),
	)
}

// parseUserTags parses the JSON array string stored in the queue row user_tags
// column. Returns nil for empty input. Logs a tag_injection_failed warning and
// returns nil for malformed JSON  -  scoring proceeds without tag context.
func parseUserTags(ctx context.Context, raw string) []string {
	if raw == "" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		slog.WarnContext(ctx, "score: tag_injection_failed  -  malformed user_tags JSON",
			"event_type", "tag_injection_failed",
			"error", err,
		)
		return nil
	}
	return tags
}
