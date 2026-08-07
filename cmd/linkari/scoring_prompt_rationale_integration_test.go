package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUserRationalePrompt_CT4_ScoreAsyncInjectsRationale(t *testing.T) {
	isolateEventsDir(t)
	srv := jinaBodyServer(t, http.StatusOK, "This is a generic article about developer tooling.")
	deps := installJinaServer(t, srv)

	prevCC := execContentClassify
	execContentClassify = func(_ context.Context, _, _ string) (string, error) { return "eng", nil }
	t.Cleanup(func() { execContentClassify = prevCC })

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})
	req := &ShareRequest{
		Type:                "url",
		URL:                 "https://example.com/rationale-prompt",
		Profile:             "eng",
		UserRationaleText:   "Useful only if it includes concrete benchmark data.",
		UserRationaleSource: "voice_transcript",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}
	req.QueueRowID = id

	prompt := runScoreAsyncCapturePrompt(t, req, q, deps)
	if !strings.Contains(prompt, "User share-time rationale:") || !strings.Contains(prompt, "concrete benchmark data") {
		t.Fatalf("prompt missing rationale section: %q", prompt)
	}
	if !strings.Contains(prompt, "Do not treat it as evidence") {
		t.Fatalf("prompt missing evidence boundary: %q", prompt)
	}
}
