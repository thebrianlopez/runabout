package main

import (
	"testing"
)

// --- CT-1: Low extraction confidence blocks routing ---
func TestActionRouteCT1_LowConfidenceBlocked(t *testing.T) {
	conf := 0.3
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
	}
	got := computeActionRouteWithConfig(95, "eng", cfg, &conf)
	if got != "" {
		t.Errorf("CT-1: score=95 conf=0.3 gate=0.5 → route=%q, want \"\" (blocked by confidence gate)", got)
	}
}

// --- CT-2: High extraction confidence passes gate ---
func TestActionRouteCT2_HighConfidencePasses(t *testing.T) {
	conf := 0.8
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
	}
	got := computeActionRouteWithConfig(85, "eng", cfg, &conf)
	if got == "" {
		t.Errorf("CT-2: score=85 conf=0.8 gate=0.5 → route empty, want draft_jira_ticket")
	}
}

// --- CT-3: nil extraction confidence skips gate entirely ---
func TestActionRouteCT3_NilConfidenceSkipsGate(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
	}
	got := computeActionRouteWithConfig(85, "eng", cfg, nil)
	if got == "" {
		t.Errorf("CT-3: nil confidence gate=0.5 score=85 → route empty, want draft_jira_ticket")
	}
}

// --- CT-4: Per-route threshold blocks below-threshold score for destructive route ---
func TestActionRouteCT4_DestructiveRouteThreshold(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
		RouteThresholds:          map[string]int{"draft_jira_ticket": 90},
	}
	got := computeActionRouteWithConfig(85, "eng", cfg, nil) // score=85 < threshold=90
	if got != "" {
		t.Errorf("CT-4: score=85 draft_jira_ticket threshold=90 → route=%q, want \"\"", got)
	}
}

// --- CT-5: Per-route threshold allows score above threshold for safe route ---
func TestActionRouteCT5_SafeRouteThreshold(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
		RouteThresholds:          map[string]int{"append_research_digest": 75},
	}
	got := computeActionRouteWithConfig(78, "research", cfg, nil) // score=78 >= threshold=75
	if got != "append_research_digest" {
		t.Errorf("CT-5: score=78 append_research_digest threshold=75 → route=%q, want append_research_digest", got)
	}
}

// --- CT-6: Default threshold fallback when no per-route override ---
func TestActionRouteCT6_DefaultThresholdFallback(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
		// No RouteThresholds  -  all routes use DefaultThreshold
	}
	got := computeActionRouteWithConfig(82, "life", cfg, nil) // score=82 >= default=80
	if got == "" {
		t.Errorf("CT-6: score=82 no override default=80 → route empty, want append_research_digest")
	}
}

// --- CT-7: ValidateRoutingConfig rejects draft_jira_ticket < 90 ---
func TestActionRouteCT7_ValidateRejectsLowJiraThreshold(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
		RouteThresholds:          map[string]int{"draft_jira_ticket": 85},
	}
	if err := ValidateRoutingConfig(cfg); err == nil {
		t.Error("CT-7: ValidateRoutingConfig with draft_jira_ticket=85 should return error, got nil")
	}
}

// --- CT-8: ValidateRoutingConfig rejects confidence gate > 1.0 ---
func TestActionRouteCT8_ValidateRejectsGateOutOfRange(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 1.5,
	}
	if err := ValidateRoutingConfig(cfg); err == nil {
		t.Error("CT-8: ValidateRoutingConfig with gate=1.5 should return error, got nil")
	}
}

// --- CT-9: Zero-value RoutingConfig (missing routing block) uses defaults ---
func TestActionRouteCT9_DefaultsWhenRoutingBlockAbsent(t *testing.T) {
	cfg := defaultRoutingConfig()
	if cfg.DefaultThreshold != 80 {
		t.Errorf("CT-9: DefaultThreshold=%d, want 80", cfg.DefaultThreshold)
	}
	if cfg.ExtractionConfidenceGate != 0.5 {
		t.Errorf("CT-9: ExtractionConfidenceGate=%.2f, want 0.50", cfg.ExtractionConfidenceGate)
	}
	// Default config must pass validation
	if err := ValidateRoutingConfig(cfg); err != nil {
		t.Errorf("CT-9: default config failed ValidateRoutingConfig: %v", err)
	}
}

// --- BT-1: ValidateRoutingConfig accepts valid draft_jira_ticket threshold ---
func TestActionRouteBT1_ValidateAcceptsValidJiraThreshold(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
		RouteThresholds:          map[string]int{"draft_jira_ticket": 90},
	}
	if err := ValidateRoutingConfig(cfg); err != nil {
		t.Errorf("BT-1: draft_jira_ticket=90 (exactly at floor) should be valid, got: %v", err)
	}
}

// --- BT-2: routing_blocked_below_threshold when score just misses threshold ---
func TestActionRouteBT2_ScoreJustBelowThreshold(t *testing.T) {
	cfg := RoutingConfig{
		DefaultThreshold:         80,
		ExtractionConfidenceGate: 0.5,
		RouteThresholds:          map[string]int{"draft_jira_ticket": 90},
	}
	// score=89, threshold=90: just below
	got := computeActionRouteWithConfig(89, "eng", cfg, nil)
	if got != "" {
		t.Errorf("BT-2: score=89 threshold=90 → route=%q, want \"\"", got)
	}
	// score=90, threshold=90: exactly at threshold
	got = computeActionRouteWithConfig(90, "eng", cfg, nil)
	if got != "draft_jira_ticket" {
		t.Errorf("BT-2: score=90 threshold=90 → route=%q, want draft_jira_ticket", got)
	}
}
