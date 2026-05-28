package main

// EPIC-158 F5 M1: Workflow Registry contract tests.
// CT-1 through CT-9 assert (intent, tag_sig) -> CaptureRenderer lookup behavior.
// Written before implementation (TDD gate).

import (
	"context"
	"testing"
)

// mockWorkflow is a test double for CaptureWorkflow.
type mockWorkflow struct{ name string }

func (m *mockWorkflow) Execute(ctx context.Context, item QueueItem) error { return nil }
func (m *mockWorkflow) Name() string                                      { return m.name }

// newRegistryServer creates a fresh Server with no existing registrations.
func newRegistryServer(t *testing.T) *Server {
	t.Helper()
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	return NewServer("test-token", router, q, NewRingLog(10), false, nil)
}

// CT-1: intent="capture" + tags=["jira"] invokes ginit renderer.
func TestWorkflowRegistry_CT1_CaptureJiraFindsGinit(t *testing.T) {
	s := newRegistryServer(t)
	ginit := &mockWorkflow{"ginit"}
	s.RegisterIntentCapture(IntentTagKey{Intent: "capture", TagSig: "jira"}, ginit)

	r, ok := s.lookupIntentCapture("capture", []string{"jira"})
	if !ok || r == nil {
		t.Fatal("CT-1: expected renderer found, got not-found")
	}
	if r.Name() != "ginit" {
		t.Errorf("CT-1: renderer name = %q, want ginit", r.Name())
	}
}

// CT-2: intent="capture" + tags=["confluence"] invokes Confluence renderer.
func TestWorkflowRegistry_CT2_CaptureConfluenceFindsRenderer(t *testing.T) {
	s := newRegistryServer(t)
	conf := &mockWorkflow{"confluence"}
	s.RegisterIntentCapture(IntentTagKey{Intent: "capture", TagSig: "confluence"}, conf)

	r, ok := s.lookupIntentCapture("capture", []string{"confluence"})
	if !ok || r == nil {
		t.Fatal("CT-2: expected renderer found, got not-found")
	}
	if r.Name() != "confluence" {
		t.Errorf("CT-2: renderer name = %q, want confluence", r.Name())
	}
}

// CT-3: intent="score" returns no renderer.
func TestWorkflowRegistry_CT3_ScoreNeverFindsRenderer(t *testing.T) {
	s := newRegistryServer(t)
	s.RegisterIntentCapture(IntentTagKey{Intent: "capture", TagSig: "jira"}, &mockWorkflow{"ginit"})

	r, ok := s.lookupIntentCapture("score", []string{"domain:eng"})
	if ok || r != nil {
		t.Errorf("CT-3: score intent must never return a renderer; got %v", r)
	}
}

// CT-4: Unrecognized capture tag returns not-found.
func TestWorkflowRegistry_CT4_UnrecognizedTagNotFound(t *testing.T) {
	s := newRegistryServer(t)
	s.RegisterIntentCapture(IntentTagKey{Intent: "capture", TagSig: "jira"}, &mockWorkflow{"ginit"})

	r, ok := s.lookupIntentCapture("capture", []string{"unknown-tool"})
	if ok || r != nil {
		t.Errorf("CT-4: unrecognized tag must return not-found; got renderer %v", r)
	}
}

// CT-5: Duplicate registration panics at startup.
func TestWorkflowRegistry_CT5_DuplicateRegistrationPanics(t *testing.T) {
	s := newRegistryServer(t)
	key := IntentTagKey{Intent: "capture", TagSig: "jira"}
	s.RegisterIntentCapture(key, &mockWorkflow{"first"})

	defer func() {
		if r := recover(); r == nil {
			t.Error("CT-5: expected panic on duplicate registration, got none")
		}
	}()
	s.RegisterIntentCapture(key, &mockWorkflow{"second"})
}

// CT-6: tagSignature filters to capture-relevant tags only.
func TestWorkflowRegistry_CT6_TagSignatureFiltersCaptureTags(t *testing.T) {
	got := tagSignature([]string{"jira", "domain:eng", "read-later"})
	if got != "jira" {
		t.Errorf("CT-6: tagSignature = %q, want jira", got)
	}
}

// CT-7: tagSignature sorts tags lexicographically.
func TestWorkflowRegistry_CT7_TagSignatureSorted(t *testing.T) {
	got := tagSignature([]string{"github", "confluence"})
	if got != "confluence:github" {
		t.Errorf("CT-7: tagSignature = %q, want confluence:github", got)
	}
}

// CT-8: tagSignature is case-sensitive; "Jira" != "jira".
func TestWorkflowRegistry_CT8_TagSignatureCaseSensitive(t *testing.T) {
	got := tagSignature([]string{"Jira"})
	if got != "" {
		t.Errorf("CT-8: tagSignature(Jira) = %q, want empty (case-sensitive)", got)
	}
}

// CT-9: Intent-only fallback fires when tag_sig has no match.
func TestWorkflowRegistry_CT9_IntentOnlyFallback(t *testing.T) {
	s := newRegistryServer(t)
	generic := &mockWorkflow{"generic-capture"}
	s.RegisterIntentCapture(IntentTagKey{Intent: "capture", TagSig: ""}, generic)

	r, ok := s.lookupIntentCapture("capture", []string{"unknown-tag"})
	if !ok || r == nil {
		t.Fatal("CT-9: expected intent-only fallback renderer, got not-found")
	}
	if r.Name() != "generic-capture" {
		t.Errorf("CT-9: renderer name = %q, want generic-capture", r.Name())
	}
}

// RG-1: score intent never triggers a renderer, even with explicit registration.
func TestWorkflowRegistry_RG1_ScoreNeverTriggersRenderer(t *testing.T) {
	s := newRegistryServer(t)
	// Even if someone accidentally registers a score renderer.
	if s.intentCaptureRegistry == nil {
		s.intentCaptureRegistry = make(map[IntentTagKey]CaptureWorkflow)
	}
	s.intentCaptureRegistry[IntentTagKey{Intent: "score", TagSig: ""}] = &mockWorkflow{"bad"}

	r, ok := s.lookupIntentCapture("score", nil)
	if ok || r != nil {
		t.Error("RG-1: score intent must never return a renderer")
	}
}

// RG-2: tagSignature is case-sensitive for capture-relevant tags.
func TestWorkflowRegistry_RG2_TagSigCaseSensitive(t *testing.T) {
	if got := tagSignature([]string{"Jira"}); got != "" {
		t.Errorf("RG-2: tagSignature(Jira) = %q, want empty", got)
	}
	if got := tagSignature([]string{"jira"}); got != "jira" {
		t.Errorf("RG-2: tagSignature(jira) = %q, want jira", got)
	}
}
