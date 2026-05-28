package hookval

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Schema is the top-level structure of hook-signal-schema.yaml.
type Schema struct {
	Version string               `yaml:"version"`
	Signals map[string]SignalDef `yaml:"signals"`
}

// SignalDef describes a single hook context signal.
type SignalDef struct {
	Type        string   `yaml:"type"`
	Pattern     string   `yaml:"pattern,omitempty"`
	Literals    []string `yaml:"literals,omitempty"`
	Values      []string `yaml:"values,omitempty"`
	Description string   `yaml:"description"`
}

// LoadSchema reads and parses a schema YAML file.
func LoadSchema(path string) (*Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading schema: %w", err)
	}
	var s Schema
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing schema: %w", err)
	}
	return &s, nil
}

// LintSchema checks that the schema is well-formed.
// Returns a list of error strings; empty means valid.
func LintSchema(s *Schema) []string {
	var errs []string
	if s.Version == "" {
		errs = append(errs, "missing top-level 'version' field")
	}
	if len(s.Signals) == 0 {
		errs = append(errs, "no signals defined")
	}
	for name, def := range s.Signals {
		if def.Type == "" {
			errs = append(errs, fmt.Sprintf("signal %q: missing 'type'", name))
		}
		if def.Description == "" {
			errs = append(errs, fmt.Sprintf("signal %q: missing 'description'", name))
		}
		switch def.Type {
		case "enum":
			if len(def.Values) == 0 {
				errs = append(errs, fmt.Sprintf("signal %q: type 'enum' requires 'values'", name))
			}
		case "integer_or_literal", "iso8601_utc":
			if def.Pattern == "" {
				errs = append(errs, fmt.Sprintf("signal %q: type %q requires 'pattern'", name, def.Type))
			}
		case "string", "string_or_literal", "path":
			// no extra fields required
		default:
			errs = append(errs, fmt.Sprintf("signal %q: unknown type %q", name, def.Type))
		}
	}
	return errs
}
