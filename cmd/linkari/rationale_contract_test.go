package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newRationaleServer(t *testing.T) *Server {
	t.Helper()
	q := newTestQueue(t)
	ring := NewRingLog(10)
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	return NewServer("test-token", router, q, ring, false, nil)
}

func TestShareRationaleF1_JSONPersistsValidRationale(t *testing.T) {
	srv := newRationaleServer(t)
	body := `{"type":"url","url":"https://rationale.example.com/json","action":"uinit_eng","user_rationale_text":"Useful only if it has concrete benchmarks.","user_rationale_source":"voice_transcript","user_rationale_duration_ms":12000,"capture_mode":"tagged_share_with_voice_rationale","source_app":"com.android.chrome"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /share status=%d body=%s", w.Code, w.Body.String())
	}
	item := latestByURL(t, srv.queue, "https://rationale.example.com/json")
	if item.UserRationaleText != "Useful only if it has concrete benchmarks." {
		t.Fatalf("UserRationaleText=%q", item.UserRationaleText)
	}
	if item.UserRationaleSource != "voice_transcript" || item.UserRationaleDurationMS != 12000 {
		t.Fatalf("source/duration = %q/%d", item.UserRationaleSource, item.UserRationaleDurationMS)
	}
	if item.CaptureMode != "tagged_share_with_voice_rationale" || item.SourceApp != "com.android.chrome" {
		t.Fatalf("capture/source_app = %q/%q", item.CaptureMode, item.SourceApp)
	}
}

func TestShareRationaleF1_MultipartPersistsValidRationale(t *testing.T) {
	srv := newRationaleServer(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("action", "uinit_eng")
	mw.WriteField("user_rationale_text", "Check if the post includes implementation details.")
	mw.WriteField("user_rationale_source", "typed")
	mw.WriteField("capture_mode", "tagged_share_with_voice_rationale")
	fw, err := mw.CreateFormFile("file", "note.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	_, _ = fw.Write([]byte("multipart body"))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/share", &body)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /share status=%d body=%s", w.Code, w.Body.String())
	}
	item := latestItem(t, srv.queue)
	if item.UserRationaleText != "Check if the post includes implementation details." || item.UserRationaleSource != "typed" {
		t.Fatalf("rationale = %q/%q", item.UserRationaleText, item.UserRationaleSource)
	}
}

func TestShareRationaleF1_InvalidOptionalRationaleDoesNotBlockShare(t *testing.T) {
	srv := newRationaleServer(t)
	body := `{"type":"url","url":"https://rationale.example.com/invalid","action":"uinit_eng","user_rationale_text":"keep me only with a valid source","user_rationale_source":"bad"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /share status=%d body=%s", w.Code, w.Body.String())
	}
	item := latestByURL(t, srv.queue, "https://rationale.example.com/invalid")
	if item.UserRationaleText != "" || item.UserRationaleSource != "" {
		t.Fatalf("invalid rationale should be omitted, got %q/%q", item.UserRationaleText, item.UserRationaleSource)
	}
}

func latestItem(t *testing.T, q *Queue) QueueItem {
	t.Helper()
	items, err := q.query("SELECT " + queueCols + " FROM queue ORDER BY id DESC LIMIT 1")
	if err != nil {
		t.Fatalf("query latest: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	return items[0]
}

func latestByURL(t *testing.T, q *Queue, url string) QueueItem {
	t.Helper()
	items, err := q.query("SELECT "+queueCols+" FROM queue WHERE url=? ORDER BY id DESC LIMIT 1", url)
	if err != nil {
		t.Fatalf("query latest: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items for %s", len(items), url)
	}
	return items[0]
}
