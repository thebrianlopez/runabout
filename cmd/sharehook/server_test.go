package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	srv := NewServer("test-token", nil)
	mux := srv.Mux()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp ShareResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %q", resp.Status)
	}
}

func TestShareUnauthorized(t *testing.T) {
	srv := NewServer("secret", nil)
	mux := srv.Mux()

	body := `{"type":"text","text":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/share", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestShareNoAuth(t *testing.T) {
	srv := NewServer("secret", nil)
	mux := srv.Mux()

	body := `{"type":"text","text":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/share", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestShareMethodNotAllowed(t *testing.T) {
	srv := NewServer("secret", nil)
	mux := srv.Mux()

	req := httptest.NewRequest(http.MethodGet, "/share", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestValidateRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     ShareRequest
		wantErr bool
	}{
		{"valid text", ShareRequest{Type: "text", Text: "hello"}, false},
		{"valid url", ShareRequest{Type: "url", URL: "https://example.com"}, false},
		{"empty text", ShareRequest{Type: "text", Text: ""}, true},
		{"empty url", ShareRequest{Type: "url", URL: ""}, true},
		{"bad url scheme", ShareRequest{Type: "url", URL: "ftp://foo"}, true},
		{"unknown type", ShareRequest{Type: "file", Text: "x"}, true},
		{"text too long", ShareRequest{Type: "text", Text: string(make([]byte, 4097))}, true},
		{"url too long", ShareRequest{Type: "url", URL: "https://" + string(make([]byte, 2048))}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRequest(&tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRequest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(3, time.Hour) // large window so nothing expires during test

	for i := 0; i < 3; i++ {
		if !rl.allow("client1") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if rl.allow("client1") {
		t.Fatal("4th request should be rate limited")
	}
	// Different client should still be allowed
	if !rl.allow("client2") {
		t.Fatal("different client should be allowed")
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://example.com", "'https://example.com'"},
		{"it's a test", "'it'\\''s a test'"},
		{"$(rm -rf /)", "'$(rm -rf /)'"},
		{"`id`", "'`id`'"},
		{"foo;bar", "'foo;bar'"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
