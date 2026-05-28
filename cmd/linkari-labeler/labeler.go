package main

import (
	"log/slog"
)

// verdictLabel maps a Linkari score to a Bluesky label string.
func verdictLabel(score int) string {
	switch {
	case score >= 70:
		return "linkari-score-high"
	case score >= 40:
		return "linkari-score-medium"
	default:
		return "linkari-score-low"
	}
}

// RuleContext is the interface for the automod rule context (simplified for MVP).
type RuleContext interface {
	AtURI() string
	AddRecordLabel(label string)
}

// mockRuleCtx is used in tests.
type mockRuleCtx struct {
	atURI    string
	addLabel func(string)
}

func (m *mockRuleCtx) AtURI() string           { return m.atURI }
func (m *mockRuleCtx) AddRecordLabel(l string) { m.addLabel(l) }

// PostRuleFunc is a function that processes a post AT URI and optionally adds labels.
type PostRuleFunc func(ctx RuleContext, atURI string)

// makeLinkariScoreRule returns a PostRuleFunc that labels posts based on Linkari score.
func makeLinkariScoreRule(db *ReadOnlyDB) PostRuleFunc {
	return func(ctx RuleContext, atURI string) {
		score := db.LookupScoreForURI(atURI)
		if score < 0 {
			slog.Debug(
				"labeler: skipped unscored",
				"event_type", "labeler_label_skipped_unscored",
				"at_uri", atURI,
			)
			return
		}
		label := verdictLabel(score)
		ctx.AddRecordLabel(label)
		slog.Info(
			"labeler: label emitted",
			"event_type", "labeler_label_emitted",
			"at_uri", atURI,
			"label", label,
			"score", score,
		)
	}
}
