package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendToResearchDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "digest.md")

	err := appendToResearchDigest(path, "https://example.com/article", "Great article about Go", 85, "eng")
	if err != nil {
		t.Fatalf("appendToResearchDigest: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(content)
	if !strings.Contains(s, "https://example.com/article") {
		t.Error("digest should contain URL")
	}
	if !strings.Contains(s, "score: 85") {
		t.Error("digest should contain score")
	}
	if !strings.Contains(s, "profile: eng") {
		t.Error("digest should contain profile")
	}
}

func TestAppendToResearchDigestMultiple(t *testing.T) {
	path := filepath.Join(t.TempDir(), "digest.md")

	appendToResearchDigest(path, "https://a.test", "First article", 80, "eng")
	appendToResearchDigest(path, "https://b.test", "Second article", 90, "research")

	content, _ := os.ReadFile(path)
	if strings.Count(string(content), "## ") != 2 {
		t.Errorf("expected 2 entries, content:\n%s", content)
	}
}

func TestAppendToResearchDigestEmptyPath(t *testing.T) {
	err := appendToResearchDigest("", "https://test.com", "v", 50, "eng")
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestComputeActionRoute(t *testing.T) {
	tests := []struct {
		score     int
		profile   string
		threshold int
		want      string
	}{
		{90, "eng", 80, "draft_jira_ticket"},
		{90, "research", 80, "append_research_digest"},
		{70, "eng", 80, ""},
		{85, "security", 80, "draft_jira_ticket"},
		{85, "dining", 80, "append_research_digest"},
		{50, "eng", 0, ""}, // threshold defaults to 80
	}
	for _, tt := range tests {
		got := computeActionRoute(tt.score, tt.profile, tt.threshold)
		if got != tt.want {
			t.Errorf("computeActionRoute(%d, %q, %d) = %q, want %q",
				tt.score, tt.profile, tt.threshold, got, tt.want)
		}
	}
}
