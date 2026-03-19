package hookval

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// SignalResult holds validation outcome for one signal.
type SignalResult struct {
	Name  string
	Value string
	Pass  bool
	Rule  string // human-readable rule, populated on failure
}

// RunHook invokes the fish hook script and returns its stdout.
// The hook is called with the given working directory as CWD.
func RunHook(hookPath, workDir string) ([]byte, error) {
	cmd := exec.Command("fish", hookPath)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("hook execution failed: %w", err)
	}
	return out, nil
}

// ParseContext extracts key=value pairs from the additionalContext field
// of the JSON object emitted by prompt-context.fish.
// The hook emits: {"hookSpecificOutput":{"hookEventName":"...","additionalContext":"k=v k=v ..."}}
func ParseContext(hookOutput []byte) (map[string]string, error) {
	var payload struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(hookOutput, &payload); err != nil {
		return nil, fmt.Errorf("parsing hook JSON: %w", err)
	}
	signals := make(map[string]string)
	for _, token := range strings.Fields(payload.HookSpecificOutput.AdditionalContext) {
		k, v, found := strings.Cut(token, "=")
		if !found {
			continue
		}
		signals[k] = v
	}
	return signals, nil
}

// ValidateSignals checks each signal value against its schema definition.
// Returns one result per defined signal.
func ValidateSignals(schema *Schema, signals map[string]string) []SignalResult {
	results := make([]SignalResult, 0, len(schema.Signals))

	for name, def := range schema.Signals {
		value, present := signals[name]
		if !present {
			results = append(results, SignalResult{
				Name:  name,
				Value: "<missing>",
				Pass:  false,
				Rule:  "signal not emitted by hook",
			})
			continue
		}

		pass, rule := checkSignal(def, value)
		results = append(results, SignalResult{
			Name:  name,
			Value: value,
			Pass:  pass,
			Rule:  rule,
		})
	}

	return results
}

// checkSignal returns (pass, failReason) for a single signal value.
func checkSignal(def SignalDef, value string) (bool, string) {
	switch def.Type {
	case "integer_or_literal":
		if matchesLiteral(def.Literals, value) {
			return true, ""
		}
		if matchesPattern(def.Pattern, value) {
			return true, ""
		}
		return false, fmt.Sprintf("must match pattern %q or be one of %v", def.Pattern, def.Literals)

	case "string_or_literal":
		if matchesLiteral(def.Literals, value) {
			return true, ""
		}
		if value != "" {
			return true, ""
		}
		return false, fmt.Sprintf("must be non-empty string or one of %v", def.Literals)

	case "string":
		if value != "" {
			return true, ""
		}
		return false, "must be a non-empty string"

	case "enum":
		for _, v := range def.Values {
			if value == v {
				return true, ""
			}
		}
		return false, fmt.Sprintf("must be one of %v", def.Values)

	case "iso8601_utc":
		if matchesPattern(def.Pattern, value) {
			return true, ""
		}
		return false, fmt.Sprintf("must match ISO8601 UTC pattern %q", def.Pattern)

	case "path":
		if strings.HasPrefix(value, "/") {
			return true, ""
		}
		return false, "must be an absolute path (starting with /)"

	default:
		return false, fmt.Sprintf("unknown signal type %q in schema", def.Type)
	}
}

func matchesLiteral(literals []string, value string) bool {
	for _, l := range literals {
		if value == l {
			return true
		}
	}
	return false
}

func matchesPattern(pattern, value string) bool {
	if pattern == "" {
		return false
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(value)
}
