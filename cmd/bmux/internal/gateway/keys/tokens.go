// Package keys implements the KeyTranslator for the send-keys (F5) pathway.
// It validates and translates mobile key sequences into tmux send-keys operations.
package keys

// tokenMap maps lowercase token names (without braces) to their tmux key strings.
// All 18 entries are case-insensitively matched via strings.ToLower on input.
var tokenMap = map[string]string{
	"enter":     "Enter",
	"escape":    "Escape",
	"tab":       "Tab",
	"ctrl-c":    "C-c",
	"ctrl-d":    "C-d",
	"ctrl-z":    "C-z",
	"ctrl-l":    "C-l",
	"up":        "Up",
	"down":      "Down",
	"left":      "Left",
	"right":     "Right",
	"pageup":    "PPage",
	"pagedown":  "NPage",
	"home":      "Home",
	"end":       "End",
	"delete":    "DC",
	"backspace": "BSpace",
	"space":     "Space",
}
