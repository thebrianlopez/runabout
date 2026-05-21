package main

// EPIC-158 F5: Workflow Registry - (intent, tag_sig) -> CaptureWorkflow.
// Replaces action-ID-keyed capture renderer registration with intent+tag-sig keyed lookup.
// The existing captureRenderers map (action-ID keyed) is kept during the soak window (F8).

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// captureRelevantTags is the canonical set of tags that affect capture routing.
// Only these tags participate in tag_sig computation.
var captureRelevantTags = map[string]bool{
	"jira":       true,
	"confluence": true,
	"github":     true,
}

// IntentTagKey is the registry lookup key for (intent, tag_sig).
type IntentTagKey struct {
	Intent string // "score" | "capture" | "transcribe"
	TagSig string // sorted capture-relevant tags joined with ":"
}

// tagSignature derives the registry lookup key from a tag slice.
// Only capture-relevant tags (jira, confluence, github) are included.
// Tags are filtered (exact match, case-sensitive), sorted lexicographically, joined with ":".
func tagSignature(tags []string) string {
	var relevant []string
	for _, tag := range tags {
		if captureRelevantTags[tag] {
			relevant = append(relevant, tag)
		}
	}
	if len(relevant) == 0 {
		return ""
	}
	sort.Strings(relevant)
	return strings.Join(relevant, ":")
}

// intentRegistryMu is a type-check sentinel only; the actual mutex is captureRenderersMu on Server.
var _ sync.RWMutex

// RegisterIntentCapture registers a renderer for a (intent, tag_sig) key.
// Panics at startup if the key is already registered (duplicate prevention per TDD §4).
// Call at server startup only - not goroutine-safe during registration phase.
func (s *Server) RegisterIntentCapture(key IntentTagKey, renderer CaptureWorkflow) {
	s.captureRenderersMu.Lock()
	defer s.captureRenderersMu.Unlock()
	if s.intentCaptureRegistry == nil {
		s.intentCaptureRegistry = make(map[IntentTagKey]CaptureWorkflow)
	}
	if _, exists := s.intentCaptureRegistry[key]; exists {
		panic(fmt.Sprintf("duplicate IntentCapture registration: intent=%q tag_sig=%q", key.Intent, key.TagSig))
	}
	s.intentCaptureRegistry[key] = renderer
	slog.Debug("intent_capture_registered",
		"intent", key.Intent,
		"tag_sig", key.TagSig,
		"renderer", renderer.Name(),
	)
}

// lookupIntentCapture finds the best renderer for the given intent and tags.
// Match order:
//  1. exact (intent, tag_sig) match
//  2. intent-only fallback (TagSig="")
//
// Returns (nil, false) when no renderer found - not an error.
// intent="score" and intent="transcribe" always return (nil, false) even if
// a renderer is accidentally registered for those intents (RG-1).
func (s *Server) lookupIntentCapture(intent string, tags []string) (CaptureWorkflow, bool) { //nolint:unused
	// Only "capture" intent can trigger renderers (RG-1).
	if intent != "capture" {
		return nil, false
	}

	s.captureRenderersMu.RLock()
	registry := s.intentCaptureRegistry
	s.captureRenderersMu.RUnlock()

	if registry == nil {
		return nil, false
	}

	sig := tagSignature(tags)

	// 1. Exact match.
	if r, ok := registry[IntentTagKey{Intent: intent, TagSig: sig}]; ok {
		slog.Info("capture_renderer_invoked",
			"renderer_name", r.Name(),
			"intent", intent,
			"tag_sig", sig,
		)
		return r, true
	}

	// 2. Intent-only fallback.
	if r, ok := registry[IntentTagKey{Intent: intent, TagSig: ""}]; ok {
		slog.Info("capture_renderer_invoked",
			"renderer_name", r.Name(),
			"intent", intent,
			"tag_sig", "",
			"fallback", true,
		)
		return r, true
	}

	slog.Warn("capture_renderer_not_found",
		"error_class", "capture_renderer_not_found",
		"intent", intent,
		"tag_sig", sig,
		"user_tags", tags,
	)
	return nil, false
}

// CaptureWorkflow is the interface for intent+tag-keyed capture actions.
// Execute runs the workflow (ginit, confluence-capture, etc.).
// Separate from CaptureRenderer (content rendering) to avoid naming collision.
type CaptureWorkflow interface {
	Execute(ctx context.Context, item QueueItem) error
	Name() string
}
