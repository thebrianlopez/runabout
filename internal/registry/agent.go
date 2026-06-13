package registry

import "fmt"

// AgentRecord is a single agent entry from org.yaml agents[].
type AgentRecord struct {
	ID           string   `yaml:"id"`
	CWD          *string  `yaml:"cwd"`
	Archetype    string   `yaml:"archetype"`
	DefaultModel string   `yaml:"default_model"`
	Provider     string   `yaml:"provider"`
	Languages    []string `yaml:"languages"`
	// Secret is the per-agent HMAC signing key for consensus vote verification.
	// Never log or include in dispatch files.
	Secret *string `yaml:"secret,omitempty"`
}

// IsWorkspaceScoped returns true when CWD is nil (agent has no static directory).
func (a AgentRecord) IsWorkspaceScoped() bool { return a.CWD == nil }

// ModelTier returns the vocabulary tier name containing this agent's DefaultModel.
func (a AgentRecord) ModelTier(vocab Vocabulary) (string, bool) {
	for tier, models := range vocab.ModelTiers {
		for _, m := range models {
			if m == a.DefaultModel {
				return tier, true
			}
		}
	}
	return "", false
}

// ProviderClass returns the vocabulary class name containing this agent's Provider.
func (a AgentRecord) ProviderClass(vocab Vocabulary) (string, bool) {
	for class, providers := range vocab.ProviderTypes {
		for _, p := range providers {
			if p == a.Provider {
				return class, true
			}
		}
	}
	return "", false
}

// applyDefaults sets default values for any optional AgentRecord fields that are absent.
func applyDefaults(a *AgentRecord) {
	if a.Archetype == "" {
		a.Archetype = DefaultArchetype
	}
	if a.DefaultModel == "" {
		a.DefaultModel = DefaultModel
	}
	if a.Provider == "" {
		a.Provider = DefaultProvider
	}
	if a.Languages == nil {
		a.Languages = []string{}
	}
	// CWD: nil means workspace-scoped  -  zero value is already nil for *string
}

// validateAgent checks per-agent field constraints and vocabulary membership.
// Returns E010 (Fatal=true) if id is missing; warnings otherwise.
func validateAgent(a *AgentRecord, index int, vocab *Vocabulary) []ValidationError {
	var errs []ValidationError

	if a.ID == "" {
		return append(errs, ValidationError{
			Code:    "E010",
			Message: fmt.Sprintf("Agent entry at index %d has no id  -  skipped", index),
			Fatal:   true,
		})
	}

	if !kebabCaseRE.MatchString(a.ID) {
		errs = append(errs, ValidationError{
			Code:    "E011",
			Message: fmt.Sprintf("Agent ID '%s' is not valid kebab-case  -  entry loaded with warning", a.ID),
		})
	}

	if vocab == nil {
		errs = append(errs, ValidationError{
			Code:    "W003",
			Message: "vocabulary block absent  -  classification validation skipped for all agents",
		})
		return errs
	}

	if !stringInSlice(a.Archetype, vocab.Archetypes) {
		errs = append(errs, ValidationError{
			Code:    "W010",
			Message: fmt.Sprintf("Agent '%s' archetype '%s' not in vocabulary  -  using default '%s'", a.ID, a.Archetype, DefaultArchetype),
		})
		a.Archetype = DefaultArchetype
	}

	if !modelInTiers(a.DefaultModel, vocab.ModelTiers) {
		errs = append(errs, ValidationError{
			Code:    "W011",
			Message: fmt.Sprintf("Agent '%s' default_model '%s' not in vocabulary  -  using default '%s'", a.ID, a.DefaultModel, DefaultModel),
		})
		a.DefaultModel = DefaultModel
	}

	if !providerInTypes(a.Provider, vocab.ProviderTypes) {
		errs = append(errs, ValidationError{
			Code:    "W012",
			Message: fmt.Sprintf("Agent '%s' provider '%s' not in vocabulary  -  using default '%s'", a.ID, a.Provider, DefaultProvider),
		})
		a.Provider = DefaultProvider
	}

	return errs
}

func stringInSlice(s string, list []string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func modelInTiers(model string, tiers map[string][]string) bool {
	for _, models := range tiers {
		if stringInSlice(model, models) {
			return true
		}
	}
	return false
}

func providerInTypes(provider string, types map[string][]string) bool {
	for _, providers := range types {
		if stringInSlice(provider, providers) {
			return true
		}
	}
	return false
}
