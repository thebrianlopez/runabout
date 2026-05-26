package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestShareTagAnalyticsAggregationContracts(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seed := func(id string, daysAgo int, shareID int64, tag string, domain string, source string, score float64, feedback string) {
		t.Helper()
		if err := q.AppendAnalyticsEvent(ctx, AnalyticsEvent{EventID: id, EventType: AnalyticsEventShareScored, ShareID: shareID, CreatedAt: now.AddDate(0, 0, -daysAgo), UserTags: []string{tag}, URLDomain: domain, SourceApp: source, Score: &score, Feedback: feedback}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("recent-ai", 1, 1, "ai", "github.com", "chrome", 92, "up")
	seed("recent-ai-2", 2, 2, "ai", "github.com", "chrome", 88, "up")
	seed("recent-marketing", 3, 3, "marketing", "vendor.example", "firefox", 22, "down")
	seed("old-ai", 20, 4, "ai", "old.example", "chrome", 75, "up")

	report, err := q.ShareTagAnalytics(ctx, "7d")
	if err != nil {
		t.Fatalf("7d report: %v", err)
	}
	if report.EventCount != 3 || report.InsufficientData {
		t.Fatalf("7d event_count=%d insufficient=%v, want 3 false", report.EventCount, report.InsufficientData)
	}
	if len(report.TopTags) < 2 || report.TopTags[0].Tag != "ai" || report.TopTags[0].Count != 2 {
		t.Fatalf("top_tags=%+v, want ai count 2 first", report.TopTags)
	}
	if len(report.HighSignalDomains) == 0 || report.HighSignalDomains[0].Domain != "github.com" {
		t.Fatalf("high_signal_domains=%+v, want github.com first", report.HighSignalDomains)
	}
	if len(report.LowSignalDomains) == 0 || report.LowSignalDomains[0].Domain != "vendor.example" {
		t.Fatalf("low_signal_domains=%+v, want vendor.example first", report.LowSignalDomains)
	}

	report30, err := q.ShareTagAnalytics(ctx, "30d")
	if err != nil {
		t.Fatalf("30d report: %v", err)
	}
	if report30.EventCount != 4 {
		t.Fatalf("30d event_count=%d, want 4", report30.EventCount)
	}
	reportAll, err := q.ShareTagAnalytics(ctx, "all")
	if err != nil {
		t.Fatalf("all report: %v", err)
	}
	if reportAll.EventCount != 4 {
		t.Fatalf("all event_count=%d, want 4", reportAll.EventCount)
	}
	if _, err := q.ShareTagAnalytics(ctx, "90d"); !errors.Is(err, ErrAnalyticsSchemaInvalid) {
		t.Fatalf("unsupported window err=%v, want ErrAnalyticsSchemaInvalid", err)
	}
}

func TestHandleShareTagAnalyticsContracts(t *testing.T) {
	q := newTestQueue(t)
	score := 91.0
	if err := q.AppendAnalyticsEvent(context.Background(), AnalyticsEvent{EventID: "api-1", EventType: AnalyticsEventShareScored, ShareID: 1, CreatedAt: time.Now().UTC(), UserTags: []string{"benchmarks"}, URLDomain: "github.com", SourceApp: "chrome", Score: &score}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := &Server{queue: q}
	w := httptest.NewRecorder()
	srv.handleShareTagAnalytics(w, httptest.NewRequest(http.MethodGet, "/analytics/share-tags?window=7d", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var report ShareTagAnalyticsReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.Window != "7d" || report.EventCount != 1 || !report.InsufficientData {
		t.Fatalf("report=%+v, want 7d count 1 insufficient", report)
	}
	if strings.Contains(w.Body.String(), "read this later") {
		t.Fatalf("aggregate response leaked raw rationale text: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	srv.handleShareTagAnalytics(w, httptest.NewRequest(http.MethodGet, "/analytics/share-tags?window=90d", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad window status=%d, want 400", w.Code)
	}
}

func TestShareTagInsightReportContracts(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seed := func(id string, shareID int64, tag string, domain string, source string, score float64, feedback string) {
		t.Helper()
		if err := q.AppendAnalyticsEvent(ctx, AnalyticsEvent{EventID: id, EventType: AnalyticsEventShareScored, ShareID: shareID, CreatedAt: now, UserTags: []string{tag}, URLDomain: domain, SourceApp: source, Score: &score, Feedback: feedback, Details: map[string]any{"raw_rationale_text": "read this later"}}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("report-ai-1", 1, "benchmarks", "github.com", "chrome", 91, "up")
	seed("report-ai-2", 2, "benchmarks", "github.com", "chrome", 86, "up")
	seed("report-noise", 3, "politics", "vendor.example", "android", 18, "down")

	report, err := q.ShareTagInsightReport(ctx, "7d")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if report.Window != "7d" || report.InsufficientData {
		t.Fatalf("report=%+v, want 7d sufficient", report)
	}
	if len(report.Observations) < 3 {
		t.Fatalf("observations=%+v, want at least three", report.Observations)
	}
	if len(report.Recommendations) == 0 {
		t.Fatalf("recommendations empty; want facts separated from recommendations")
	}
	if report.Observations[0].Evidence.Count == 0 && report.Observations[0].Evidence.Shares == 0 {
		t.Fatalf("observation missing numeric evidence: %+v", report.Observations[0])
	}
	body, _ := json.Marshal(report)
	if strings.Contains(string(body), "read this later") || strings.Contains(string(body), "raw_rationale_text") {
		t.Fatalf("report leaked raw rationale text/details: %s", string(body))
	}

	w := httptest.NewRecorder()
	(&Server{queue: q}).handleShareTagAnalyticsReport(w, httptest.NewRequest(http.MethodGet, "/analytics/share-tags/report?window=7d", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var apiReport ShareTagInsightReport
	if err := json.Unmarshal(w.Body.Bytes(), &apiReport); err != nil {
		t.Fatalf("decode api report: %v", err)
	}
	if len(apiReport.Observations) < 3 {
		t.Fatalf("api observations=%+v, want at least three", apiReport.Observations)
	}

	w = httptest.NewRecorder()
	(&Server{queue: q}).handleShareTagAnalyticsReport(w, httptest.NewRequest(http.MethodGet, "/analytics/share-tags/report?window=90d", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad window status=%d, want 400", w.Code)
	}
}

func TestShareTagInsightReportInsufficientData(t *testing.T) {
	q := newTestQueue(t)
	score := 70.0
	if err := q.AppendAnalyticsEvent(context.Background(), AnalyticsEvent{EventID: "report-one", EventType: AnalyticsEventShareScored, ShareID: 1, CreatedAt: time.Now().UTC(), UserTags: []string{"ai"}, URLDomain: "example.com", Score: &score}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	report, err := q.ShareTagInsightReport(context.Background(), "7d")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !report.InsufficientData || len(report.Observations) != 0 || len(report.Recommendations) != 0 {
		t.Fatalf("report=%+v, want friendly insufficient-data empty state", report)
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
