package main

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/ProtonMail/gluon/rfc822"
	proton "github.com/ProtonMail/go-proton-api"
	"golang.org/x/net/html"
)

func buildFilename(meta proton.MessageMetadata) string {
	t := time.Unix(meta.Time, 0).UTC()
	dateStr := t.Format("2006-01-02")
	slug := slugify(meta.Subject)
	if len(slug) > 80 {
		slug = slug[:80]
	}
	return fmt.Sprintf("%s_%s_%s.md", dateStr, meta.ID[:8], slug)
}

func buildMarkdown(meta proton.MessageMetadata, body string) string {
	var sb strings.Builder

	t := time.Unix(meta.Time, 0).UTC()

	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("id: %q\n", meta.ID))
	sb.WriteString(fmt.Sprintf("subject: %q\n", meta.Subject))
	sb.WriteString(fmt.Sprintf("from: %q\n", formatAddress(meta.Sender)))
	sb.WriteString(fmt.Sprintf("to: [%s]\n", formatAddressList(meta.ToList)))
	if len(meta.CCList) > 0 {
		sb.WriteString(fmt.Sprintf("cc: [%s]\n", formatAddressList(meta.CCList)))
	}
	sb.WriteString(fmt.Sprintf("date: %q\n", t.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("labels: [%s]\n", strings.Join(quotedStrings(meta.LabelIDs), ", ")))
	if meta.NumAttachments > 0 {
		sb.WriteString(fmt.Sprintf("attachment_count: %d\n", meta.NumAttachments))
	}
	sb.WriteString("---\n\n")

	sb.WriteString(fmt.Sprintf("# %s\n\n", meta.Subject))
	sb.WriteString(body)
	sb.WriteString("\n")

	return sb.String()
}

func extractBody(decrypted []byte, mimeType rfc822.MIMEType) string {
	switch mimeType {
	case rfc822.TextPlain:
		return string(decrypted)

	case rfc822.TextHTML:
		return htmlToText(string(decrypted))

	default:
		// Try to parse as MIME multipart
		section := rfc822.Parse(decrypted)
		children, err := section.Children()
		if err != nil || len(children) == 0 {
			return string(decrypted)
		}

		var htmlBody, plainBody string
		_ = section.Walk(func(s *rfc822.Section) error {
			ct, _, _ := s.ContentType()
			switch rfc822.MIMEType(ct) {
			case rfc822.TextHTML:
				decoded, err := s.DecodedBody()
				if err == nil {
					htmlBody = string(decoded)
				}
			case rfc822.TextPlain:
				decoded, err := s.DecodedBody()
				if err == nil {
					plainBody = string(decoded)
				}
			}
			return nil
		})

		if htmlBody != "" {
			return htmlToText(htmlBody)
		}
		if plainBody != "" {
			return plainBody
		}
		return string(decrypted)
	}
}

func htmlToText(raw string) string {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return raw
	}

	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				sb.WriteString(text)
				sb.WriteString(" ")
			}
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "br":
				sb.WriteString("\n")
			case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "li", "tr":
				sb.WriteString("\n\n")
			case "a":
				for _, attr := range n.Attr {
					if attr.Key == "href" {
						for child := n.FirstChild; child != nil; child = child.NextSibling {
							walk(child)
						}
						sb.WriteString(fmt.Sprintf("(%s) ", attr.Val))
						return
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)

	return strings.TrimSpace(sb.String())
}

func formatAddress(addr *mail.Address) string {
	if addr == nil {
		return ""
	}
	if addr.Name != "" {
		return fmt.Sprintf("%s <%s>", addr.Name, addr.Address)
	}
	return addr.Address
}

func formatAddressList(addrs []*mail.Address) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = fmt.Sprintf("%q", formatAddress(a))
	}
	return strings.Join(parts, ", ")
}

func quotedStrings(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

func matchesContact(meta proton.MessageMetadata, email string) bool {
	if meta.Sender != nil && strings.ToLower(meta.Sender.Address) == email {
		return true
	}
	for _, addr := range meta.ToList {
		if addr != nil && strings.ToLower(addr.Address) == email {
			return true
		}
	}
	for _, addr := range meta.CCList {
		if addr != nil && strings.ToLower(addr.Address) == email {
			return true
		}
	}
	for _, addr := range meta.BCCList {
		if addr != nil && strings.ToLower(addr.Address) == email {
			return true
		}
	}
	return false
}

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}
