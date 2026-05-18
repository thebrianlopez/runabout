package registry

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

const (
	DefaultSchemaVersion = "1.0"
	DefaultArchetype     = "agentic_coder"
	DefaultModel         = "claude-sonnet-4-6"
	DefaultProvider      = "anthropic"
)

// ValidationError is a schema or constraint violation encountered during load or validate.
type ValidationError struct {
	Code    string
	Message string
	Fatal   bool // true = entry skipped; false = warning only
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// OrgDocument is the top-level structure of org.yaml.
type OrgDocument struct {
	SchemaVersion string                      `yaml:"schema_version"`
	Org           string                      `yaml:"org"`
	Agents        []AgentRecord               `yaml:"agents"`
	Vocabulary    *Vocabulary                 `yaml:"vocabulary"`
	Schemas       map[string]SchemaIndexEntry `yaml:"schemas"`
}

// Vocabulary defines valid classification values for agents.
type Vocabulary struct {
	Archetypes    []string            `yaml:"archetypes"`
	ModelTiers    map[string][]string `yaml:"model_tiers"`
	ProviderTypes map[string][]string `yaml:"provider_types"`
}

// SchemaIndexEntry is one entry in the schemas index block.
type SchemaIndexEntry struct {
	Version   *string `yaml:"version"`
	Spec      *string `yaml:"spec"`
	Instances string  `yaml:"instances"`
}

// Registry holds the parsed and validated agent registry.
type Registry struct {
	doc    OrgDocument
	agents map[string]*AgentRecord
	errs   []ValidationError
}

// kebabCaseRE matches valid kebab-case agent IDs.
var kebabCaseRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Load reads org.yaml from path and returns a Registry.
func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading org.yaml: %w", err)
	}
	return LoadBytes(data)
}

// LoadBytes parses org.yaml from raw YAML bytes.
func LoadBytes(data []byte) (*Registry, error) {
	var doc OrgDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, ValidationError{Code: "E001", Message: fmt.Sprintf("org.yaml parse failed: %s", err), Fatal: true}
	}
	return buildRegistry(doc), nil
}

func buildRegistry(doc OrgDocument) *Registry {
	r := &Registry{
		doc:    doc,
		agents: make(map[string]*AgentRecord),
	}
	if r.doc.SchemaVersion == "" {
		r.errs = append(r.errs, ValidationError{Code: "W001", Message: "org.yaml missing schema_version — treating as v1"})
		r.doc.SchemaVersion = DefaultSchemaVersion
	}
	for i := range r.doc.Agents {
		a := &r.doc.Agents[i]
		applyDefaults(a)
		errs := validateAgent(a, i, r.doc.Vocabulary)
		r.errs = append(r.errs, errs...)
		fatal := false
		for _, e := range errs {
			if e.Fatal {
				fatal = true
				break
			}
		}
		if !fatal {
			r.agents[a.ID] = a
		}
	}
	return r
}

// Validate runs cross-agent constraints: duplicate IDs, duplicate CWDs, vocabulary completeness.
func (r *Registry) Validate() []ValidationError {
	var errs []ValidationError
	seenIDs := make(map[string]bool)
	seenCWDs := make(map[string]string)

	for i := range r.doc.Agents {
		a := &r.doc.Agents[i]
		if a.ID == "" {
			continue
		}
		if seenIDs[a.ID] {
			errs = append(errs, ValidationError{Code: "E002", Message: fmt.Sprintf("Duplicate agent ID '%s' — second entry ignored", a.ID)})
			continue
		}
		seenIDs[a.ID] = true
		if a.CWD != nil && *a.CWD != "" {
			if other, ok := seenCWDs[*a.CWD]; ok {
				errs = append(errs, ValidationError{Code: "E003", Message: fmt.Sprintf("Duplicate CWD '%s' on agents '%s', '%s'", *a.CWD, other, a.ID)})
			} else {
				seenCWDs[*a.CWD] = a.ID
			}
		}
	}

	if r.doc.Vocabulary == nil {
		errs = append(errs, ValidationError{Code: "E004", Message: "org.yaml missing vocabulary — cannot validate classifications"})
		return errs
	}
	if len(r.doc.Vocabulary.Archetypes) == 0 {
		errs = append(errs, ValidationError{Code: "E004", Message: "vocabulary.archetypes is empty"})
	}
	return errs
}

// LookupAgent returns the agent record for id, or (nil, false) if not found.
func (r *Registry) LookupAgent(id string) (*AgentRecord, bool) {
	a, ok := r.agents[id]
	return a, ok
}

// Agents returns all loaded agent records.
func (r *Registry) Agents() []*AgentRecord {
	out := make([]*AgentRecord, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, a)
	}
	return out
}

// AgentCount returns the number of successfully loaded agents.
func (r *Registry) AgentCount() int { return len(r.agents) }

// SchemaVersion returns the schema_version field value.
func (r *Registry) SchemaVersion() string { return r.doc.SchemaVersion }

// Vocabulary returns the vocabulary block (may be nil).
func (r *Registry) Vocabulary() *Vocabulary { return r.doc.Vocabulary }

// Errors returns warnings and errors collected during loading.
func (r *Registry) Errors() []ValidationError { return r.errs }
