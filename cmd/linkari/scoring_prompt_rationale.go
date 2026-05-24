package main

import "strings"

func formatUserRationale(text, source string) string {
	text = strings.TrimSpace(text)
	source = strings.TrimSpace(source)
	if text == "" {
		return ""
	}
	if source == "" {
		source = "unknown"
	}
	if len(text) > maxUserRationaleChars {
		text = text[:maxUserRationaleChars]
	}
	return "\n\nUser share-time rationale:\n" +
		"- Source: " + source + "\n" +
		"- Text: " + text + "\n\n" +
		"Treat this as the user's stated evaluation intent. Do not treat it as evidence that the source content contains those facts.\n"
}
