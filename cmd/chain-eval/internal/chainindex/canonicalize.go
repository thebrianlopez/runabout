package chainindex

// ExtractStatus reads all status surfaces from a markdown artifact's content
// and returns the canonical status with drift metadata.
//
// Priority order:
//  1. YAML frontmatter: status: <value>  (always canonical when present)
//  2. Markdown table row: | **Status** | <value> |  (canonical when #1 absent)
//  3. Legacy plain text: Status: <value>  (drift-checked against #2 when both present)
//
// Case-insensitive comparison for drift detection.
func ExtractStatus(content string) StatusExtractionResult {
	panic("not implemented")
}
