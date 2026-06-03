package chainindex

import (
	"regexp"
	"strings"
)

var (
	// frontmatterStatusRe matches `status: value` in YAML frontmatter.
	frontmatterStatusRe = regexp.MustCompile(`(?m)^status:\s+(.+?)\s*$`)
	// bodyTableStatusRe matches `| **Status** | value |` in markdown tables.
	bodyTableStatusRe = regexp.MustCompile(`(?m)^\|\s*\*\*Status\*\*\s*\|\s*` + "`?" + `([^|` + "`" + `]+?)` + "`?" + `\s*\|`)
	// legacyStatusRe matches `Status: value` as plain text (no table/frontmatter).
	legacyStatusRe = regexp.MustCompile(`(?m)^Status:\s+(.+?)\s*$`)
)

// ExtractStatus reads all status surfaces from a markdown artifact's content
// and returns the canonical status with drift metadata.
//
// Priority order:
//  1. YAML frontmatter: status: <value>  (always canonical when present)
//  2. Markdown table row: | **Status** | <value> |  (canonical when #1 absent)
//  3. Legacy plain text: Status: <value>  (drift-checked against #2 when both present)
//
// Comparison is case-insensitive for drift detection.
func ExtractStatus(content string) StatusExtractionResult {
	fm, body := splitFrontmatter(content)

	frontmatterStatus := extractFrontmatterStatus(fm)
	bodyTableStatus := extractBodyTableStatus(body)
	legacyStatus := extractLegacyStatus(body)

	result := StatusExtractionResult{}
	result.Surfaces.Frontmatter = frontmatterStatus
	result.Surfaces.Body = bodyTableStatus

	switch {
	case frontmatterStatus != "":
		// Surface 1 is canonical. Check drift against body.
		result.Canonical = frontmatterStatus
		if bodyTableStatus != "" && !eqCI(frontmatterStatus, bodyTableStatus) {
			result.SurfaceDrift = true
			result.Surfaces.Divergent = []string{bodyTableStatus}
		}

	case bodyTableStatus != "":
		// Surface 2 is canonical. Check drift against legacy.
		result.Canonical = bodyTableStatus
		if legacyStatus != "" && !eqCI(bodyTableStatus, legacyStatus) {
			result.SurfaceDrift = true
			result.Surfaces.Body = bodyTableStatus
			result.Surfaces.Divergent = []string{legacyStatus}
		}

	case legacyStatus != "":
		result.Canonical = legacyStatus

	default:
		result.Canonical = "Unknown"
	}

	return result
}

// splitFrontmatter returns (frontmatter, body). Frontmatter is the content
// between the first and second `---` delimiters. If no frontmatter, body is
// the entire content.
func splitFrontmatter(content string) (frontmatter, body string) {
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return "", content
	}
	// Find the closing --- after the first line.
	rest := content[strings.Index(content, "---")+3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", content
	}
	return rest[:idx], rest[idx+4:]
}

func extractFrontmatterStatus(fm string) string {
	if m := frontmatterStatusRe.FindStringSubmatch(fm); len(m) > 1 {
		return strings.Trim(strings.TrimSpace(m[1]), "`\"'")
	}
	return ""
}

func extractBodyTableStatus(body string) string {
	if m := bodyTableStatusRe.FindStringSubmatch(body); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func extractLegacyStatus(body string) string {
	if m := legacyStatusRe.FindStringSubmatch(body); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func eqCI(a, b string) bool {
	return strings.EqualFold(a, b)
}
