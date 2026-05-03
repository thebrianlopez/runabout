package main

import (
	"fmt"
	"log/slog"
	"strings"
)

// domainRouteOverrideEmitter is the function called when resolveDomainRoute
// successfully overrides req.Action. It is a package-level variable so tests
// can inject a mock sink (RG-2). Production code uses emitDomainRouteOverride.
var domainRouteOverrideEmitter = emitDomainRouteOverride

// emitDomainRouteOverride writes a domain_route_override event to the telemetry
// bus. Called on every successful domain route match so operators can observe
// silent action overrides in the SSE/JSONL stream (TDD §3 — always log override).
func emitDomainRouteOverride(url, originalAction, resolvedAction, pattern string) {
	emitPushEvent("domain_route_override", map[string]interface{}{
		"url":             url,
		"original_action": originalAction,
		"resolved_action": resolvedAction,
		"pattern":         pattern,
	})
}

// resolveDomainRoute iterates domain_routes (first-match wins) and overrides
// req.Action when the request URL contains a rule's Pattern substring.
//
// Contract (TDD F1 §3):
//   - Match found, override_action in cfgIndex → req.Action = override_action,
//     domain_route_override event emitted, return nil.
//   - Match found, override_action NOT in cfgIndex → domain_route_action_missing
//     event emitted, return non-nil error.
//   - No match → req.Action unchanged, return nil.
//   - routes nil/empty → no-op, return nil.
//
// Pure function except for the event emit side effect. No IO, no network.
func resolveDomainRoute(req *ShareRequest, routes []DomainRoute, cfgIndex map[string]*ActionConfig) error {
	for _, rule := range routes {
		if !strings.Contains(req.URL, rule.Pattern) {
			continue
		}
		// Pattern matched.
		if _, ok := cfgIndex[rule.OverrideAction]; !ok {
			emitPushEvent("domain_route_action_missing", map[string]interface{}{
				"url":             req.URL,
				"pattern":         rule.Pattern,
				"override_action": rule.OverrideAction,
			})
			return fmt.Errorf("domain_route_action_missing: action %q not in cfgIndex (pattern %q)", rule.OverrideAction, rule.Pattern)
		}
		originalAction := req.Action
		req.Action = rule.OverrideAction
		domainRouteOverrideEmitter(req.URL, originalAction, req.Action, rule.Pattern)
		return nil
	}
	return nil
}

// validateDomainRoutes checks that every override_action in domain_routes is
// present in cfgIndex. Returns an error (rather than calling log.Fatal directly)
// so callers can choose to fatal-log with structured context.
func validateDomainRoutes(routes []DomainRoute, cfgIndex map[string]*ActionConfig) error {
	for _, rule := range routes {
		if _, ok := cfgIndex[rule.OverrideAction]; !ok {
			return fmt.Errorf("domain_routes misconfigured: override_action %q not found (pattern %q)", rule.OverrideAction, rule.Pattern)
		}
	}
	return nil
}

// ResolveDomainRoute is the thread-safe Router entry point for resolveDomainRoute.
// Call this AFTER resolveShareAction and BEFORE checkScopedAuth (F1 wire order).
func (r *Router) ResolveDomainRoute(req *ShareRequest, routes []DomainRoute) error {
	r.mu.RLock()
	cfgIndex := r.cfgIndex
	r.mu.RUnlock()
	return resolveDomainRoute(req, routes, cfgIndex)
}

// ValidateDomainRoutes is the thread-safe startup validation entry point.
// Fatal-logs if any override_action is not registered in the router's cfgIndex.
func (r *Router) ValidateDomainRoutes(routes []DomainRoute) {
	r.mu.RLock()
	cfgIndex := r.cfgIndex
	r.mu.RUnlock()
	if err := validateDomainRoutes(routes, cfgIndex); err != nil {
		slog.Error("startup validation failed", "error", err)
		panic(err.Error()) // replaced by log.Fatal in main.go call site
	}
}
