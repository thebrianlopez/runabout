package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAppendAnalyticsEventContracts(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()
	score := 91.0
	ev := AnalyticsEvent{
		EventID: "test-share-scored-1", EventType: AnalyticsEventShareScored, ShareID: 42,
		CreatedAt: time.Date(2026, 5, 26, 17, 0, 0, 0, time.UTC), Profile: "default", Intent: "score",
		ContentType: "url", URLDomain: "example.com", UserTags: []string{"ai", "paper"}, Score: &score, Verdict: "worth_watching",
		Details: map[string]any{"attempt": 1},
	}
	if err := q.AppendAnalyticsEvent(ctx, ev); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := q.AppendAnalyticsEvent(ctx, ev); err != nil {
		t.Fatalf("duplicate should be idempotent: %v", err)
	}
	var count int
	var tags string
	var gotScore float64
	if err := q.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(user_tags_json),''), COALESCE(MAX(score),0) FROM share_analytics_events WHERE event_id=?`, ev.EventID).Scan(&count, &tags, &gotScore); err != nil {
		t.Fatalf("query analytics: %v", err)
	}
	if count != 1 {
		t.Fatalf("rows=%d, want 1", count)
	}
	if tags != `["ai","paper"]` {
		t.Fatalf("tags=%s", tags)
	}
	if gotScore != score {
		t.Fatalf("score=%v", gotScore)
	}

	if err := q.AppendAnalyticsEvent(ctx, AnalyticsEvent{EventID: "bad", EventType: "nope"}); !errors.Is(err, ErrAnalyticsSchemaInvalid) {
		t.Fatalf("unsupported event err=%v, want ErrAnalyticsSchemaInvalid", err)
	}
}

func TestAnalyticsLifecycleEmissionHelpers(t *testing.T) {
	q := newTestQueue(t)
	req := &ShareRequest{Type: "url", URL: "https://example.com/a", Profile: "default", Intent: "score", SourceApp: "chrome", CaptureMode: "share_sheet", UserRationaleText: "read this later"}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	recordAnalyticsEvent(context.Background(), q, AnalyticsEvent{EventID: "share_created:test", EventType: AnalyticsEventShareCreated, ShareID: id, CreatedAt: time.Now(), Profile: req.Profile, Intent: req.Intent, ContentType: req.Type, ShareSurface: req.CaptureMode, SourceApp: req.SourceApp, URLDomain: analyticsDomain(req.URL), HasUserRationale: true, RationaleWordCount: rationaleWordCount(req.UserRationaleText)})
	score := 88.0
	recordAnalyticsEvent(context.Background(), q, AnalyticsEvent{EventID: "share_scored:test", EventType: AnalyticsEventShareScored, ShareID: id, CreatedAt: time.Now(), Profile: req.Profile, Intent: req.Intent, Score: &score, Verdict: "worth_watching"})
	recordAnalyticsEvent(context.Background(), q, AnalyticsEvent{EventID: "share_outcome:test", EventType: AnalyticsEventShareOutcome, ShareID: id, CreatedAt: time.Now(), Outcome: "watched"})
	recordAnalyticsEvent(context.Background(), q, AnalyticsEvent{EventID: "share_feedback:test", EventType: AnalyticsEventShareFeedback, ShareID: id, CreatedAt: time.Now(), Feedback: "up"})
	for _, typ := range []string{AnalyticsEventShareCreated, AnalyticsEventShareScored, AnalyticsEventShareOutcome, AnalyticsEventShareFeedback} {
		var count int
		if err := q.db.QueryRow(`SELECT COUNT(*) FROM share_analytics_events WHERE share_id=? AND event_type=?`, id, typ).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", typ, err)
		}
		if count != 1 {
			t.Fatalf("%s count=%d, want 1", typ, count)
		}
	}
}
