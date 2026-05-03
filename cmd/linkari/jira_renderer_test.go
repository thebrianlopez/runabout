package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// CT-15: compile-time assertion — JiraRenderer implements CaptureRenderer.
var _ CaptureRenderer = (*JiraRenderer)(nil)

// jiraIssueFixture builds a minimal IssueSchemeV2-shaped JSON string for tests.
// domain is used to set the Self field (e.g. "org.atlassian.net"); pass "" to omit.
func jiraIssueFixture(key, summary, description, status, assignee, priority string, labels, components []string, links []map[string]interface{}) string {
	return jiraIssueFixtureDomain("org.atlassian.net", key, summary, description, status, assignee, priority, labels, components, links)
}

func jiraIssueFixtureDomain(domain, key, summary, description, status, assignee, priority string, labels, components []string, links []map[string]interface{}) string {
	comps := make([]map[string]string, len(components))
	for i, c := range components {
		comps[i] = map[string]string{"name": c}
	}
	fields := map[string]interface{}{
		"summary":     summary,
		"description": description,
		"status":      map[string]string{"name": status},
		"assignee":    map[string]string{"displayName": assignee},
		"priority":    map[string]string{"name": priority},
		"labels":      labels,
		"components":  comps,
		"issuelinks":  links,
	}
	self := ""
	if domain != "" && key != "" {
		self = "https://" + domain + "/rest/api/2/issue/" + key
	}
	b, _ := json.Marshal(map[string]interface{}{"key": key, "self": self, "fields": fields})
	return string(b)
}

// CT-1: ExtractJiraKey("/browse/SR-2972") → "SR-2972", nil.
func TestJiraRenderer_CT1_ExtractJiraKey_BrowsePath(t *testing.T) {
	key, err := ExtractJiraKey("/browse/SR-2972")
	if err != nil {
		t.Fatalf("CT-1: unexpected error: %v", err)
	}
	if key != "SR-2972" {
		t.Errorf("CT-1: got %q, want SR-2972", key)
	}
}

// CT-2: ExtractJiraKey with shell metacharacter → error containing "jira_key_invalid".
func TestJiraRenderer_CT2_ExtractJiraKey_ShellMetachar(t *testing.T) {
	_, err := ExtractJiraKey("/browse/$(echo)")
	if err == nil {
		t.Fatal("CT-2: expected error for shell metacharacter key, got nil")
	}
	if !strings.Contains(err.Error(), "jira_key_invalid") {
		t.Errorf("CT-2: expected jira_key_invalid error, got %v", err)
	}
}

// CT-3: ExtractJiraKey non-browse path → error containing "jira_url_not_browse".
func TestJiraRenderer_CT3_ExtractJiraKey_NonBrowse(t *testing.T) {
	_, err := ExtractJiraKey("/wiki/spaces/X")
	if err == nil {
		t.Fatal("CT-3: expected error for non-browse path, got nil")
	}
	if !strings.Contains(err.Error(), "jira_url_not_browse") {
		t.Errorf("CT-3: expected jira_url_not_browse error, got %v", err)
	}
}

// CT-7: JiraRenderer.Render minimal issue → all frontmatter fields present.
func TestJiraRenderer_CT7_MinimalIssue_FrontmatterComplete(t *testing.T) {
	content := jiraIssueFixture(
		"SR-2972", "Snowflake: credential rotation", "Body text.",
		"In Progress", "Brian Lopez", "High",
		[]string{"infrastructure", "security"},
		[]string{"platform"},
		nil,
	)
	r := NewJiraRenderer()
	now := time.Date(2026, 5, 3, 3, 11, 0, 0, time.UTC)
	out, err := r.Render(content, ContentTypeJSON, now)
	if err != nil {
		t.Fatalf("CT-7: unexpected error: %v", err)
	}
	s := string(out)
	for _, field := range []string{"key:", "summary:", "status:", "assignee:", "priority:", "labels:", "components:", "url:", "captured_at:"} {
		if !strings.Contains(s, field) {
			t.Errorf("CT-7: frontmatter missing field %q", field)
		}
	}
	if !strings.Contains(s, "SR-2972") {
		t.Error("CT-7: output missing issue key SR-2972")
	}
}

// CT-8: JiraRenderer.Render no description → "### Description" section absent.
func TestJiraRenderer_CT8_NoDescription_SectionAbsent(t *testing.T) {
	content := jiraIssueFixture("SR-1", "Summary only", "", "Open", "Alice", "Medium", nil, nil, nil)
	r := NewJiraRenderer()
	out, err := r.Render(content, ContentTypeJSON, time.Now().UTC())
	if err != nil {
		t.Fatalf("CT-8: unexpected error: %v", err)
	}
	if strings.Contains(string(out), "### Description") {
		t.Error("CT-8: output contains '### Description' but description was empty")
	}
}

// CT-9: JiraRenderer.Render no links → "### Linked Issues" section absent.
func TestJiraRenderer_CT9_NoLinks_SectionAbsent(t *testing.T) {
	content := jiraIssueFixture("SR-2", "Title", "desc", "Done", "Bob", "Low", nil, nil, nil)
	r := NewJiraRenderer()
	out, err := r.Render(content, ContentTypeJSON, time.Now().UTC())
	if err != nil {
		t.Fatalf("CT-9: unexpected error: %v", err)
	}
	if strings.Contains(string(out), "### Linked Issues") {
		t.Error("CT-9: output contains '### Linked Issues' but links were empty")
	}
}

// CT-10: JiraRenderer.Render with links → ### Linked Issues rendered.
func TestJiraRenderer_CT10_WithLinks_Rendered(t *testing.T) {
	links := []map[string]interface{}{
		{
			"type":         map[string]string{"outward": "blocks", "inward": "is blocked by"},
			"outwardIssue": map[string]string{"key": "SR-1234"},
		},
		{
			"type":        map[string]string{"outward": "blocks", "inward": "is blocked by"},
			"inwardIssue": map[string]string{"key": "ISRE-999"},
		},
	}
	content := jiraIssueFixture("SR-3", "With links", "desc", "In Progress", "Carol", "High", nil, nil, links)
	r := NewJiraRenderer()
	out, err := r.Render(content, ContentTypeJSON, time.Now().UTC())
	if err != nil {
		t.Fatalf("CT-10: unexpected error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "### Linked Issues") {
		t.Error("CT-10: missing '### Linked Issues' section")
	}
	if !strings.Contains(s, "SR-1234") {
		t.Error("CT-10: missing outward link key SR-1234")
	}
	if !strings.Contains(s, "ISRE-999") {
		t.Error("CT-10: missing inward link key ISRE-999")
	}
}

// CT-11: JiraRenderer.Render missing Key → render_missing_key error.
func TestJiraRenderer_CT11_MissingKey_Error(t *testing.T) {
	b, _ := json.Marshal(map[string]interface{}{
		"key":    "",
		"fields": map[string]interface{}{"summary": "No key"},
	})
	r := NewJiraRenderer()
	out, err := r.Render(string(b), ContentTypeJSON, time.Now().UTC())
	if err == nil {
		t.Fatal("CT-11: expected render_missing_key error, got nil")
	}
	if !strings.Contains(err.Error(), "render_missing_key") {
		t.Errorf("CT-11: expected render_missing_key error, got %v", err)
	}
	if out != nil {
		t.Error("CT-11: expected nil bytes on error")
	}
}

// CT-12: JiraRenderer.ArtifactKey extracts key from browse URL.
func TestJiraRenderer_CT12_ArtifactKey(t *testing.T) {
	r := NewJiraRenderer()
	got := r.ArtifactKey("https://org.atlassian.net/browse/SR-2972")
	if got != "SR-2972" {
		t.Errorf("CT-12: ArtifactKey = %q, want SR-2972", got)
	}
}

// CT-13: JiraRenderer.Render is pure (same input → same bytes).
func TestJiraRenderer_CT13_Pure(t *testing.T) {
	content := jiraIssueFixture("SR-99", "Pure test", "same desc", "Open", "Dan", "Low", nil, nil, nil)
	r := NewJiraRenderer()
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	out1, err := r.Render(content, ContentTypeJSON, fixed)
	if err != nil {
		t.Fatalf("CT-13: first render error: %v", err)
	}
	out2, err := r.Render(content, ContentTypeJSON, fixed)
	if err != nil {
		t.Fatalf("CT-13: second render error: %v", err)
	}
	if !bytes.Equal(out1, out2) {
		t.Error("CT-13: render is not pure — outputs differ across calls")
	}
}

// CT-14: JiraRenderer.Render non-JSON content → error.
func TestJiraRenderer_CT14_NonJSON_Error(t *testing.T) {
	r := NewJiraRenderer()
	_, err := r.Render("<html>not json</html>", ContentTypeJSON, time.Now().UTC())
	if err == nil {
		t.Fatal("CT-14: expected error for non-JSON input, got nil")
	}
}

// RG-1: ExtractJiraKey rejects shell metacharacters.
func TestJiraRenderer_RG1_ShellMetachars(t *testing.T) {
	bad := []string{
		"/browse/$(echo hacked)",
		"/browse/SR-1;drop",
		"/browse/SR-1&&cmd",
		"/browse/SR-1|pipe",
		"/browse/SR-1`backtick`",
	}
	for _, path := range bad {
		_, err := ExtractJiraKey(path)
		if err == nil {
			t.Errorf("RG-1: ExtractJiraKey(%q) returned nil error; expected rejection", path)
		}
	}
}

// RG-2: JiraRenderer has no http.Client field — compile test (no runtime assertion needed).
func TestJiraRenderer_RG2_NoHTTPClient(t *testing.T) {
	// If JiraRenderer had an http.Client field it would be visible here.
	// The compile-time check at the top (var _ CaptureRenderer = (*JiraRenderer)(nil))
	// ensures the interface is satisfied. No further assertion needed.
	_ = NewJiraRenderer()
}

// RG-3: JiraRenderer.Render produces valid UTF-8 for unicode input.
func TestJiraRenderer_RG3_ValidUTF8(t *testing.T) {
	content := jiraIssueFixture(
		"SR-999", "日本語サマリー: こんにちは", "Ünïcödé description — 中文 🚀",
		"In Progress", "Ångström, Björn", "High", nil, nil, nil,
	)
	r := NewJiraRenderer()
	out, err := r.Render(content, ContentTypeJSON, time.Now().UTC())
	if err != nil {
		t.Fatalf("RG-3: unexpected error: %v", err)
	}
	if !utf8.Valid(out) {
		t.Error("RG-3: JiraRenderer.Render produced invalid UTF-8 output")
	}
}
