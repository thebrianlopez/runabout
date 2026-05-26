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

type AnalyticsTagSummary struct {
	Tag          string  `json:"tag"`
	Count        int     `json:"count"`
	AvgScore     float64 `json:"avg_score"`
	ThumbsUpRate float64 `json:"thumbs_up_rate"`
}

type AnalyticsDomainSummary struct {
	Domain   string  `json:"domain"`
	Shares   int     `json:"shares"`
	AvgScore float64 `json:"avg_score"`
}

type AnalyticsSourceAppSummary struct {
	SourceApp string `json:"source_app"`
	Shares    int    `json:"shares"`
}

type ShareTagAnalyticsReport struct {
	Window                    string                      `json:"window"`
	EventCount                int                         `json:"event_count"`
	InsufficientData          bool                        `json:"insufficient_data"`
	TopTags                   []AnalyticsTagSummary       `json:"top_tags"`
	HighSignalDomains         []AnalyticsDomainSummary    `json:"high_signal_domains"`
	LowSignalDomains          []AnalyticsDomainSummary    `json:"low_signal_domains"`
	SourceApps                []AnalyticsSourceAppSummary `json:"source_apps"`
	RecurringPositiveCriteria []string                    `json:"recurring_positive_criteria"`
	RecurringNoiseCriteria    []string                    `json:"recurring_noise_criteria"`
}

type ShareTagInsightEvidence struct {
	Count        int     `json:"count,omitempty"`
	Shares       int     `json:"shares,omitempty"`
	AvgScore     float64 `json:"avg_score,omitempty"`
	ThumbsUpRate float64 `json:"thumbs_up_rate,omitempty"`
	SourceApp    string  `json:"source_app,omitempty"`
	Domain       string  `json:"domain,omitempty"`
	Tag          string  `json:"tag,omitempty"`
}

type ShareTagInsightObservation struct {
	Summary  string                  `json:"summary"`
	Kind     string                  `json:"kind"`
	Evidence ShareTagInsightEvidence `json:"evidence"`
}

type ShareTagInsightReport struct {
	Window           string                       `json:"window"`
	GeneratedAt      string                       `json:"generated_at"`
	Observations     []ShareTagInsightObservation `json:"observations"`
	Recommendations  []string                     `json:"recommendations"`
	InsufficientData bool                         `json:"insufficient_data"`
}

func (q *Queue) ShareTagAnalytics(ctx context.Context, window string) (ShareTagAnalyticsReport, error) {
	report := ShareTagAnalyticsReport{Window: window, TopTags: []AnalyticsTagSummary{}, HighSignalDomains: []AnalyticsDomainSummary{}, LowSignalDomains: []AnalyticsDomainSummary{}, SourceApps: []AnalyticsSourceAppSummary{}, RecurringPositiveCriteria: []string{}, RecurringNoiseCriteria: []string{}}
	where, args, err := analyticsWindowWhere(window)
	if err != nil {
		return report, err
	}
	if err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM share_analytics_events `+where, args...).Scan(&report.EventCount); err != nil {
		return report, fmt.Errorf("analytics_query_failed: event_count: %w", err)
	}
	report.InsufficientData = report.EventCount < 3

	if rows, err := q.db.QueryContext(ctx, `SELECT json_each.value AS tag, COUNT(*) AS c, COALESCE(AVG(score),0) AS avg_score,
		COALESCE(AVG(CASE WHEN feedback='up' THEN 1.0 WHEN feedback IN ('down','neutral') THEN 0.0 END),0) AS thumbs_up_rate
		FROM share_analytics_events, json_each(COALESCE(NULLIF(user_tags_json,''),'[]')) `+where+`
		GROUP BY tag ORDER BY c DESC, tag ASC LIMIT 10`, args...); err != nil {
		return report, fmt.Errorf("analytics_query_failed: top_tags: %w", err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var item AnalyticsTagSummary
			if err := rows.Scan(&item.Tag, &item.Count, &item.AvgScore, &item.ThumbsUpRate); err != nil {
				return report, err
			}
			report.TopTags = append(report.TopTags, item)
		}
	}

	report.HighSignalDomains, err = q.analyticsDomains(ctx, where, args, "DESC")
	if err != nil {
		return report, err
	}
	report.LowSignalDomains, err = q.analyticsDomains(ctx, where, args, "ASC")
	if err != nil {
		return report, err
	}

	if rows, err := q.db.QueryContext(ctx, `SELECT COALESCE(NULLIF(source_app,''),'unknown') AS source_app, COUNT(DISTINCT share_id) AS shares FROM share_analytics_events `+where+` GROUP BY source_app ORDER BY shares DESC, source_app ASC LIMIT 10`, args...); err != nil {
		return report, fmt.Errorf("analytics_query_failed: source_apps: %w", err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var item AnalyticsSourceAppSummary
			if err := rows.Scan(&item.SourceApp, &item.Shares); err != nil {
				return report, err
			}
			report.SourceApps = append(report.SourceApps, item)
		}
	}
	for _, tag := range report.TopTags {
		if tag.AvgScore >= 70 || tag.ThumbsUpRate >= 0.6 {
			report.RecurringPositiveCriteria = append(report.RecurringPositiveCriteria, tag.Tag)
		}
		if tag.AvgScore > 0 && tag.AvgScore <= 40 {
			report.RecurringNoiseCriteria = append(report.RecurringNoiseCriteria, tag.Tag)
		}
	}
	return report, nil
}

func (q *Queue) ShareTagInsightReport(ctx context.Context, window string) (ShareTagInsightReport, error) {
	agg, err := q.ShareTagAnalytics(ctx, window)
	if err != nil {
		return ShareTagInsightReport{Window: window, Observations: []ShareTagInsightObservation{}, Recommendations: []string{}}, err
	}
	report := ShareTagInsightReport{
		Window:           agg.Window,
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
		Observations:     []ShareTagInsightObservation{},
		Recommendations:  []string{},
		InsufficientData: agg.InsufficientData,
	}
	if agg.InsufficientData {
		return report, nil
	}
	for _, tag := range agg.TopTags {
		if len(report.Observations) >= 3 {
			break
		}
		kind := "tag_pattern"
		summary := fmt.Sprintf("%s appears in %d shared items with average score %.1f", tag.Tag, tag.Count, tag.AvgScore)
		if tag.AvgScore >= 70 || tag.ThumbsUpRate >= 0.6 {
			kind = "high_signal_tag"
			summary = fmt.Sprintf("%s is a high-signal tag across %d shares", tag.Tag, tag.Count)
			report.Recommendations = append(report.Recommendations, fmt.Sprintf("Prioritize review of new items tagged %s when time is limited.", tag.Tag))
		} else if tag.AvgScore > 0 && tag.AvgScore <= 40 {
			kind = "low_signal_tag"
			summary = fmt.Sprintf("%s is trending low-signal with average score %.1f", tag.Tag, tag.AvgScore)
			report.Recommendations = append(report.Recommendations, fmt.Sprintf("Consider tightening capture criteria for %s-tagged items.", tag.Tag))
		}
		report.Observations = append(report.Observations, ShareTagInsightObservation{Summary: summary, Kind: kind, Evidence: ShareTagInsightEvidence{Tag: tag.Tag, Count: tag.Count, AvgScore: tag.AvgScore, ThumbsUpRate: tag.ThumbsUpRate}})
	}
	for _, domain := range agg.HighSignalDomains {
		if len(report.Observations) >= 3 {
			break
		}
		report.Observations = append(report.Observations, ShareTagInsightObservation{Summary: fmt.Sprintf("%s is a high-signal domain with average score %.1f", domain.Domain, domain.AvgScore), Kind: "high_signal_domain", Evidence: ShareTagInsightEvidence{Domain: domain.Domain, Shares: domain.Shares, AvgScore: domain.AvgScore}})
		report.Recommendations = append(report.Recommendations, fmt.Sprintf("Keep %s in the active capture path while its score remains high.", domain.Domain))
	}
	for _, domain := range agg.LowSignalDomains {
		if len(report.Observations) >= 3 {
			break
		}
		report.Observations = append(report.Observations, ShareTagInsightObservation{Summary: fmt.Sprintf("%s is a low-signal domain with average score %.1f", domain.Domain, domain.AvgScore), Kind: "low_signal_domain", Evidence: ShareTagInsightEvidence{Domain: domain.Domain, Shares: domain.Shares, AvgScore: domain.AvgScore}})
		report.Recommendations = append(report.Recommendations, fmt.Sprintf("Review whether %s should be filtered or routed differently.", domain.Domain))
	}
	for _, source := range agg.SourceApps {
		if len(report.Observations) >= 3 {
			break
		}
		report.Observations = append(report.Observations, ShareTagInsightObservation{Summary: fmt.Sprintf("%s is the most active share surface with %d shares", source.SourceApp, source.Shares), Kind: "source_app_volume", Evidence: ShareTagInsightEvidence{SourceApp: source.SourceApp, Shares: source.Shares}})
	}
	return report, nil
}

func analyticsWindowWhere(window string) (string, []any, error) {
	switch window {
	case "7d":
		return "WHERE created_at >= ?", []any{time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339Nano)}, nil
	case "30d":
		return "WHERE created_at >= ?", []any{time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano)}, nil
	case "all", "":
		return "", nil, nil
	default:
		return "", nil, fmt.Errorf("%w: unsupported analytics window", ErrAnalyticsSchemaInvalid)
	}
}

func (q *Queue) analyticsDomains(ctx context.Context, where string, args []any, direction string) ([]AnalyticsDomainSummary, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT url_domain, COUNT(DISTINCT share_id) AS shares, COALESCE(AVG(score),0) AS avg_score FROM share_analytics_events `+where+` GROUP BY url_domain HAVING url_domain IS NOT NULL AND url_domain != '' ORDER BY avg_score `+direction+`, shares DESC, url_domain ASC LIMIT 10`, args...)
	if err != nil {
		return nil, fmt.Errorf("analytics_query_failed: domains: %w", err)
	}
	defer rows.Close()
	out := []AnalyticsDomainSummary{}
	for rows.Next() {
		var item AnalyticsDomainSummary
		if err := rows.Scan(&item.Domain, &item.Shares, &item.AvgScore); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}
