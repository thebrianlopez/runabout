package main

import (
	"path/filepath"
	"testing"
)

// EPIC-057 M3: Jira ingress invariant tests — regex validation, scoped auth,
// and auto-score enqueue path.
// EPIC-077 M2: routeJiraURL extraction tests — ordering invariant validation.

func TestJiraKeyRegex(t *testing.T) {
	cases := []struct {
		input string
		valid bool
	}{
		{"PROJ-123", true},
		{"AB-1", true},
		{"LONGPROJECT-99999", true},
		{"A1B-42", true},
		{"PROJ_SUB-7", true},
		{"", false},            // empty
		{"proj-123", false},    // lowercase
		{"PROJ123", false},     // no dash
		{"PROJ-", false},       // no digits after dash
		{"PROJ-abc", false},    // non-digits after dash
		{"-123", false},        // leading dash
		{"PROJ-123 ", false},   // trailing space
		{"PROJ-123\n", false},  // newline
		{"$(echo)", false},     // shell injection
	}
	for _, c := range cases {
		got := jiraKeyRegex.MatchString(c.input)
		if got != c.valid {
			t.Errorf("jiraKeyRegex(%q) = %v, want %v", c.input, got, c.valid)
		}
	}
}

func TestScopedAuthMatrix(t *testing.T) {
	srv := &Server{
		token:     "mobile-secret",
		jiraToken: "jira-secret",
	}
	cases := []struct {
		bearer  string
		action  string
		wantOK  bool
		wantKind string
	}{
		{"mobile-secret", "uinit_auto", true, "mobile"},
		{"mobile-secret", "ginit_auto", false, "mobile"},
		{"jira-secret", "uinit_auto", false, "jira"},
		{"jira-secret", "ginit_auto", true, "jira"},
	}
	for _, c := range cases {
		kind, ok := srv.checkScopedAuth(c.bearer, c.action)
		if ok != c.wantOK || kind != c.wantKind {
			t.Errorf("checkScopedAuth(bearer=%q, action=%q) = (%q, %v), want (%q, %v)",
				c.bearer, c.action, kind, ok, c.wantKind, c.wantOK)
		}
	}
}

func TestAutoScoreEnqueuePath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	req := &ShareRequest{
		Action:  "ginit_auto",
		Profile: "eng",
		Type:    "text",
		Text:    "PROJ-42",
	}
	id, err := q.EnqueueScored(req, "workspace_bootstrapped")
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected non-zero queue ID")
	}

	// Verify the row is scored, not pending/relayed.
	items, err := q.List("scored", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 scored item, got %d", len(items))
	}
	if items[0].Verdict != "workspace_bootstrapped" {
		t.Errorf("verdict = %q, want workspace_bootstrapped", items[0].Verdict)
	}
	if items[0].Action != "ginit_auto" {
		t.Errorf("action = %q, want ginit_auto", items[0].Action)
	}

	// Verify it does NOT appear in pending (watchdog won't sweep it).
	pending, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("auto-scored row should not appear in pending, got %d", len(pending))
	}
}

// TestRouteJiraURL validates routeJiraURL behaviour: uinit_auto + Jira URL →
// ginit_auto rewrite; non-Jira URLs and non-uinit_auto actions are unchanged.
// EPIC-077 M2.
func TestRouteJiraURL(t *testing.T) {
	cfgIndex := map[string]*ActionConfig{
		"uinit_auto": {ID: "uinit_auto"},
		"ginit_auto": {ID: "ginit_auto"},
	}

	cases := []struct {
		name       string
		action     string
		url        string
		wantAction string
		wantReroute bool
	}{
		{
			name:        "uinit_auto + Jira URL → ginit_auto",
			action:      "uinit_auto",
			url:         "https://mycompany.atlassian.net/browse/PROJ-123",
			wantAction:  "ginit_auto",
			wantReroute: true,
		},
		{
			name:        "uinit_auto + non-Jira URL → no reroute",
			action:      "uinit_auto",
			url:         "https://github.com/foo/bar",
			wantAction:  "uinit_auto",
			wantReroute: false,
		},
		{
			name:        "ginit_auto already set → no reroute",
			action:      "ginit_auto",
			url:         "https://mycompany.atlassian.net/browse/PROJ-456",
			wantAction:  "ginit_auto",
			wantReroute: false,
		},
		{
			name:        "uinit_auto + Jira URL but no ginit_auto in cfg → no reroute",
			action:      "uinit_auto",
			url:         "https://mycompany.atlassian.net/browse/PROJ-789",
			wantAction:  "uinit_auto",
			wantReroute: false,
		},
		{
			name:        "empty action → no reroute",
			action:      "",
			url:         "https://mycompany.atlassian.net/browse/PROJ-1",
			wantAction:  "",
			wantReroute: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := cfgIndex
			if c.name == "uinit_auto + Jira URL but no ginit_auto in cfg → no reroute" {
				cfg = map[string]*ActionConfig{
					"uinit_auto": {ID: "uinit_auto"},
					// ginit_auto absent
				}
			}
			req := &ShareRequest{Action: c.action, URL: c.url, Profile: ""}
			got := routeJiraURL(req, cfg)
			if got != c.wantReroute {
				t.Errorf("routeJiraURL() rerouted=%v, want %v", got, c.wantReroute)
			}
			if req.Action != c.wantAction {
				t.Errorf("routeJiraURL() req.Action=%q, want %q", req.Action, c.wantAction)
			}
		})
	}
}

// TestRouteJiraURLOrderingInvariant verifies the EPIC-077 M2 ordering invariant:
// routeJiraURL fires before checkScopedAuth. A uinit_auto share with a Jira
// URL and a mobile token must be rerouted to ginit_auto FIRST — then rejected
// by checkScopedAuth (mobile tokens cannot invoke ginit_*). This test confirms
// that a mobile token cannot sneak a Jira URL through as uinit_auto.
func TestRouteJiraURLOrderingInvariant(t *testing.T) {
	cfgIndex := map[string]*ActionConfig{
		"uinit_auto": {ID: "uinit_auto"},
		"ginit_auto": {ID: "ginit_auto"},
	}
	srv := &Server{
		token:     "mobile-secret",
		jiraToken: "jira-secret",
	}

	req := &ShareRequest{
		Action: "uinit_auto",
		URL:    "https://mycompany.atlassian.net/browse/PROJ-123",
	}

	// Step 1: Jira reroute fires first.
	rerouted := routeJiraURL(req, cfgIndex)
	if !rerouted {
		t.Fatal("expected routeJiraURL to reroute uinit_auto → ginit_auto")
	}
	if req.Action != "ginit_auto" {
		t.Fatalf("expected req.Action=ginit_auto after reroute, got %q", req.Action)
	}

	// Step 2: checkScopedAuth sees post-reroute action "ginit_auto".
	// Mobile token must be rejected — ginit_* is Jira-only.
	kind, ok := srv.checkScopedAuth("mobile-secret", req.Action)
	if ok {
		t.Errorf("checkScopedAuth(mobile-secret, ginit_auto) should reject — mobile cannot invoke ginit_*")
	}
	if kind != "mobile" {
		t.Errorf("expected kind=mobile, got %q", kind)
	}

	// Jira token must be accepted for ginit_auto.
	kind, ok = srv.checkScopedAuth("jira-secret", req.Action)
	if !ok {
		t.Errorf("checkScopedAuth(jira-secret, ginit_auto) should accept — Jira token may invoke ginit_*")
	}
	if kind != "jira" {
		t.Errorf("expected kind=jira, got %q", kind)
	}
}

// TestRouteJiraURL_ProfileEmpty confirms that routeJiraURL reroutes correctly
// even when profile="" (the uinit_auto case with no pre-set profile).
// This is the canonical use case: uinit_auto + Jira URL → ginit_auto, profile="".
func TestRouteJiraURL_ProfileEmpty(t *testing.T) {
	cfgIndex := map[string]*ActionConfig{
		"uinit_auto": {ID: "uinit_auto"},
		"ginit_auto": {ID: "ginit_auto"},
	}
	req := &ShareRequest{
		Action:  "uinit_auto",
		URL:     "https://company.atlassian.net/browse/ENG-42",
		Profile: "",
	}
	if !routeJiraURL(req, cfgIndex) {
		t.Fatal("expected reroute")
	}
	if req.Action != "ginit_auto" {
		t.Errorf("expected ginit_auto, got %q", req.Action)
	}
	if req.Profile != "" {
		t.Errorf("routeJiraURL must not modify profile; got %q", req.Profile)
	}
}
