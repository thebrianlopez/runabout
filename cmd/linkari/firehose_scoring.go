package main

import "log/slog"

// firehoseScoreContext carries the dependencies needed to call scoreAsync
// from handleFirehosePost. Separating these from the queue allows M3 to
// wire scoring without touching processFirehoseMessage's WebSocket logic.
type firehoseScoreContext struct {
	Queue      *Queue
	Eval       Evaluator
	Events     *EventLogger
	BskyClient *BlueskyClient
	ScoreSem   chan struct{} // capacity=3 concurrency limiter
}

// resolveFirehoseProfile maps a firehose subscription profile to a valid
// scoring profile. "default" and any unknown profile fall back to "eng"
// since all current firehose keywords are engineering-related.
func resolveFirehoseProfile(profile string) string {
	switch profile {
	case "eng":
		return "eng"
	case "default":
		slog.Warn(
			"firehose profile fallback",
			"event_type", "firehose_profile_fallback",
			"original_profile", profile,
			"resolved_profile", "eng",
		)
		return "eng"
	default:
		slog.Warn(
			"firehose profile fallback",
			"event_type", "firehose_profile_fallback",
			"original_profile", profile,
			"resolved_profile", "eng",
		)
		return "eng"
	}
}

// firehoseActionForProfile derives a registered action ID from a profile name.
// Known profiles map to uinit_{profile}. Unknown or empty profiles fall back
// to uinit_eng to avoid empty action fields that break replay (F6).
func firehoseActionForProfile(profile string) string {
	switch profile {
	case "eng":
		return "uinit_eng"
	default:
		if profile == "" {
			slog.Warn(
				"firehose action derivation failed",
				"event_type", "firehose_action_derivation_failed",
				"error_class", "firehose_action_derivation_failed",
				"profile", profile,
			)
		}
		return "uinit_eng"
	}
}

// migrateFirehoseProfiles updates all firehose_subscriptions rows with
// profile='default' to 'eng'. Idempotent  -  safe to run on startup.
func migrateFirehoseProfiles(q *Queue) {
	_, err := q.db.Exec("UPDATE firehose_subscriptions SET profile = 'eng' WHERE profile = 'default'")
	if err != nil {
		slog.Warn("firehose profile migration failed", "error", err)
	}
}
