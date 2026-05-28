package main

import (
	"testing"
)

// EPIC-061: share resolution tests updated for 2-action auto-profile model.
// uinit_auto and ginit_auto replace the 14 per-profile actions.

func testCfgIndex() map[string]*ActionConfig {
	return map[string]*ActionConfig{
		"uinit_auto": {
			ID:          "uinit_auto",
			Kind:        KindTemplate,
			ProfileMap:  "auto",
			Target:      "linkari:0",
			ServerScore: true,
		},
		"ginit_auto": {
			ID:              "ginit_auto",
			Kind:            KindTemplate,
			ProfileMap:      "auto",
			Target:          "linkari:0",
			CommandTemplate: "ginit {{.Text}}",
			AutoScore:       true,
		},
		"note_auto": {
			ID:          "note_auto",
			Kind:        KindTemplate,
			ProfileMap:  "auto",
			Target:      "linkari:0",
			ServerScore: true,
		},
	}
}

// Caller-wins: registered actions resolve to themselves.
func TestResolveShareAction_CallerWins(t *testing.T) {
	idx := testCfgIndex()
	cases := []struct {
		action string
	}{
		{"uinit_auto"},
		{"ginit_auto"},
	}
	for _, c := range cases {
		req := &ShareRequest{Action: c.action, Type: "url", URL: "https://example.com"}
		for _, flag := range []bool{false, true} {
			got := resolveShareAction(req, idx, flag)
			if got.ResolvedAction != c.action {
				t.Errorf("action=%s flag=%v: got resolved_action=%q want %q", c.action, flag, got.ResolvedAction, c.action)
			}
		}
	}
}

// Unknown action  -  returned as-is; Route fails fast on lookup miss.
func TestResolveShareAction_UnknownActionPassThrough(t *testing.T) {
	idx := testCfgIndex()
	req := &ShareRequest{Action: "unknown_action", Type: "url", URL: "https://example.com", Profile: "eng"}
	got := resolveShareAction(req, idx, false)
	if got.ResolvedAction != "unknown_action" {
		t.Errorf("got action=%q want unknown_action (pass-through)", got.ResolvedAction)
	}
	if got.Reason != "" {
		t.Errorf("pass-through should leave Reason empty, got %q", got.Reason)
	}
}

// EPIC-061 M2: domain heuristic routing. When heuristicOverrideEnabled is
// true and the action is uinit_auto (ProfileMap="auto"), the resolved
// profile is determined by URL domain heuristics.
func TestResolveShareAction_DomainHeuristic(t *testing.T) {
	idx := testCfgIndex()
	cases := []struct {
		url     string
		profile string
		reason  string // expected Reason field
	}{
		{"https://github.com/golang/go", "eng", ""},
		{"https://stackoverflow.com/questions/123", "eng", ""},
		{"https://arxiv.org/abs/1706.03762", "eng", ""},
		{"https://www.booking.com/hotel/nyc", "travel", ""},
		{"https://www.airbnb.com/rooms/123", "travel", ""},
		{"https://open.spotify.com/track/abc", "music", ""},
		{"https://www.bloomberg.com/markets", "finance", ""},
		{"https://www.nytimes.com/section/food", "dining", ""},
		{"https://www.yelp.com/biz/restaurant", "dining", ""},
		{"https://www.zara.com/us/en/dress-123", "fashion", ""},
		{"https://www.reddit.com/r/golang", "eng", "domain_fallback"},       // fallback to eng
		{"https://unknown-site.example.com/page", "eng", "domain_fallback"}, // fallback to eng
	}
	for _, c := range cases {
		req := &ShareRequest{Action: "uinit_auto", Type: "url", URL: c.url}
		got := resolveShareAction(req, idx, true)
		if got.ResolvedProfile != c.profile {
			t.Errorf("url=%s: got profile=%q want %q", c.url, got.ResolvedProfile, c.profile)
		}
		if got.ResolvedAction != "uinit_auto" {
			t.Errorf("url=%s: action should stay uinit_auto, got %q", c.url, got.ResolvedAction)
		}
		if got.Reason != c.reason {
			t.Errorf("url=%s: got reason=%q want %q", c.url, got.Reason, c.reason)
		}
	}
}

// F1 M4: TestResolveShareAction_JiraAutoRoute migrated from routeJiraURL to
// resolveDomainRoute. The domain_routes table now governs Jira URL routing.
// After F1, Jira browse URLs route to capture_jira_auto (not ginit_auto).
// The ordering invariant is preserved: resolveDomainRoute fires before
// resolveShareAction.
func TestResolveShareAction_JiraAutoRoute(t *testing.T) {
	idx := testCfgIndex()
	// Add capture actions to satisfy resolveDomainRoute cfgIndex check.
	idx["capture_jira_auto"] = &ActionConfig{ID: "capture_jira_auto"}

	routes := []DomainRoute{
		{Pattern: "atlassian.net/browse/", OverrideAction: "capture_jira_auto"},
	}
	jiraURLs := []string{
		"https://myorg.atlassian.net/browse/PROJ-123",
	}
	for _, u := range jiraURLs {
		req := &ShareRequest{Action: "uinit_auto", Type: "url", URL: u}
		// F1: resolveDomainRoute fires before resolveShareAction.
		if err := resolveDomainRoute(req, routes, idx); err != nil {
			t.Fatalf("url=%s: unexpected error: %v", u, err)
		}
		if req.Action != "capture_jira_auto" {
			t.Errorf("url=%s: expected req.Action=capture_jira_auto after resolveDomainRoute, got %q", u, req.Action)
		}
		// After domain route, resolveShareAction sees capture_jira_auto  -  caller-wins.
		got := resolveShareAction(req, idx, true)
		if got.ResolvedAction != "capture_jira_auto" {
			t.Errorf("url=%s: expected resolveShareAction to preserve capture_jira_auto, got %q", u, got.ResolvedAction)
		}
	}
}

// EPIC-061 M2: heuristic override disabled  -  profile stays empty, no rerouting.
func TestResolveShareAction_HeuristicDisabledNoProfile(t *testing.T) {
	idx := testCfgIndex()
	req := &ShareRequest{Action: "uinit_auto", Type: "url", URL: "https://github.com/golang/go"}
	got := resolveShareAction(req, idx, false)
	// With heuristic disabled and ProfileMap=auto, profile stays as-is (empty).
	if got.ResolvedProfile != "" {
		t.Errorf("expected empty profile with heuristic disabled, got %q", got.ResolvedProfile)
	}
	if got.ResolvedAction != "uinit_auto" {
		t.Errorf("expected uinit_auto, got %q", got.ResolvedAction)
	}
}

// Round-trip: auto actions with various URLs resolve cleanly.
func TestShareActionRoundTrip(t *testing.T) {
	idx := testCfgIndex()
	urls := []string{
		"https://github.com/golang/go",
		"https://arxiv.org/abs/1706.03762",
		"https://www.nytimes.com/section/food",
		"https://www.bloomberg.com/markets",
		"https://open.spotify.com/track/abc",
	}
	for _, u := range urls {
		req := &ShareRequest{Action: "uinit_auto", Type: "url", URL: u}
		got := resolveShareAction(req, idx, true)
		if got.ResolvedAction != "uinit_auto" {
			t.Errorf("round-trip violation: url=%s resolved=%q want uinit_auto", u, got.ResolvedAction)
		}
		if got.ResolvedProfile == "" {
			t.Errorf("round-trip: url=%s should have a resolved profile", u)
		}
	}
}

// PA-5 (uinit-action-unresolved POMO): bare "uinit" must normalize to "uinit_auto"
// when intent is present. Guards against Android clients that emit the bare
// intent keyword instead of the fully-qualified action ID.
func TestResolveShareAction_BareUinitNormalizesToAuto(t *testing.T) {
	idx := testCfgIndex()
	cases := []struct {
		action string
		intent string
	}{
		{"uinit", "score"},
		{"uinit", "capture"},
		{"uinit", ""},
	}
	for _, tc := range cases {
		req := &ShareRequest{Action: tc.action, Intent: tc.intent, Type: "url", URL: "https://example.com"}
		got := resolveShareAction(req, idx, false)
		if got.ResolvedAction != "uinit_auto" {
			t.Errorf("action=%q intent=%q: expected ResolvedAction=uinit_auto, got %q",
				tc.action, tc.intent, got.ResolvedAction)
		}
		if got.Reason != "bare_action_normalized" {
			t.Errorf("action=%q intent=%q: expected Reason=bare_action_normalized, got %q",
				tc.action, tc.intent, got.Reason)
		}
	}
}

// RG-3 (POMO_20260526T202824Z_pdf-action-routing-gap): bare "note" sent by
// Android for PDF file shares must normalize to "note_auto" via bare-intent
// normalization. Guards against the silent routing failure observed in
// trace_id 5382e37d where every PDF share returned HTTP 200 with no scoring.
func TestResolveShareAction_NoteNormalizesToNoteAuto(t *testing.T) {
	idx := testCfgIndex()
	req := &ShareRequest{Action: "note", Type: "document", MimeType: "application/pdf"}
	got := resolveShareAction(req, idx, false)
	if got.ResolvedAction != "note_auto" {
		t.Errorf("expected ResolvedAction=note_auto, got %q (PDF shares will fail routing)", got.ResolvedAction)
	}
	if got.Reason != "bare_action_normalized" {
		t.Errorf("expected Reason=bare_action_normalized, got %q", got.Reason)
	}
}
