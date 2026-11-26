package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CT-1: Name returns "pi_remote".
func TestPiRemoteScoringBackend_Name(t *testing.T) {
	b := PiRemoteScoringBackend{BaseURL: "http://example.invalid"}
	if got := b.Name(); got != "pi_remote" {
		t.Fatalf("Name: got %q, want %q", got, "pi_remote")
	}
}

// CT-2: Complete posts the expected JSON body to /v1/complete and returns
// the trimmed stdout field.
func TestPiRemoteScoringBackend_Complete_PostsAndParses(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"stdout": "  hello world  \n", "error": ""})
	}))
	defer srv.Close()

	b := PiRemoteScoringBackend{BaseURL: srv.URL, Client: srv.Client()}
	got, err := b.Complete(context.Background(), "sys-prompt", "user-content")
	if err != nil {
		t.Fatalf("CT-2: unexpected error: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("CT-2: got %q, want %q", got, "hello world")
	}
	if gotPath != "/v1/complete" {
		t.Fatalf("CT-2: path: got %q, want %q", gotPath, "/v1/complete")
	}
	if gotBody["system_prompt"] != "sys-prompt" || gotBody["content"] != "user-content" {
		t.Fatalf("CT-2: request body: got %+v", gotBody)
	}
}

// CT-3: CompleteJSON posts to /v1/complete-json with schema included and
// returns the raw (trimmed) stdout bytes.
func TestPiRemoteScoringBackend_CompleteJSON_PassesSchemaAndReturnsBytes(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"stdout": `{"score":1}`, "error": ""})
	}))
	defer srv.Close()

	b := PiRemoteScoringBackend{BaseURL: srv.URL, Client: srv.Client()}
	got, err := b.CompleteJSON(context.Background(), "sys", "content", `{"type":"object"}`)
	if err != nil {
		t.Fatalf("CT-3: unexpected error: %v", err)
	}
	if string(got) != `{"score":1}` {
		t.Fatalf("CT-3: got %q, want %q", string(got), `{"score":1}`)
	}
	if gotPath != "/v1/complete-json" {
		t.Fatalf("CT-3: path: got %q, want %q", gotPath, "/v1/complete-json")
	}
	if gotBody["schema"] != `{"type":"object"}` {
		t.Fatalf("CT-3: schema not passed through: got %+v", gotBody)
	}
}

// CT-4: CompleteVision base64-encodes the image file and includes it in the
// request to /v1/complete-vision.
func TestPiRemoteScoringBackend_CompleteVision_EncodesImage(t *testing.T) {
	imgContent := []byte("fake-image-bytes")
	imgPath := filepath.Join(t.TempDir(), "test.png")
	if err := os.WriteFile(imgPath, imgContent, 0o644); err != nil {
		t.Fatal(err)
	}

	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"stdout": `{"score":2}`, "error": ""})
	}))
	defer srv.Close()

	b := PiRemoteScoringBackend{BaseURL: srv.URL, Client: srv.Client()}
	got, err := b.CompleteVision(context.Background(), "sys", "meta", imgPath, `{"type":"object"}`)
	if err != nil {
		t.Fatalf("CT-4: unexpected error: %v", err)
	}
	if string(got) != `{"score":2}` {
		t.Fatalf("CT-4: got %q, want %q", string(got), `{"score":2}`)
	}
	if gotPath != "/v1/complete-vision" {
		t.Fatalf("CT-4: path: got %q, want %q", gotPath, "/v1/complete-vision")
	}
	wantB64 := base64.StdEncoding.EncodeToString(imgContent)
	if gotBody["image_base64"] != wantB64 {
		t.Fatalf("CT-4: image_base64: got %q, want %q", gotBody["image_base64"], wantB64)
	}
	if gotBody["content"] != "meta" {
		t.Fatalf("CT-4: content: got %q, want %q", gotBody["content"], "meta")
	}
	if gotBody["schema"] != `{"type":"object"}` {
		t.Fatalf("CT-4: schema not passed through: got %+v", gotBody)
	}
}

// CT-5: CompleteVision returns an error without contacting the server when
// the image file cannot be read.
func TestPiRemoteScoringBackend_CompleteVision_UnreadableImage(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	b := PiRemoteScoringBackend{BaseURL: srv.URL, Client: srv.Client()}
	_, err := b.CompleteVision(context.Background(), "sys", "meta", filepath.Join(t.TempDir(), "missing.png"), "{}")
	if err == nil {
		t.Fatal("CT-5: expected error for missing image file")
	}
	if called {
		t.Fatal("CT-5: server should not have been contacted")
	}
}

// CT-6: non-200 status produces a wrapped error containing the server's
// error detail.
func TestPiRemoteScoringBackend_NonOKStatus_WrapsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"stdout": "", "error": "pi-service crashed: boom-detail"})
	}))
	defer srv.Close()

	b := PiRemoteScoringBackend{BaseURL: srv.URL, Client: srv.Client()}
	_, err := b.Complete(context.Background(), "sys", "content")
	if err == nil {
		t.Fatal("CT-6: expected error, got nil")
	}
	if !strings.HasPrefix(err.Error(), "pi_remote:") {
		t.Fatalf("CT-6: error should be wrapped with pi_remote prefix, got: %v", err)
	}
	if !strings.Contains(err.Error(), "boom-detail") {
		t.Fatalf("CT-6: error should contain server detail %q, got: %v", "boom-detail", err)
	}
}

// CT-7: a 200 response with a non-empty error field also produces a wrapped
// error containing the server's error detail.
func TestPiRemoteScoringBackend_BodyError_WrapsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"stdout": "", "error": "model refused: policy-violation-detail"})
	}))
	defer srv.Close()

	b := PiRemoteScoringBackend{BaseURL: srv.URL, Client: srv.Client()}
	_, err := b.CompleteJSON(context.Background(), "sys", "content", "{}")
	if err == nil {
		t.Fatal("CT-7: expected error, got nil")
	}
	if !strings.HasPrefix(err.Error(), "pi_remote:") {
		t.Fatalf("CT-7: error should be wrapped with pi_remote prefix, got: %v", err)
	}
	if !strings.Contains(err.Error(), "policy-violation-detail") {
		t.Fatalf("CT-7: error should contain server detail %q, got: %v", "policy-violation-detail", err)
	}
}

// CT-8: empty stdout on an otherwise-successful response is itself an error.
func TestPiRemoteScoringBackend_EmptyOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"stdout": "", "error": ""})
	}))
	defer srv.Close()

	b := PiRemoteScoringBackend{BaseURL: srv.URL, Client: srv.Client()}
	_, err := b.Complete(context.Background(), "sys", "content")
	if err == nil {
		t.Fatal("CT-8: expected error on empty output, got nil")
	}
	if !strings.Contains(err.Error(), "pi_remote returned empty output") {
		t.Fatalf("CT-8: got: %v", err)
	}
}

// CT-9: nil Client field defaults to a client with a timeout rather than
// panicking or hanging forever.
func TestPiRemoteScoringBackend_NilClientUsesDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"stdout": "ok", "error": ""})
	}))
	defer srv.Close()

	b := PiRemoteScoringBackend{BaseURL: srv.URL}
	got, err := b.Complete(context.Background(), "sys", "content")
	if err != nil {
		t.Fatalf("CT-9: unexpected error: %v", err)
	}
	if got != "ok" {
		t.Fatalf("CT-9: got %q, want %q", got, "ok")
	}
}
