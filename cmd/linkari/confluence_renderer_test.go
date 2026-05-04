package main

// F6 ConfluenceRenderer contract tests (CT-1–CT-15) and regression guards (RG-1–RG-3).
// All CT-* tests must FAIL until M2 implements production code.
// RG-2 is verified by running the existing TestJiraClient_* suite unchanged.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// compile-time assertion — ConfluenceRenderer implements CaptureRenderer.
var _ CaptureRenderer = (*ConfluenceRenderer)(nil)

// confluencePageFixture is a minimal Confluence REST API v1 JSON response.
// body.atlas_doc_format.value is a JSON-encoded ADF document string (double-encoded).
const confluencePageFixture = `{
  "id": "123456",
  "title": "Test Page",
  "space": {"key": "ENG", "name": "Engineering"},
  "version": {"number": 3, "when": "2026-05-01T10:00:00.000Z"},
  "body": {
    "atlas_doc_format": {
      "value": "{\"version\":1,\"type\":\"doc\",\"content\":[{\"type\":\"paragraph\",\"content\":[{\"type\":\"text\",\"text\":\"Hello world\"}]}]}"
    }
  },
  "metadata": {
    "labels": {"results": [{"name": "linkari"}]}
  }
}`

// confluenceTableFixture contains an ADF table node in the value field.
const confluenceTableFixture = `{
  "id": "123456",
  "title": "Table Page",
  "space": {"key": "ENG", "name": "Engineering"},
  "version": {"number": 1, "when": "2026-05-01T10:00:00.000Z"},
  "body": {
    "atlas_doc_format": {
      "value": "{\"version\":1,\"type\":\"doc\",\"content\":[{\"type\":\"table\",\"content\":[{\"type\":\"tableRow\",\"content\":[{\"type\":\"tableHeader\",\"content\":[{\"type\":\"paragraph\",\"content\":[{\"type\":\"text\",\"text\":\"Col1\"}]}]},{\"type\":\"tableHeader\",\"content\":[{\"type\":\"paragraph\",\"content\":[{\"type\":\"text\",\"text\":\"Col2\"}]}]}]},{\"type\":\"tableRow\",\"content\":[{\"type\":\"tableCell\",\"content\":[{\"type\":\"paragraph\",\"content\":[{\"type\":\"text\",\"text\":\"A\"}]}]},{\"type\":\"tableCell\",\"content\":[{\"type\":\"paragraph\",\"content\":[{\"type\":\"text\",\"text\":\"B\"}]}]}]}]}]}"
    }
  },
  "metadata": {
    "labels": {"results": []}
  }
}`

// confluenceMediaFixture contains an ADF mediaGroup node in the value field.
const confluenceMediaFixture = `{
  "id": "123456",
  "title": "Media Page",
  "space": {"key": "ENG", "name": "Engineering"},
  "version": {"number": 1, "when": "2026-05-01T10:00:00.000Z"},
  "body": {
    "atlas_doc_format": {
      "value": "{\"version\":1,\"type\":\"doc\",\"content\":[{\"type\":\"mediaGroup\",\"content\":[{\"type\":\"media\",\"attrs\":{\"id\":\"abc-123\",\"type\":\"file\",\"alt\":\"diagram.png\"}}]}]}"
    }
  },
  "metadata": {
    "labels": {"results": []}
  }
}`

// confluenceUnknownNodeFixture contains an unsupported ADF node type ("panel").
const confluenceUnknownNodeFixture = `{
  "id": "123456",
  "title": "Unknown Node Page",
  "space": {"key": "ENG", "name": "Engineering"},
  "version": {"number": 1, "when": "2026-05-01T10:00:00.000Z"},
  "body": {
    "atlas_doc_format": {
      "value": "{\"version\":1,\"type\":\"doc\",\"content\":[{\"type\":\"panel\",\"content\":[{\"type\":\"paragraph\",\"content\":[{\"type\":\"text\",\"text\":\"panel body\"}]}]},{\"type\":\"paragraph\",\"content\":[{\"type\":\"text\",\"text\":\"after panel\"}]}]}"
    }
  },
  "metadata": {
    "labels": {"results": []}
  }
}`

// confluenceEmptyTitleFixture has an empty title field.
const confluenceEmptyTitleFixture = `{
  "id": "123456",
  "title": "",
  "space": {"key": "ENG", "name": "Engineering"},
  "version": {"number": 1, "when": "2026-05-01T10:00:00.000Z"},
  "body": {
    "atlas_doc_format": {
      "value": "{\"version\":1,\"type\":\"doc\",\"content\":[]}"
    }
  },
  "metadata": {
    "labels": {"results": []}
  }
}`

// confluenceMalformedADFFixture has a malformed (non-JSON) ADF value.
const confluenceMalformedADFFixture = `{
  "id": "123456",
  "title": "Bad ADF Page",
  "space": {"key": "ENG", "name": "Engineering"},
  "version": {"number": 1, "when": "2026-05-01T10:00:00.000Z"},
  "body": {
    "atlas_doc_format": {
      "value": "NOT VALID JSON {{{"
    }
  },
  "metadata": {
    "labels": {"results": []}
  }
}`

var fixedNow = time.Date(2026, 5, 4, 13, 39, 37, 0, time.UTC)

// CT-1: Valid Confluence REST API response → Render returns non-empty bytes; no error.
func TestConfluenceRenderer_CT1_ValidResponse_NonEmptyBytes(t *testing.T) {
	r := NewConfluenceRenderer()
	out, err := r.Render(confluencePageFixture, ContentTypeADF, fixedNow)
	if err != nil {
		t.Fatalf("CT-1: unexpected error: %v", err)
	}
	if len(out) == 0 {
		t.Error("CT-1: expected non-empty bytes, got empty")
	}
}

// CT-2: Frontmatter fields — source, id, title, space, version, last_modified, url, captured_at all present.
func TestConfluenceRenderer_CT2_FrontmatterFieldsPresent(t *testing.T) {
	r := NewConfluenceRenderer()
	out, err := r.Render(confluencePageFixture, ContentTypeADF, fixedNow)
	if err != nil {
		t.Fatalf("CT-2: unexpected error: %v", err)
	}
	s := string(out)
	for _, field := range []string{"source:", "id:", "title:", "space:", "version:", "last_modified:", "url:", "captured_at:"} {
		if !strings.Contains(s, field) {
			t.Errorf("CT-2: frontmatter missing field %q", field)
		}
	}
}

// CT-3: ADF paragraph node → output contains paragraph text followed by blank line.
func TestConfluenceRenderer_CT3_ParagraphNode(t *testing.T) {
	r := NewConfluenceRenderer()
	out, err := r.Render(confluencePageFixture, ContentTypeADF, fixedNow)
	if err != nil {
		t.Fatalf("CT-3: unexpected error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "Hello world") {
		t.Error("CT-3: output missing paragraph text 'Hello world'")
	}
}

// CT-4: ADF heading level 2 → output contains ## heading text.
func TestConfluenceRenderer_CT4_HeadingLevel2(t *testing.T) {
	fixture := `{
  "id": "123456",
  "title": "Heading Page",
  "space": {"key": "ENG", "name": "Engineering"},
  "version": {"number": 1, "when": "2026-05-01T10:00:00.000Z"},
  "body": {
    "atlas_doc_format": {
      "value": "{\"version\":1,\"type\":\"doc\",\"content\":[{\"type\":\"heading\",\"attrs\":{\"level\":2},\"content\":[{\"type\":\"text\",\"text\":\"Section Header\"}]}]}"
    }
  },
  "metadata": {"labels": {"results": []}}
}`
	r := NewConfluenceRenderer()
	out, err := r.Render(fixture, ContentTypeADF, fixedNow)
	if err != nil {
		t.Fatalf("CT-4: unexpected error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "## Section Header") {
		t.Errorf("CT-4: output missing '## Section Header', got:\n%s", s)
	}
}

// CT-5: ADF codeBlock with language → output contains ```go\n...\n```.
func TestConfluenceRenderer_CT5_CodeBlockWithLanguage(t *testing.T) {
	fixture := `{
  "id": "123456",
  "title": "Code Page",
  "space": {"key": "ENG", "name": "Engineering"},
  "version": {"number": 1, "when": "2026-05-01T10:00:00.000Z"},
  "body": {
    "atlas_doc_format": {
      "value": "{\"version\":1,\"type\":\"doc\",\"content\":[{\"type\":\"codeBlock\",\"attrs\":{\"language\":\"go\"},\"content\":[{\"type\":\"text\",\"text\":\"fmt.Println(\\\"hello\\\")\"}]}]}"
    }
  },
  "metadata": {"labels": {"results": []}}
}`
	r := NewConfluenceRenderer()
	out, err := r.Render(fixture, ContentTypeADF, fixedNow)
	if err != nil {
		t.Fatalf("CT-5: unexpected error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "```go") {
		t.Errorf("CT-5: output missing '```go' fence, got:\n%s", s)
	}
	if !strings.Contains(s, "fmt.Println") {
		t.Error("CT-5: output missing code body 'fmt.Println'")
	}
}

// CT-6: ADF bulletList → output contains - item lines.
func TestConfluenceRenderer_CT6_BulletList(t *testing.T) {
	fixture := `{
  "id": "123456",
  "title": "List Page",
  "space": {"key": "ENG", "name": "Engineering"},
  "version": {"number": 1, "when": "2026-05-01T10:00:00.000Z"},
  "body": {
    "atlas_doc_format": {
      "value": "{\"version\":1,\"type\":\"doc\",\"content\":[{\"type\":\"bulletList\",\"content\":[{\"type\":\"listItem\",\"content\":[{\"type\":\"paragraph\",\"content\":[{\"type\":\"text\",\"text\":\"first item\"}]}]},{\"type\":\"listItem\",\"content\":[{\"type\":\"paragraph\",\"content\":[{\"type\":\"text\",\"text\":\"second item\"}]}]}]}]}"
    }
  },
  "metadata": {"labels": {"results": []}}
}`
	r := NewConfluenceRenderer()
	out, err := r.Render(fixture, ContentTypeADF, fixedNow)
	if err != nil {
		t.Fatalf("CT-6: unexpected error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "- first item") {
		t.Errorf("CT-6: output missing '- first item', got:\n%s", s)
	}
	if !strings.Contains(s, "- second item") {
		t.Errorf("CT-6: output missing '- second item', got:\n%s", s)
	}
}

// CT-7: ADF table with header row → output is valid GFM table with |---| separator row.
func TestConfluenceRenderer_CT7_TableGFM(t *testing.T) {
	r := NewConfluenceRenderer()
	out, err := r.Render(confluenceTableFixture, ContentTypeADF, fixedNow)
	if err != nil {
		t.Fatalf("CT-7: unexpected error: %v", err)
	}
	s := string(out)
	// GFM table must have a separator row with dashes.
	if !strings.Contains(s, "|---") && !strings.Contains(s, "| ---") {
		t.Errorf("CT-7: output missing GFM table separator row '|---|', got:\n%s", s)
	}
	if !strings.Contains(s, "Col1") || !strings.Contains(s, "Col2") {
		t.Errorf("CT-7: output missing table headers Col1/Col2, got:\n%s", s)
	}
}

// CT-8: ADF media node → attachments: frontmatter list contains the attachment identifier.
func TestConfluenceRenderer_CT8_MediaNodeAttachments(t *testing.T) {
	r := NewConfluenceRenderer()
	out, err := r.Render(confluenceMediaFixture, ContentTypeADF, fixedNow)
	if err != nil {
		t.Fatalf("CT-8: unexpected error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "attachments:") {
		t.Error("CT-8: output missing 'attachments:' frontmatter key")
	}
	// Should contain alt text "diagram.png" or id "abc-123" as the attachment identifier.
	if !strings.Contains(s, "diagram.png") && !strings.Contains(s, "abc-123") {
		t.Errorf("CT-8: output missing attachment identifier (diagram.png or abc-123), got:\n%s", s)
	}
}

// CT-9: Unknown ADF node type "panel" → Render returns non-empty bytes (node skipped).
// adf_unsupported_block is logged but cannot be asserted on at the test level.
func TestConfluenceRenderer_CT9_UnknownNodeSkipped(t *testing.T) {
	r := NewConfluenceRenderer()
	out, err := r.Render(confluenceUnknownNodeFixture, ContentTypeADF, fixedNow)
	if err != nil {
		t.Fatalf("CT-9: unexpected error (unknown node must not cause error): %v", err)
	}
	if len(out) == 0 {
		t.Error("CT-9: expected non-empty bytes after skipping unknown node, got empty")
	}
	// The text after the unknown node should still be rendered.
	s := string(out)
	if !strings.Contains(s, "after panel") {
		t.Errorf("CT-9: output missing text after unknown node ('after panel'), got:\n%s", s)
	}
}

// CT-10: Empty title in API response → Render returns (nil, error) containing "render_missing_title".
func TestConfluenceRenderer_CT10_EmptyTitle_Error(t *testing.T) {
	r := NewConfluenceRenderer()
	out, err := r.Render(confluenceEmptyTitleFixture, ContentTypeADF, fixedNow)
	if err == nil {
		t.Fatal("CT-10: expected render_missing_title error, got nil")
	}
	if !strings.Contains(err.Error(), "render_missing_title") {
		t.Errorf("CT-10: expected error containing 'render_missing_title', got: %v", err)
	}
	if out != nil {
		t.Error("CT-10: expected nil bytes on error, got non-nil")
	}
}

// CT-11: body.atlas_doc_format.value is malformed JSON → Render returns (nil, error) containing "confluence_decode_adf_error".
func TestConfluenceRenderer_CT11_MalformedADF_Error(t *testing.T) {
	r := NewConfluenceRenderer()
	out, err := r.Render(confluenceMalformedADFFixture, ContentTypeADF, fixedNow)
	if err == nil {
		t.Fatal("CT-11: expected confluence_decode_adf_error, got nil")
	}
	if !strings.Contains(err.Error(), "confluence_decode_adf_error") {
		t.Errorf("CT-11: expected error containing 'confluence_decode_adf_error', got: %v", err)
	}
	if out != nil {
		t.Error("CT-11: expected nil bytes on error, got non-nil")
	}
}

// CT-12: ArtifactKey with standard page URL → returns numeric page ID.
func TestConfluenceRenderer_CT12_ArtifactKey_StandardURL(t *testing.T) {
	r := NewConfluenceRenderer()
	got := r.ArtifactKey("https://org.atlassian.net/wiki/spaces/ENG/pages/123456")
	if got != "123456" {
		t.Errorf("CT-12: ArtifactKey = %q, want %q", got, "123456")
	}
}

// CT-13: ArtifactKey with trailing slug → returns numeric page ID, not slug.
func TestConfluenceRenderer_CT13_ArtifactKey_TrailingSlug(t *testing.T) {
	r := NewConfluenceRenderer()
	got := r.ArtifactKey("https://org.atlassian.net/wiki/spaces/ENG/pages/123456/My+Page+Title")
	if got != "123456" {
		t.Errorf("CT-13: ArtifactKey = %q, want %q", got, "123456")
	}
}

// CT-14: ArtifactKey with non-wiki URL → returns "confluence-unknown" fallback.
func TestConfluenceRenderer_CT14_ArtifactKey_NonWikiURL(t *testing.T) {
	r := NewConfluenceRenderer()
	got := r.ArtifactKey("https://example.com/some/other/path")
	if got != "confluence-unknown" {
		t.Errorf("CT-14: ArtifactKey = %q, want %q", got, "confluence-unknown")
	}
}

// CT-15: FetchConfluenceADF receives 401 → returns ErrAtlassianAuth.
func TestConfluenceRenderer_CT15_FetchADF_Auth401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &JiraClient{
		Domain:     "acme.atlassian.net",
		Username:   "u",
		Password:   "p",
		baseURL:    srv.URL,
		httpClient: srv.Client(),
	}
	_, err := c.FetchConfluenceADF(context.Background(), "123456")
	if err != ErrAtlassianAuth {
		t.Errorf("CT-15: expected ErrAtlassianAuth, got %v", err)
	}
}

// RG-1: JiraClient.Fetch for /browse/ URLs still returns ContentTypeJSON.
// Confluence fetch change must not affect the Jira issue path.
func TestConfluenceRenderer_RG1_JiraFetch_StillReturnsContentTypeJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"key":"PROJ-1","fields":{"summary":"Test"}}`))
	}))
	defer srv.Close()

	c := &JiraClient{
		Domain:     "acme.atlassian.net",
		Username:   "u",
		Password:   "p",
		baseURL:    srv.URL,
		httpClient: srv.Client(),
	}
	u, _ := url.Parse("https://acme.atlassian.net/browse/PROJ-1")
	_, ct, err := c.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("RG-1: unexpected error: %v", err)
	}
	if ct != ContentTypeJSON {
		t.Errorf("RG-1: expected ContentTypeJSON for Jira browse URL, got %v", ct)
	}
}

// RG-2: All existing TestJiraClient_* tests pass unchanged.
// This is enforced by running the full suite; no new assertion needed here.
// Verified by: go test ./cmd/linkari/... -run TestJiraClient
func TestConfluenceRenderer_RG2_ExistingJiraTestsUnchanged(t *testing.T) {
	// Compile-time: JiraClient still satisfies DomainClient.
	var _ DomainClient = (*JiraClient)(nil)
	// Compile-time: JiraRenderer still satisfies CaptureRenderer.
	var _ CaptureRenderer = (*JiraRenderer)(nil)
}

// RG-3: ConfluenceRenderer.Render is pure — same fixture input → byte-identical output on two consecutive calls.
func TestConfluenceRenderer_RG3_Pure(t *testing.T) {
	r := NewConfluenceRenderer()
	fixed := time.Date(2026, 5, 4, 13, 39, 37, 0, time.UTC)
	out1, err := r.Render(confluencePageFixture, ContentTypeADF, fixed)
	if err != nil {
		t.Fatalf("RG-3: first render error: %v", err)
	}
	out2, err := r.Render(confluencePageFixture, ContentTypeADF, fixed)
	if err != nil {
		t.Fatalf("RG-3: second render error: %v", err)
	}
	if !bytes.Equal(out1, out2) {
		t.Error("RG-3: ConfluenceRenderer.Render is not pure — outputs differ across calls")
	}
}
