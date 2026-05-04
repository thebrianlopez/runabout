package main

import "time"

// ConfluenceRenderer converts Confluence REST API ADF responses to markdown artifacts.
// It implements CaptureRenderer and is stateless and pure.
type ConfluenceRenderer struct{}

func NewConfluenceRenderer() *ConfluenceRenderer { return &ConfluenceRenderer{} }

// Render parses the Confluence REST API JSON response (ContentTypeADF),
// converts the embedded ADF document to CommonMark markdown, and returns
// the artifact bytes with YAML frontmatter.
func (r *ConfluenceRenderer) Render(content string, ct ContentType, now time.Time) ([]byte, error) {
	return nil, nil // stub — implemented in M2
}

// ArtifactKey extracts the numeric page ID from a Confluence wiki URL.
// "/wiki/spaces/ENG/pages/123456/My+Page" → "123456"
func (r *ConfluenceRenderer) ArtifactKey(rawURL string) string {
	return "" // stub — implemented in M2
}

var _ CaptureRenderer = (*ConfluenceRenderer)(nil)
