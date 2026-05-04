package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

// ConfluenceRenderer converts Confluence REST API ADF responses to markdown artifacts.
// It implements CaptureRenderer and is stateless and pure.
type ConfluenceRenderer struct{}

func NewConfluenceRenderer() *ConfluenceRenderer { return &ConfluenceRenderer{} }

// confluenceAPIResponse is the outer struct for the Confluence REST API v1 response.
type confluenceAPIResponse struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Space struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"space"`
	Version struct {
		Number int    `json:"number"`
		When   string `json:"when"`
	} `json:"version"`
	Body struct {
		AtlasDocFormat struct {
			Value string `json:"value"`
		} `json:"atlas_doc_format"`
	} `json:"body"`
	Metadata struct {
		Labels struct {
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		} `json:"labels"`
	} `json:"metadata"`
}

// adfNode represents a node in the Atlassian Document Format tree.
type adfNode struct {
	Type    string                 `json:"type"`
	Content []adfNode              `json:"content"`
	Text    string                 `json:"text"`
	Attrs   map[string]interface{} `json:"attrs"`
	Marks   []struct {
		Type  string                 `json:"type"`
		Attrs map[string]interface{} `json:"attrs"`
	} `json:"marks"`
}

// ArtifactKey extracts the numeric page ID from a Confluence wiki URL.
// "/wiki/spaces/ENG/pages/123456/My+Page" → "123456"
func (r *ConfluenceRenderer) ArtifactKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "confluence-unknown"
	}
	path := u.Path
	rest, ok := strings.CutPrefix(path, "/wiki/spaces/")
	if !ok {
		return "confluence-unknown"
	}
	// rest = "{space}/pages/{pageID}[/...]"
	parts := strings.SplitN(rest, "/", 4) // [space, "pages", pageID, ...]
	if len(parts) < 3 || parts[1] != "pages" || parts[2] == "" {
		return "confluence-unknown"
	}
	return parts[2]
}

// Render parses the Confluence REST API JSON response (ContentTypeADF),
// converts the embedded ADF document to CommonMark markdown, and returns
// the artifact bytes with YAML frontmatter.
func (r *ConfluenceRenderer) Render(content string, ct ContentType, now time.Time) ([]byte, error) {
	// Step 1: outer JSON unmarshal.
	var resp confluenceAPIResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		return nil, fmt.Errorf("confluence_decode_response_error: %w", err)
	}

	// Step 2: validate title.
	if resp.Title == "" {
		return nil, fmt.Errorf("render_missing_title: title field is empty")
	}

	// Step 3: inner JSON unmarshal of the ADF value string.
	var adfDoc adfNode
	if err := json.Unmarshal([]byte(resp.Body.AtlasDocFormat.Value), &adfDoc); err != nil {
		return nil, fmt.Errorf("confluence_decode_adf_error: %w", err)
	}

	// Step 4: collect attachments and render body.
	var attachments []string
	body := renderADFNode(adfDoc, &attachments)

	// Step 5: render YAML frontmatter.
	var b strings.Builder

	// Collect labels.
	labelNames := make([]string, 0, len(resp.Metadata.Labels.Results))
	for _, l := range resp.Metadata.Labels.Results {
		labelNames = append(labelNames, l.Name)
	}

	capturedAt := now.UTC().Format(time.RFC3339)

	b.WriteString("---\n")
	b.WriteString("source: confluence\n")
	fmt.Fprintf(&b, "id: %q\n", resp.ID)
	fmt.Fprintf(&b, "title: %q\n", resp.Title)
	fmt.Fprintf(&b, "space: %s\n", resp.Space.Key)
	fmt.Fprintf(&b, "space_name: %s\n", resp.Space.Name)
	fmt.Fprintf(&b, "version: %d\n", resp.Version.Number)
	fmt.Fprintf(&b, "last_modified: %q\n", resp.Version.When)
	if len(labelNames) > 0 {
		fmt.Fprintf(&b, "labels: [%s]\n", strings.Join(labelNames, ", "))
	} else {
		b.WriteString("labels: []\n")
	}
	if len(attachments) > 0 {
		fmt.Fprintf(&b, "attachments: [%s]\n", strings.Join(attachments, ", "))
	} else {
		b.WriteString("attachments: []\n")
	}
	b.WriteString("url: \"\"\n")
	fmt.Fprintf(&b, "captured_at: %s\n", capturedAt)
	b.WriteString("---\n")

	// Step 6: title heading + body.
	fmt.Fprintf(&b, "\n# %s\n\n", resp.Title)
	b.WriteString(body)

	return []byte(b.String()), nil
}

// renderADFNode converts a single ADF node (and its subtree) to markdown.
// Attachments discovered in media nodes are appended to the attachments slice.
func renderADFNode(node adfNode, attachments *[]string) string {
	switch node.Type {
	case "doc":
		var parts []string
		for _, child := range node.Content {
			parts = append(parts, renderADFNode(child, attachments))
		}
		return strings.Join(parts, "")

	case "paragraph":
		var inner strings.Builder
		for _, child := range node.Content {
			inner.WriteString(renderADFNode(child, attachments))
		}
		return inner.String() + "\n\n"

	case "heading":
		level := 1
		if node.Attrs != nil {
			if v, ok := node.Attrs["level"]; ok {
				switch lv := v.(type) {
				case float64:
					level = int(lv)
				case int:
					level = lv
				}
			}
		}
		var inner strings.Builder
		for _, child := range node.Content {
			inner.WriteString(renderADFNode(child, attachments))
		}
		return strings.Repeat("#", level) + " " + inner.String() + "\n\n"

	case "text":
		text := node.Text
		for i := len(node.Marks) - 1; i >= 0; i-- {
			mark := node.Marks[i]
			switch mark.Type {
			case "strong":
				text = "**" + text + "**"
			case "em":
				text = "*" + text + "*"
			case "code":
				text = "`" + text + "`"
			case "strike":
				text = "~~" + text + "~~"
			case "link":
				href := ""
				if mark.Attrs != nil {
					if u, ok := mark.Attrs["href"]; ok {
						href, _ = u.(string)
					}
				}
				text = "[" + text + "](" + href + ")"
			}
		}
		return text

	case "hardBreak":
		return "\n"

	case "rule":
		return "\n---\n"

	case "codeBlock":
		lang := ""
		if node.Attrs != nil {
			if v, ok := node.Attrs["language"]; ok {
				lang, _ = v.(string)
			}
		}
		var inner strings.Builder
		for _, child := range node.Content {
			inner.WriteString(renderADFNode(child, attachments))
		}
		return "```" + lang + "\n" + inner.String() + "\n```\n\n"

	case "blockquote":
		var inner strings.Builder
		for _, child := range node.Content {
			inner.WriteString(renderADFNode(child, attachments))
		}
		lines := strings.Split(strings.TrimRight(inner.String(), "\n"), "\n")
		var quoted strings.Builder
		for _, line := range lines {
			quoted.WriteString("> " + line + "\n")
		}
		return quoted.String() + "\n"

	case "bulletList":
		var result strings.Builder
		for _, child := range node.Content {
			item := strings.TrimRight(renderADFNode(child, attachments), "\n ")
			result.WriteString("- " + item + "\n")
		}
		return result.String() + "\n"

	case "orderedList":
		var result strings.Builder
		for _, child := range node.Content {
			item := strings.TrimRight(renderADFNode(child, attachments), "\n ")
			result.WriteString("1. " + item + "\n")
		}
		return result.String() + "\n"

	case "listItem":
		var inner strings.Builder
		for _, child := range node.Content {
			inner.WriteString(renderADFNode(child, attachments))
		}
		return strings.TrimRight(inner.String(), "\n ")

	case "table":
		return renderADFTable(node, attachments)

	case "tableRow", "tableHeader", "tableCell":
		// Handled inside renderADFTable; should not appear at top level.
		var inner strings.Builder
		for _, child := range node.Content {
			inner.WriteString(renderADFNode(child, attachments))
		}
		return strings.TrimRight(inner.String(), "\n ")

	case "media":
		id := ""
		if node.Attrs != nil {
			if v, ok := node.Attrs["alt"]; ok && v != nil {
				if s, ok := v.(string); ok && s != "" {
					id = s
				}
			}
			if id == "" {
				if v, ok := node.Attrs["id"]; ok && v != nil {
					if s, ok := v.(string); ok && s != "" {
						id = s
					}
				}
			}
		}
		if id != "" {
			*attachments = append(*attachments, id)
		}
		return ""

	case "mediaGroup":
		for _, child := range node.Content {
			renderADFNode(child, attachments)
		}
		return ""

	case "mention":
		text := ""
		if node.Attrs != nil {
			if v, ok := node.Attrs["text"]; ok && v != nil {
				text, _ = v.(string)
			}
		}
		return "@" + text

	case "emoji":
		text := ""
		if node.Attrs != nil {
			if v, ok := node.Attrs["text"]; ok && v != nil {
				text, _ = v.(string)
			}
		}
		if text == "" {
			text = ":emoji:"
		}
		return text

	case "inlineCard", "blockCard":
		u := ""
		if node.Attrs != nil {
			if v, ok := node.Attrs["url"]; ok && v != nil {
				u, _ = v.(string)
			}
		}
		return "[" + u + "](" + u + ")"

	default:
		slog.Warn("adf_unsupported_block", "type", node.Type)
		return ""
	}
}

// renderADFTable renders a table ADF node as a GFM table.
func renderADFTable(node adfNode, attachments *[]string) string {
	if len(node.Content) == 0 {
		return ""
	}

	var rows [][]string
	isHeaderRow := make([]bool, 0, len(node.Content))

	for _, row := range node.Content {
		if row.Type != "tableRow" {
			continue
		}
		var cells []string
		rowIsHeader := false
		for _, cell := range row.Content {
			if cell.Type == "tableHeader" {
				rowIsHeader = true
			}
			var inner strings.Builder
			for _, child := range cell.Content {
				inner.WriteString(renderADFNode(child, attachments))
			}
			cellText := strings.TrimRight(inner.String(), "\n ")
			cells = append(cells, cellText)
		}
		rows = append(rows, cells)
		isHeaderRow = append(isHeaderRow, rowIsHeader)
	}

	if len(rows) == 0 {
		return ""
	}

	var b strings.Builder

	// Render first row as header.
	firstRow := rows[0]
	b.WriteString("|")
	for _, cell := range firstRow {
		b.WriteString(" " + cell + " |")
	}
	b.WriteString("\n")

	// Separator row.
	b.WriteString("|")
	for range firstRow {
		b.WriteString("---|")
	}
	b.WriteString("\n")

	// Remaining rows.
	for _, row := range rows[1:] {
		b.WriteString("|")
		for _, cell := range row {
			b.WriteString(" " + cell + " |")
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	return b.String()
}

var _ CaptureRenderer = (*ConfluenceRenderer)(nil)
