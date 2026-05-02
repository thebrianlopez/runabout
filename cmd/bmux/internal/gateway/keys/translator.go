package keys

import (
	"fmt"
	"strings"
)

// KeyTranslator validates and translates send-keys input.
type KeyTranslator interface {
	// Translate converts raw key input into a slice of tmux send-keys operations.
	// If literal is true, the entire input is returned as a single Literal:true op
	// with no token parsing or E03 errors.
	Translate(input string, literal bool) ([]SendKeysOp, error)
}

// SendKeysOp is a single tmux send-keys invocation.
type SendKeysOp struct {
	Keys    string
	Literal bool
}

// TranslateError is returned when input fails validation or contains an unknown token.
// The Code field carries a stable machine-readable error identifier.
//
// Security: the Keys field of SendKeysOp is NEVER included in error messages
// because input may contain passwords or sensitive text.
type TranslateError struct {
	Code    string
	Message string
}

func (e *TranslateError) Error() string {
	return fmt.Sprintf("keys: %s: %s", e.Code, e.Message)
}

// NewKeyTranslator returns the default KeyTranslator implementation.
func NewKeyTranslator() KeyTranslator {
	return &keyTranslator{}
}

type keyTranslator struct{}

// Translate validates input and converts it to []SendKeysOp.
//
// Validation order:
//  1. len > 4096 → E01 input_invalid_length
//  2. NUL byte   → E02 input_invalid_nul
//
// If literal=true: return single {Keys:input, Literal:true} op (no token parsing).
// If literal=false: scan for {Token} patterns; unknown tokens → E03 input_invalid_token.
func (kt *keyTranslator) Translate(input string, literal bool) ([]SendKeysOp, error) {
	// E01: length check
	if len(input) > 4096 {
		return nil, &TranslateError{
			Code:    "input_invalid_length",
			Message: "input exceeds 4096 bytes",
		}
	}

	// E02: NUL byte check
	if strings.ContainsRune(input, '\x00') {
		return nil, &TranslateError{
			Code:    "input_invalid_nul",
			Message: "input contains NUL byte",
		}
	}

	// literal=true: bypass all token parsing
	if literal {
		return []SendKeysOp{{Keys: input, Literal: true}}, nil
	}

	// Empty input: single empty literal op
	if input == "" {
		return []SendKeysOp{{Keys: "", Literal: true}}, nil
	}

	return scanTokens(input)
}

// scanTokens splits input into text and {Token} segments left-to-right.
// Text segments become Literal:true ops; recognized tokens become Literal:false ops.
// Unknown {Token} patterns return E03.
func scanTokens(input string) ([]SendKeysOp, error) {
	var ops []SendKeysOp
	remaining := input

	for len(remaining) > 0 {
		open := strings.IndexByte(remaining, '{')
		if open < 0 {
			// No more tokens — rest is plain text
			ops = append(ops, SendKeysOp{Keys: remaining, Literal: true})
			break
		}

		// Text before the '{'
		if open > 0 {
			ops = append(ops, SendKeysOp{Keys: remaining[:open], Literal: true})
		}

		// Find closing '}'
		close := strings.IndexByte(remaining[open:], '}')
		if close < 0 {
			// No closing brace — treat rest as plain text
			ops = append(ops, SendKeysOp{Keys: remaining[open:], Literal: true})
			break
		}
		close += open // adjust to absolute index in remaining

		tokenName := remaining[open+1 : close]
		tmuxKey, ok := tokenMap[strings.ToLower(tokenName)]
		if !ok {
			return nil, &TranslateError{
				Code:    "input_invalid_token",
				Message: fmt.Sprintf("unknown token {%s}", tokenName),
			}
		}

		ops = append(ops, SendKeysOp{Keys: tmuxKey, Literal: false})
		remaining = remaining[close+1:]
	}

	return ops, nil
}
