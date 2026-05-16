package main

import "fmt"

// RoutingConfig holds configurable action routing thresholds loaded from actions.yaml.
type RoutingConfig struct {
	DefaultThreshold         int            `yaml:"default_threshold"`
	ExtractionConfidenceGate float64        `yaml:"extraction_confidence_gate"`
	RouteThresholds          map[string]int `yaml:"route_thresholds"`
}

// defaultRoutingConfig returns the zero-config defaults per F4 TDD.
func defaultRoutingConfig() RoutingConfig {
	return RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
	}
}

// routeThreshold returns the per-route threshold, falling back to DefaultThreshold.
func (c RoutingConfig) routeThreshold(route string) int {
	if t, ok := c.RouteThresholds[route]; ok {
		return t
	}
	if c.DefaultThreshold <= 0 {
		return 80
	}
	return c.DefaultThreshold
}

// ValidateRoutingConfig checks invariants at config load time.
// draft_jira_ticket threshold must be >= 90 (destructive action floor).
// ExtractionConfidenceGate must be in [0, 1.0].
func ValidateRoutingConfig(cfg RoutingConfig) error {
	if t, ok := cfg.RouteThresholds["draft_jira_ticket"]; ok && t < 90 {
		return fmt.Errorf("draft_jira_ticket threshold must be >= 90, got %d", t)
	}
	if cfg.ExtractionConfidenceGate < 0 || cfg.ExtractionConfidenceGate > 1.0 {
		return fmt.Errorf("extraction_confidence_gate must be in [0, 1.0], got %f", cfg.ExtractionConfidenceGate)
	}
	return nil
}

// computeActionRouteWithConfig determines the action route using the full RoutingConfig.
// extractionConfidence may be nil (URL shares without extraction — gate skipped).
// Gate 1: block if extractionConfidence != nil && < ExtractionConfidenceGate
// Gate 2: block if score < per-route threshold
func computeActionRouteWithConfig(score int, profile string, cfg RoutingConfig, extractionConfidence *float64) string {
	if extractionConfidence != nil && *extractionConfidence < cfg.ExtractionConfidenceGate {
		return ""
	}
	// Profile → route mapping (same as computeActionRoute)
	var route string
	switch profile {
	case "eng", "security", "infra":
		route = "draft_jira_ticket"
	default:
		route = "append_research_digest"
	}
	threshold := cfg.routeThreshold(route)
	if score < threshold {
		return ""
	}
	return route
}
