package version

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestGet_Defaults(t *testing.T) {
	info := Get()

	if info.GoVersion == "" {
		t.Error("GoVersion should not be empty")
	}
	if info.Platform == "" {
		t.Error("Platform should not be empty")
	}
	// Version, Commit, BuildDate may be "dev"/"unknown" in test builds —
	// just verify they are non-empty strings.
	if info.Version == "" {
		t.Error("Version should not be empty")
	}
	if info.Commit == "" {
		t.Error("Commit should not be empty")
	}
	if info.BuildDate == "" {
		t.Error("BuildDate should not be empty")
	}
}

func TestInfo_String(t *testing.T) {
	info := Info{
		Version:   "v1.2.3",
		Commit:    "abc1234",
		BuildDate: "2026-02-23T00:00:00Z",
		GoVersion: "go1.25.4",
		Platform:  "linux/amd64",
	}

	s := info.String()

	for _, want := range []string{"workctl", "v1.2.3", "abc1234", "2026-02-23T00:00:00Z", "go1.25.4", "linux/amd64"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
}

func TestInfo_JSON(t *testing.T) {
	info := Info{
		Version:   "v1.2.3",
		Commit:    "abc1234",
		BuildDate: "2026-02-23T00:00:00Z",
		GoVersion: "go1.25.4",
		Platform:  "linux/amd64",
	}

	raw := info.JSON()

	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("JSON() produced invalid JSON: %v", err)
	}

	checks := map[string]string{
		"version":    "v1.2.3",
		"commit":     "abc1234",
		"build_date": "2026-02-23T00:00:00Z",
		"go_version": "go1.25.4",
		"platform":   "linux/amd64",
	}
	for key, want := range checks {
		if got := out[key]; got != want {
			t.Errorf("JSON field %q = %q, want %q", key, got, want)
		}
	}
}

func TestGet_Platform(t *testing.T) {
	info := Get()
	want := runtime.GOOS + "/" + runtime.GOARCH
	if info.Platform != want {
		t.Errorf("Platform = %q, want %q", info.Platform, want)
	}
}
