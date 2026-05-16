package main

import (
	"testing"
)

func ptr64(v float64) *float64 { return &v }

// CT-1: Extraction confidence gate blocks low-quality extraction
func TestConfidenceThresholdCT1_LowConfidenceBlocked(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
	}
	confidence := ptr64(0.3)
	route := computeActionRouteWithConfig(95, "eng", cfg, confidence)
	if route != "" {
		t.Errorf("CT-1: expected no route (low confidence), got %q", route)
	}
}

// CT-2: Extraction confidence gate passes high-quality extraction
func TestConfidenceThresholdCT2_HighConfidencePasses(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
	}
	confidence := ptr64(0.8)
	route := computeActionRouteWithConfig(85, "eng", cfg, confidence)
	if route == "" {
		t.Errorf("CT-2: expected a route (high confidence, score above threshold), got empty")
	}
}

// CT-3: nil extraction_confidence skips the confidence gate
func TestConfidenceThresholdCT3_NilConfidenceSkipsGate(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
	}
	route := computeActionRouteWithConfig(85, "eng", cfg, nil)
	if route == "" {
		t.Errorf("CT-3: expected route when confidence=nil (gate skipped, score ≥ threshold), got empty")
	}
}

// CT-4: Per-route threshold for destructive route (draft_jira_ticket requires 90)
func TestConfidenceThresholdCT4_DestructiveRouteHighThreshold(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
		RouteThresholds:          map[string]int{"draft_jira_ticket": 90},
	}
	// eng → draft_jira_ticket; score=85 < 90 → blocked
	route := computeActionRouteWithConfig(85, "eng", cfg, nil)
	if route != "" {
		t.Errorf("CT-4: expected no route (score=85 < draft_jira_ticket threshold 90), got %q", route)
	}
}

// CT-5: Per-route threshold for safe route (append_research_digest, lower threshold)
func TestConfidenceThresholdCT5_SafeRoutePassesLowerThreshold(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
		RouteThresholds:          map[string]int{"append_research_digest": 75},
	}
	// research → append_research_digest; score=78 >= 75 → passes
	route := computeActionRouteWithConfig(78, "research", cfg, nil)
	if route == "" {
		t.Errorf("CT-5: expected route (score=78 >= append_research_digest threshold 75), got empty")
	}
}

// CT-6: Default threshold fallback for unknown route
func TestConfidenceThresholdCT6_DefaultThresholdFallback(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
	}
	// score=82 >= default threshold=80 → should return a route
	route := computeActionRouteWithConfig(82, "life", cfg, nil)
	if route == "" {
		t.Errorf("CT-6: expected route (score=82 >= default threshold 80), got empty")
	}
}

// CT-7: ValidateRoutingConfig rejects draft_jira_ticket threshold < 90
func TestConfidenceThresholdCT7_ValidateRejectsBelowFloor(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
		RouteThresholds:          map[string]int{"draft_jira_ticket": 85},
	}
	if err := ValidateRoutingConfig(cfg); err == nil {
		t.Error("CT-7: expected error for draft_jira_ticket=85 (below floor 90), got nil")
	}
}

// CT-8: ValidateRoutingConfig rejects extraction_confidence_gate > 1.0
func TestConfidenceThresholdCT8_ValidateRejectsGateAboveOne(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 1.5,
	}
	if err := ValidateRoutingConfig(cfg); err == nil {
		t.Error("CT-8: expected error for extraction_confidence_gate=1.5 (above 1.0), got nil")
	}
}

// CT-9: Missing routing block uses defaults (DefaultThreshold=80, ExtractionConfidenceGate=0.5)
func TestConfidenceThresholdCT9_MissingBlockUsesDefaults(t *testing.T) {
	cfg := defaultRoutingConfig()
	if cfg.DefaultThreshold != 80 {
		t.Errorf("CT-9: DefaultThreshold=%d, want 80", cfg.DefaultThreshold)
	}
	if cfg.ExtractionConfidenceGate != 0.5 {
		t.Errorf("CT-9: ExtractionConfidenceGate=%f, want 0.5", cfg.ExtractionConfidenceGate)
	}
}
