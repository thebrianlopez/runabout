package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

const (
	AnalyticsEventShareCreated  = "share_created"
	AnalyticsEventShareScored   = "share_scored"
	AnalyticsEventShareOutcome  = "share_outcome"
	AnalyticsEventShareFeedback = "share_feedback"
)

var ErrAnalyticsSchemaInvalid = errors.New("analytics_schema_invalid")

var supportedAnalyticsEvents = map[string]bool{
	AnalyticsEventShareCreated:  true,
	AnalyticsEventShareScored:   true,
	AnalyticsEventShareOutcome:  true,
	AnalyticsEventShareFeedback: true,
}

type AnalyticsEvent struct {
	EventID            string
	EventType          string
	ShareID            int64
	CreatedAt          time.Time
	Profile            string
	Intent             string
	ContentType        string
	ShareSurface       string
	SourceApp          string
	URLDomain          string
	UserTags           []string
	HasUserRationale   bool
	RationaleWordCount int
	Score              *float64
	Verdict            string
	Outcome            string
	Feedback           string
	Details            map[string]any
}

func (q *Queue) AppendAnalyticsEvent(ctx context.Context, ev AnalyticsEvent) error {
	if q == nil || q.db == nil {
		return fmt.Errorf("analytics_write_failed: queue not configured")
	}
	if strings.TrimSpace(ev.EventID) == "" || !supportedAnalyticsEvents[ev.EventType] {
		return fmt.Errorf("%w: event_id and supported event_type are required", ErrAnalyticsSchemaInvalid)
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}
	tagsJSON, err := json.Marshal(ev.UserTags)
	if err != nil {
		return fmt.Errorf("%w: user_tags_json: %v", ErrAnalyticsSchemaInvalid, err)
	}
	details := ev.Details
	if details == nil {
		details = map[string]any{}
	}
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("%w: details_json: %v", ErrAnalyticsSchemaInvalid, err)
	}
	_, err = q.db.ExecContext(ctx, `INSERT INTO share_analytics_events
		(event_id, event_type, share_id, created_at, profile, intent, content_type, share_surface, source_app, url_domain, user_tags_json, has_user_rationale, rationale_word_count, score, verdict, outcome, feedback, details_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.EventID, ev.EventType, nullInt64(ev.ShareID), ev.CreatedAt.UTC().Format(time.RFC3339Nano), ev.Profile, ev.Intent, ev.ContentType,
		ev.ShareSurface, ev.SourceApp, ev.URLDomain, string(tagsJSON), boolToInt(ev.HasUserRationale), ev.RationaleWordCount,
		ev.Score, ev.Verdict, ev.Outcome, ev.Feedback, string(detailsJSON))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil
		}
		return fmt.Errorf("analytics_write_failed: %w", err)
	}
	return nil
}

func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func recordAnalyticsEvent(ctx context.Context, q *Queue, ev AnalyticsEvent) {
	if q == nil {
		slog.WarnContext(ctx, "analytics event write failed", "event_type", "analytics_write_failed", "analytics_event_type", ev.EventType, "share_id", ev.ShareID, "error", "queue not configured")
		return
	}
	if err := q.AppendAnalyticsEvent(ctx, ev); err != nil {
		slog.WarnContext(ctx, "analytics event write failed", "event_type", "analytics_write_failed", "analytics_event_type", ev.EventType, "share_id", ev.ShareID, "error", err)
		return
	}
	slog.InfoContext(ctx, "analytics event written", "event_type", "analytics_event_written", "analytics_event_type", ev.EventType, "share_id", ev.ShareID)
}

func analyticsDomain(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func rationaleWordCount(s string) int { return len(strings.Fields(strings.TrimSpace(s))) }
