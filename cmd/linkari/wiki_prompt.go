package main

import (
	"fmt"
	"os"
)

func approxTokens(s string) int {
	return len(s) / 4
}

// trimToTokenBudget head-trims content to fit within maxTokens, keeping the
// newest entries (tail of the document).
func trimToTokenBudget(content string, maxTokens int) string {
	if approxTokens(content) <= maxTokens {
		return content
	}
	maxChars := maxTokens * 4
	if maxChars >= len(content) {
		return content
	}
	return content[len(content)-maxChars:]
}

// buildWikiContextBlock reads the index file at indexPath, trims it to maxTokens,
// and returns a formatted prompt block with a gap-rubric extension.
// Returns ("", nil) when the file is empty. Returns ("", err) on read failure.
func buildWikiContextBlock(indexPath string, maxTokens int) (string, error) {
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return "", fmt.Errorf("wiki_prompt: read %s: %w", indexPath, err)
	}
	content := trimToTokenBudget(string(data), maxTokens)
	if content == "" {
		return "", nil
	}
	block := "\n\n## Wiki Topic Context\n\n" + content +
		"\n\nNote any explicit knowledge gaps relative to the above topic context in your evaluation.\n"
	return block, nil
}

// buildScoringPrompt appends wikiBlock to sysPrompt. When wikiBlock is empty
// the return value is byte-for-byte identical to sysPrompt (CT-6 invariant).
func buildScoringPrompt(sysPrompt, wikiBlock string) string {
	if wikiBlock == "" {
		return sysPrompt
	}
	return sysPrompt + wikiBlock
}
