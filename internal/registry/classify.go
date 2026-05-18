package registry

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Classification holds the derived agent classification from org.yaml vocabulary.
type Classification struct {
	Archetype    string
	ModelTier    string // "untiered" when model not found in vocabulary
	ProviderType string // "unclassed" when provider not found in vocabulary
}

// ClassifyAgent returns the classification for agentID.
// Returns (Classification{}, error) if the agent is not in the registry.
func (r *Registry) ClassifyAgent(agentID string) (Classification, error) {
	a, ok := r.LookupAgent(agentID)
	if !ok {
		return Classification{}, fmt.Errorf("agent %q not in registry", agentID)
	}

	c := Classification{Archetype: a.Archetype}
	vocab := r.Vocabulary()
	if vocab != nil {
		if tier, ok := a.ModelTier(*vocab); ok {
			c.ModelTier = tier
		} else {
			c.ModelTier = "untiered"
		}
		if class, ok := a.ProviderClass(*vocab); ok {
			c.ProviderType = class
		} else {
			c.ProviderType = "unclassed"
		}
	} else {
		c.ModelTier = "untiered"
		c.ProviderType = "unclassed"
	}
	return c, nil
}

// LoadWithOverrides loads org.yaml and merges per-agent field overrides from localOverridePath.
// If localOverridePath does not exist, Load succeeds with org.yaml values only (not an error).
func LoadWithOverrides(orgYAMLPath, localOverridePath string) (*Registry, error) {
	r, err := Load(orgYAMLPath)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(localOverridePath)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		// W303: local override unreadable — skip, base registry still valid
		return r, nil
	}

	var overrides struct {
		Agents []AgentRecord `yaml:"agents"`
	}
	if err := yaml.Unmarshal(data, &overrides); err != nil {
		// W303: malformed local override — skip
		return r, nil
	}

	for _, oa := range overrides.Agents {
		if existing, ok := r.agents[oa.ID]; ok {
			if oa.Archetype != "" {
				existing.Archetype = oa.Archetype
			}
			if oa.DefaultModel != "" {
				existing.DefaultModel = oa.DefaultModel
			}
			if oa.Provider != "" {
				existing.Provider = oa.Provider
			}
		} else {
			// New agent introduced via local override
			a := oa
			applyDefaults(&a)
			r.agents[oa.ID] = &a
		}
	}
	return r, nil
}
