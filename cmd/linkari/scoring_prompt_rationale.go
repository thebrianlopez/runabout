package main

import "strings"

// isRationaleQuestion returns true when text heuristically reads as a question.
// Used for telemetry only - not for routing decisions.
func isRationaleQuestion(text string) bool {
	t := strings.TrimSpace(strings.ToLower(text))
	if t == "" {
		return false
	}
	if strings.HasSuffix(t, "?") {
		return true
	}
	for _, prefix := range []string{
		"is ", "are ", "was ", "were ",
		"what ", "how ", "why ", "when ", "where ", "who ",
		"does ", "do ", "did ", "can ", "could ", "should ", "would ",
	} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

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
