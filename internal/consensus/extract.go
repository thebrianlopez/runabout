package consensus

import (
	"encoding/json"
	"fmt"
)

// ExtractJSON finds the first JSON object or array in text.
func ExtractJSON(text string) (json.RawMessage, error) {
	return extractJSON(text)
}

func extractJSON(text string) (json.RawMessage, error) {
	for i, c := range text {
		if c == '{' || c == '[' {
			candidate := text[i:]
			b := []byte(candidate)
			if json.Valid(b) {
				return json.RawMessage(b), nil
			}
			for j := len(b); j > 0; j-- {
				if b[j-1] == '}' || b[j-1] == ']' {
					if json.Valid(b[:j]) {
						return json.RawMessage(b[:j]), nil
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("no JSON found in response")
}
