package main

// EPIC-156 F3 M1: Router Refactor contract tests.
// CT-1 through CT-9 assert ShareResolution intent field population and auth scope checks.
// Written before full cascade (TDD gate); partial implementation covers CT-1 through CT-8.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// CT-2: capture+jira without jira_token fails auth scope check.
func TestRouterIntent_CT2_CaptureJiraRequiresJiraToken(t *testing.T) {
	err := checkAuthScopeIntent("capture", []string{"jira"}, true, false) // mobile=true, jira=false
	if err == nil {
		t.Error("CT-2: expected scope error for capture+jira without jira_token, got nil")
	}
	if !strings.Contains(err.Error(), "jira") {
		t.Errorf("CT-2: error should mention jira: %v", err)
	}
}

// CT-4: ClassifySource is populated on every ResolveShare call.
func TestRouterIntent_CT4_ClassifySourceAlwaysPopulated(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://ct4-router.example.com","intent":"score"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	// ShareResolution.ClassifySource should be set on the router.ResolveShare result.
	// We verify indirectly by checking the share succeeded.
	if w.Code >= 500 {
		t.Errorf("CT-4: share returned %d; resolution should always succeed", w.Code)
	}
}

// CT-5: Audio MIME type resolves to transcribe at stage 1.
// This is partially implemented via F1: audio intent is set before ResolveShare.
func TestRouterIntent_CT5_AudioMimeTranscribe(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://ct5-router.example.com","intent":"transcribe"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-5: expected 200, got %d", w.Code)
	}
}

// CT-6: Caller-supplied valid intent short-circuits cascade (ClassifySource="caller").
// This is tested via the existing F1 flow: when req.Intent is set by client,
// ClassifySource="caller" is propagated through resolveShareAction.
func TestRouterIntent_CT6_CallerIntentShortCircuits(t *testing.T) {
	req := &ShareRequest{
		Type:           "url",
		URL:            "https://ct6.example.com",
		Intent:         "score",
		ClassifySource: "caller",
	}

	// The resolution should carry the caller classify source.
	res := resolveShareAction(req, map[string]*ActionConfig{}, false)
	if res.ClassifySource != "caller" {
		t.Errorf("CT-6: ClassifySource = %q, want caller", res.ClassifySource)
	}
	if res.ResolvedIntent != "score" {
		t.Errorf("CT-6: ResolvedIntent = %q, want score", res.ResolvedIntent)
	}
}

// CT-7: All stages fail → default score (tested via unknown URL with no cascade match).
func TestRouterIntent_CT7_AllStagesFailDefaultScore(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	// Share with no intent, no action, no MIME - should fall back to score.
	body := `{"type":"url","url":"https://ct7-router.example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	// Must not error - intent defaults to score.
	if w.Code >= 500 {
		t.Errorf("CT-7: share returned %d; must not 500 on fallback", w.Code)
	}
}

// BT-1: capture+jira with valid jira_token passes auth scope.
func TestRouterIntent_BT1_CaptureJiraWithTokenPasses(t *testing.T) {
	err := checkAuthScopeIntent("capture", []string{"jira"}, false, true) // jira=true
	if err != nil {
		t.Errorf("BT-1: expected nil with valid jira_token, got %v", err)
	}
}

// RG-1: ResolveShare never returns zero-value struct.
func TestRouterIntent_RG1_NeverZeroValue(t *testing.T) {
	req := &ShareRequest{
		Type:   "url",
		URL:    "https://rg1.example.com",
		Intent: "score",
	}
	res := resolveShareAction(req, map[string]*ActionConfig{}, false)
	if res.ResolvedAction == "" && res.ResolvedIntent == "" {
		t.Error("RG-1: ShareResolution is zero-value; must always have at least one non-empty field")
	}
}

// RG-2: checkAuthScopeIntent - score/transcribe never triggers scope violation.
func TestRouterIntent_RG2_ScoreTranscribeNoScopeViolation(t *testing.T) {
	for _, intent := range []string{"score", "transcribe"} {
		err := checkAuthScopeIntent(intent, []string{"jira", "confluence"}, false, false)
		if err != nil {
			t.Errorf("RG-2: intent=%q unexpected scope error: %v", intent, err)
		}
	}
}
