package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestYtdlpVersion_ParsesReleaseTags(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"2026.08.19", true},
		{"2026.07.04", true},
		{" 2026.07.04\n", true},
		{"2026.08.19.232919", true},
		{"", false},
		{"nightly", false},
		{"2026.08", false},
		{"2026.13.45", false},
	}
	for _, c := range cases {
		_, ok := parseYtdlpVersion(c.in)
		if ok != c.want {
			t.Errorf("parseYtdlpVersion(%q) ok = %v, want %v", c.in, ok, c.want)
		}
	}
}

func TestYtdlpVersion_StaleInstallWarns(t *testing.T) {
	check := ytdlpVersionCheck("2026.07.04", "2026.08.19", nil)

	if check.Status != statusWarn {
		t.Fatalf("status = %q, want %q (message: %s)", check.Status, statusWarn, check.Message)
	}
	if !strings.Contains(check.Message, "2026.07.04") || !strings.Contains(check.Message, "2026.08.19") {
		t.Errorf("message must name both versions, got %q", check.Message)
	}
	if !strings.Contains(check.Message, "46 days behind") {
		t.Errorf("message = %q, want it to report 46 days behind", check.Message)
	}
	if !strings.Contains(check.Message, "403") {
		t.Errorf("message should name the observed failure mode, got %q", check.Message)
	}
}

func TestYtdlpVersion_CurrentInstallIsOK(t *testing.T) {
	check := ytdlpVersionCheck("2026.08.19", "2026.08.19", nil)
	if check.Status != statusOK {
		t.Errorf("status = %q, want %q", check.Status, statusOK)
	}
}

func TestYtdlpVersion_NewerThanLatestIsOK(t *testing.T) {
	check := ytdlpVersionCheck("2026.09.01", "2026.08.19", nil)
	if check.Status != statusOK {
		t.Errorf("status = %q, want %q (a nightly ahead of stable must not warn)", check.Status, statusOK)
	}
}

func TestYtdlpVersion_NetworkFailureDegradesToOK(t *testing.T) {
	check := ytdlpVersionCheck("2026.07.04", "", errors.New("dial tcp: lookup api.github.com: no such host"))

	if check.Status != statusOK {
		t.Fatalf("status = %q, want %q - an offline doctor run must not report a false stale warning", check.Status, statusOK)
	}
	if !strings.Contains(check.Message, "2026.07.04") {
		t.Errorf("message must still report the installed version, got %q", check.Message)
	}
}

func TestYtdlpVersion_UnparseableVersionDegradesToOK(t *testing.T) {
	check := ytdlpVersionCheck("some-custom-build", "2026.08.19", nil)
	if check.Status != statusOK {
		t.Errorf("status = %q, want %q", check.Status, statusOK)
	}
}

func TestYtdlpVersion_FetcherParsesTagName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "2026.08.19"})
	}))
	defer srv.Close()

	got, err := fetchLatestYtdlpVersion(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "2026.08.19" {
		t.Errorf("got %q, want %q", got, "2026.08.19")
	}
}

func TestYtdlpVersion_FetcherErrorsOnNonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	if _, err := fetchLatestYtdlpVersion(context.Background(), srv.URL); err == nil {
		t.Error("want an error on non-200, got nil")
	}
}

func TestYtdlpVersion_FetcherHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := fetchLatestYtdlpVersion(ctx, "https://api.github.com/repos/yt-dlp/yt-dlp/releases/latest"); err == nil {
		t.Error("want an error from a cancelled context, got nil")
	}
}
