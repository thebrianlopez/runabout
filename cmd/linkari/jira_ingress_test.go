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
		{"", false},           // empty
		{"proj-123", false},   // lowercase
		{"PROJ123", false},    // no dash
		{"PROJ-", false},      // no digits after dash
		{"PROJ-abc", false},   // non-digits after dash
		{"-123", false},       // leading dash
		{"PROJ-123 ", false},  // trailing space
		{"PROJ-123\n", false}, // newline
		{"$(echo)", false},    // shell injection
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
		bearer   string
		action   string
		wantOK   bool
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

// TestRouteJiraURL and TestRouteJiraURLOrderingInvariant were removed in F1 (M4).
// routeJiraURL was deleted; its behaviour is superseded by resolveDomainRoute.
// Equivalent coverage lives in domain_route_test.go: CT-1, CT-2, RG-1, RG-2.

// --- EPIC-091 M1: Atlassian URL routing contract tests (CT-1, CT-2) ----
//
// CT-1 and CT-2 verify that atlassian.net URLs produce a positive profile match.
// CT-3 through CT-6 and RG-1 previously tested routeJiraURL; those are removed
// in F1 M4. Equivalent coverage lives in domain_route_test.go (CT-1/CT-2/RG-1/RG-2).

// BT-1: Router.ResolveShare with atlassian.net URL sets Reason="" (positive domain match, not fallback).
func TestAtlassianURLRouting_BT1_ResolveShareNoFallback(t *testing.T) {
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	req := &ShareRequest{
		Action: "uinit_auto",
		Type:   "url",
		URL:    "https://org.atlassian.net/browse/SR-2972",
	}
	res := router.ResolveShare(req, true)
	if res.Reason != "" {
		t.Errorf("BT-1: ResolveShare() Reason=%q, want \"\" (atlassian.net must not fall back to domain_fallback)", res.Reason)
	}
	if res.ResolvedProfile != "eng" {
		t.Errorf("BT-1: ResolveShare() ResolvedProfile=%q, want \"eng\"", res.ResolvedProfile)
	}
}

// CT-1: atlassian.net Jira browse URL → positive profile match ("eng", true).
func TestAtlassianURLRouting_CT1_JiraBrowsePositiveMatch(t *testing.T) {
	profile, matched := classifyURLProfile("https://org.atlassian.net/browse/KEY-1")
	if profile != "eng" {
		t.Errorf("CT-1: classifyURLProfile() profile=%q, want %q", profile, "eng")
	}
	if !matched {
		t.Errorf("CT-1: classifyURLProfile() matched=%v, want true (positive domain match, not fallback)", matched)
	}
}

// CT-2: atlassian.net Confluence wiki URL → positive profile match ("eng", true).
func TestAtlassianURLRouting_CT2_ConfluenceWikiPositiveMatch(t *testing.T) {
	profile, matched := classifyURLProfile("https://org.atlassian.net/wiki/spaces/X/pages/123")
	if profile != "eng" {
		t.Errorf("CT-2: classifyURLProfile() profile=%q, want %q", profile, "eng")
	}
	if !matched {
		t.Errorf("CT-2: classifyURLProfile() matched=%v, want true (positive domain match, not fallback)", matched)
	}
}
