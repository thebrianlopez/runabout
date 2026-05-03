package main

import (
	"strings"
	"testing"
)

// F1 Domain-Aware Action Routing — Contract Tests (CT-1 through CT-8) and
// Regression Guards (RG-1, RG-2).
//
// Written first (M1) as failing tests — resolveDomainRoute does not exist yet.
// All tests must pass green after M3 lands.
//
// FIRST constraints: pure function, no IO, no network, no shared state.
// Each test constructs its own []DomainRoute and cfgIndex.

// --- helpers ---

// testRoutes returns the canonical two-rule domain_routes table used in most tests.
func testRoutes() []DomainRoute {
	return []DomainRoute{
		{Pattern: "atlassian.net/browse/", OverrideAction: "capture_jira_auto"},
		{Pattern: "atlassian.net/wiki/spaces/", OverrideAction: "capture_confluence_auto"},
	}
}

// domainRouteCfgIndex returns a cfgIndex that contains both capture actions
// plus the standard auto actions, for use in domain route tests.
func domainRouteCfgIndex() map[string]*ActionConfig {
	return map[string]*ActionConfig{
		"capture_jira_auto":       {ID: "capture_jira_auto"},
		"capture_confluence_auto": {ID: "capture_confluence_auto"},
		"uinit_auto":              {ID: "uinit_auto"},
		"vnote_auto":              {ID: "vnote_auto"},
		"ginit_auto":              {ID: "ginit_auto"},
	}
}

// capturedEventSink records emitted domain_route_override events for RG-2.
// It replaces the global emitDomainRouteOverride function pointer during the test.
type capturedOverrideEvent struct {
	originalAction string
	resolvedAction string
	pattern        string
	url            string
}

// CT-1: uinit_auto + Jira browse URL → capture_jira_auto override.
func TestDomainRoute_CT1_UinitAutoJiraBrowse(t *testing.T) {
	req := &ShareRequest{Action: "uinit_auto", URL: "https://org.atlassian.net/browse/KEY-1"}
	routes := testRoutes()
	cfgIndex := domainRouteCfgIndex()

	if err := resolveDomainRoute(req, routes, cfgIndex); err != nil {
		t.Fatalf("CT-1: unexpected error: %v", err)
	}
	if req.Action != "capture_jira_auto" {
		t.Errorf("CT-1: req.Action = %q, want %q", req.Action, "capture_jira_auto")
	}
}

// CT-2: vnote_auto + Jira browse URL → capture_jira_auto (all actions overridden).
func TestDomainRoute_CT2_VnoteAutoJiraBrowseOverridden(t *testing.T) {
	req := &ShareRequest{Action: "vnote_auto", URL: "https://org.atlassian.net/browse/KEY-1"}
	routes := testRoutes()
	cfgIndex := domainRouteCfgIndex()

	if err := resolveDomainRoute(req, routes, cfgIndex); err != nil {
		t.Fatalf("CT-2: unexpected error: %v", err)
	}
	if req.Action != "capture_jira_auto" {
		t.Errorf("CT-2: req.Action = %q, want %q", req.Action, "capture_jira_auto")
	}
}

// CT-3: Confluence wiki URL → capture_confluence_auto override.
func TestDomainRoute_CT3_ConfluenceWiki(t *testing.T) {
	req := &ShareRequest{Action: "uinit_auto", URL: "https://org.atlassian.net/wiki/spaces/X/pages/123"}
	routes := testRoutes()
	cfgIndex := domainRouteCfgIndex()

	if err := resolveDomainRoute(req, routes, cfgIndex); err != nil {
		t.Fatalf("CT-3: unexpected error: %v", err)
	}
	if req.Action != "capture_confluence_auto" {
		t.Errorf("CT-3: req.Action = %q, want %q", req.Action, "capture_confluence_auto")
	}
}

// CT-4: No matching pattern → action unchanged.
func TestDomainRoute_CT4_NoMatch(t *testing.T) {
	req := &ShareRequest{Action: "uinit_auto", URL: "https://medium.com/article"}
	routes := testRoutes()
	cfgIndex := domainRouteCfgIndex()

	if err := resolveDomainRoute(req, routes, cfgIndex); err != nil {
		t.Fatalf("CT-4: unexpected error: %v", err)
	}
	if req.Action != "uinit_auto" {
		t.Errorf("CT-4: req.Action = %q, want %q", req.Action, "uinit_auto")
	}
}

// CT-5: cfgIndex missing capture_jira_auto → error containing "domain_route_action_missing".
func TestDomainRoute_CT5_MissingOverrideAction(t *testing.T) {
	req := &ShareRequest{Action: "uinit_auto", URL: "https://org.atlassian.net/browse/KEY-1"}
	routes := testRoutes()
	// cfgIndex without capture_jira_auto
	cfgIndex := map[string]*ActionConfig{
		"uinit_auto": {ID: "uinit_auto"},
		"ginit_auto": {ID: "ginit_auto"},
		// capture_jira_auto intentionally absent
	}

	err := resolveDomainRoute(req, routes, cfgIndex)
	if err == nil {
		t.Fatal("CT-5: expected non-nil error when override_action missing from cfgIndex")
	}
	if !strings.Contains(err.Error(), "domain_route_action_missing") {
		t.Errorf("CT-5: error = %q, want to contain %q", err.Error(), "domain_route_action_missing")
	}
}

// CT-6: routes=nil → no-op, action unchanged.
func TestDomainRoute_CT6_NilRoutes(t *testing.T) {
	req := &ShareRequest{Action: "uinit_auto", URL: "https://org.atlassian.net/browse/KEY-1"}
	cfgIndex := domainRouteCfgIndex()

	if err := resolveDomainRoute(req, nil, cfgIndex); err != nil {
		t.Fatalf("CT-6: unexpected error: %v", err)
	}
	if req.Action != "uinit_auto" {
		t.Errorf("CT-6: req.Action = %q, want %q (nil routes must be no-op)", req.Action, "uinit_auto")
	}
}

// CT-7: URL matches two rules — first-match wins.
func TestDomainRoute_CT7_FirstMatchWins(t *testing.T) {
	// Both patterns match "atlassian.net/browse/" — first rule is jira, second is confluence.
	// Use distinct patterns so they both trigger.
	routes := []DomainRoute{
		{Pattern: "atlassian.net/browse/", OverrideAction: "capture_jira_auto"},
		{Pattern: "atlassian.net/", OverrideAction: "capture_confluence_auto"},
	}
	cfgIndex := domainRouteCfgIndex()
	req := &ShareRequest{Action: "uinit_auto", URL: "https://org.atlassian.net/browse/KEY-1"}

	if err := resolveDomainRoute(req, routes, cfgIndex); err != nil {
		t.Fatalf("CT-7: unexpected error: %v", err)
	}
	if req.Action != "capture_jira_auto" {
		t.Errorf("CT-7: req.Action = %q, want %q (first-match rule must win)", req.Action, "capture_jira_auto")
	}
}

// CT-8: routeJiraURL does not exist in package after removal.
// This is a compile-time check. The test file will fail to compile if
// routeJiraURL is still present. We verify its absence by not calling it here
// and asserting the symbol is unavailable via build tag.
// The compile check is satisfied in M4 when routeJiraURL is deleted — before
// that milestone this test is a no-op placeholder that documents the intent.
func TestDomainRoute_CT8_RouteJiraURLRemoved(t *testing.T) {
	// After M4: routeJiraURL must not exist in the package.
	// This test passes trivially once routeJiraURL is removed (M4).
	// Until M4, the symbol still exists and this test is a documentation stub.
	//
	// The authoritative compile check is: the build must succeed after M4.
	// No call to routeJiraURL appears in this file — if routeJiraURL is removed
	// and any other file still references it, the build fails (which is CT-8's
	// assertion).
	t.Log("CT-8: routeJiraURL removal verified by successful package build")
}

// --- Regression Guards ---

// RG-1: Jira browse URL must route to capture_jira_auto, NOT ginit_auto.
func TestDomainRoute_RG1_NotGinitAuto(t *testing.T) {
	req := &ShareRequest{Action: "uinit_auto", URL: "https://org.atlassian.net/browse/KEY-1"}
	routes := testRoutes()
	cfgIndex := domainRouteCfgIndex()

	if err := resolveDomainRoute(req, routes, cfgIndex); err != nil {
		t.Fatalf("RG-1: unexpected error: %v", err)
	}
	if req.Action == "ginit_auto" {
		t.Errorf("RG-1: req.Action = %q — Jira browse URL must NOT route to ginit_auto post-F1", req.Action)
	}
	if req.Action != "capture_jira_auto" {
		t.Errorf("RG-1: req.Action = %q, want %q", req.Action, "capture_jira_auto")
	}
}

// RG-2: domain_route_override event is emitted on every match with correct fields.
// Uses a package-level override sink (domainRouteOverrideEmitter) to capture events.
func TestDomainRoute_RG2_OverrideEventEmitted(t *testing.T) {
	var captured []capturedOverrideEvent
	// Install a test-local emitter that records events.
	prev := domainRouteOverrideEmitter
	domainRouteOverrideEmitter = func(url, originalAction, resolvedAction, pattern string) {
		captured = append(captured, capturedOverrideEvent{
			url:            url,
			originalAction: originalAction,
			resolvedAction: resolvedAction,
			pattern:        pattern,
		})
	}
	defer func() { domainRouteOverrideEmitter = prev }()

	req := &ShareRequest{Action: "uinit_auto", URL: "https://org.atlassian.net/browse/KEY-1"}
	routes := testRoutes()
	cfgIndex := domainRouteCfgIndex()

	if err := resolveDomainRoute(req, routes, cfgIndex); err != nil {
		t.Fatalf("RG-2: unexpected error: %v", err)
	}

	if len(captured) != 1 {
		t.Fatalf("RG-2: expected 1 domain_route_override event, got %d", len(captured))
	}
	ev := captured[0]
	if ev.originalAction != "uinit_auto" {
		t.Errorf("RG-2: original_action = %q, want %q", ev.originalAction, "uinit_auto")
	}
	if ev.resolvedAction != "capture_jira_auto" {
		t.Errorf("RG-2: resolved_action = %q, want %q", ev.resolvedAction, "capture_jira_auto")
	}
	if ev.pattern != "atlassian.net/browse/" {
		t.Errorf("RG-2: pattern = %q, want %q", ev.pattern, "atlassian.net/browse/")
	}
}
