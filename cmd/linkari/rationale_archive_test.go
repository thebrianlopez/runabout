package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestShareRationaleF4_ArchiveIncludesRationaleMetadata(t *testing.T) {
	srv := newRationaleServer(t)
	req := &ShareRequest{
		Type:                    "url",
		URL:                     "https://rationale.example.com/archive",
		Action:                  "uinit_eng",
		Profile:                 "eng",
		UserRationaleText:       "Only useful if it has concrete implementation details.",
		UserRationaleSource:     "voice_transcript",
		UserRationaleDurationMS: 9000,
		CaptureMode:             "tagged_share_with_voice_rationale",
		SourceApp:               "com.android.chrome",
	}
	id, err := srv.queue.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := srv.queue.Archive(id); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	hreq := httptest.NewRequest(http.MethodGet, "/archive?status=archived&limit=5", nil)
	hreq.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, hreq)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /archive status=%d body=%s", w.Code, w.Body.String())
	}
	var items []QueueItem
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode archive: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d archive items, want 1", len(items))
	}
	got := items[0]
	if got.UserRationaleText != req.UserRationaleText {
		t.Fatalf("UserRationaleText=%q, want %q", got.UserRationaleText, req.UserRationaleText)
	}
	if got.UserRationaleSource != "voice_transcript" || got.UserRationaleDurationMS != 9000 {
		t.Fatalf("rationale source/duration=%q/%d", got.UserRationaleSource, got.UserRationaleDurationMS)
	}
	if got.CaptureMode != req.CaptureMode || got.SourceApp != req.SourceApp {
		t.Fatalf("capture/source_app=%q/%q", got.CaptureMode, got.SourceApp)
	}
}

func TestShareRationaleF4_ArchiveWithoutRationaleOmitsFields(t *testing.T) {
	srv := newRationaleServer(t)
	id, err := srv.queue.Enqueue(&ShareRequest{Type: "url", URL: "https://rationale.example.com/no-note", Profile: "eng"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := srv.queue.Archive(id); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	hreq := httptest.NewRequest(http.MethodGet, "/archive?status=archived&limit=5", nil)
	hreq.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, hreq)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /archive status=%d body=%s", w.Code, w.Body.String())
	}
	var raw []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode archive: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("got %d archive items, want 1", len(raw))
	}
	if _, ok := raw[0]["user_rationale_text"]; ok {
		t.Fatalf("user_rationale_text should be omitted for empty rationale: %#v", raw[0])
	}
}
