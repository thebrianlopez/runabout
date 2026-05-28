package main

// EPIC-072 M9: Action routing  -  server-side computation of action_route
// from score + profile + verdict. Dispatches to M10 (Jira) and M11 (research digest).

import (
	"context"
	"log/slog"
)

// ActionRouteConfig holds per-profile routing thresholds.
type ActionRouteConfig struct {
	// Score threshold (0-100) above which action routing triggers.
	// Default: 80.
	Threshold int `yaml:"threshold"`
}

// validActionRoutes enumerates accepted action_route values.
var validActionRoutes = map[string]bool{
	"draft_jira_ticket":      true,
	"append_research_digest": true,
}

// computeActionRoute determines the action route based on score, profile, and verdict.
// Returns empty string if no route should be triggered.
func computeActionRoute(score int, profile string, threshold int) string {
	if threshold <= 0 {
		threshold = 80
	}
	if score < threshold {
		return ""
	}

	// Profile-based routing rules.
	switch profile {
	case "eng", "security", "infra":
		return "draft_jira_ticket"
	case "research", "finance", "dining", "life":
		return "append_research_digest"
	default:
		return "append_research_digest"
	}
}

// dispatchActionRoute computes and persists the action route, then dispatches
// to the appropriate handler (M10: Jira, M11: research digest).
// cfg: routing thresholds from config (use defaultRoutingConfig() if absent).
// extractionConfidence: nil for URL shares  -  confidence gate is skipped.
func dispatchActionRoute(ctx context.Context, sc *Scorecard, profile, url string, q *Queue, itemID int64, cfg RoutingConfig, extractionConfidence *float64) {
	route := computeActionRouteWithConfig(sc.Score, profile, cfg, extractionConfidence)
	if route == "" {
		return
	}

	if err := q.SetActionRoute(itemID, route); err != nil {
		slog.WarnContext(ctx, "action_route: set failed", "id", itemID, "route", route, "error", err)
		return
	}

	slog.InfoContext(
		ctx, "action_route dispatched",
		"event_type", "action_route_dispatched",
		"id", itemID,
		"route", route,
		"profile", profile,
		"score", sc.Score,
	)

	switch route {
	case "draft_jira_ticket":
		// M10 handler  -  will be implemented in jira_client.go.
		slog.DebugContext(ctx, "action_route: draft_jira_ticket pending M10", "url", url)
	case "append_research_digest":
		// M11 handler  -  will be implemented separately.
		slog.DebugContext(ctx, "action_route: append_research_digest pending M11", "url", url)
	}
}
