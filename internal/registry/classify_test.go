package registry_test

// EPIC-090 (F5) contract tests: CT-1 through CT-8
// Covers castex consumer migration: ClassifyAgent and LoadWithOverrides.
// Run with: go test ./internal/registry/... -run F5

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thebrianlopez/runabout/internal/registry"
)

const classifyOrgYAML = `
schema_version: "2.0"
org: test
agents:
  - id: coder-agent
    archetype: agentic_coder
    default_model: claude-sonnet-4-6
    provider: anthropic
  - id: orchestrate-agent
    archetype: orchestrator
    default_model: claude-opus-4-7
    provider: anthropic
vocabulary:
  archetypes: [agentic_coder, orchestrator, shell_assistant, tool_runner]
  model_tiers:
    balanced: [claude-sonnet-4-6]
    frontier: [claude-opus-4-7]
  provider_types:
    cloud_anthropic: [anthropic]
`

// CT-1: behavioral equivalence — classification from org.yaml produces expected results.
// Note: taxonomy.yaml does not exist; equivalence verified against known fixture values
// that match what taxonomy.yaml would have contained for these agents.
func TestF5_CT1_BehavioralEquivalence(t *testing.T) {
	r, err := registry.LoadBytes([]byte(classifyOrgYAML))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// Expected classification matching taxonomy.yaml values for these agents
	type expected struct {
		archetype    string
		modelTier    string
		providerType string
	}
	cases := map[string]expected{
		"coder-agent":       {"agentic_coder", "balanced", "cloud_anthropic"},
		"orchestrate-agent": {"orchestrator", "frontier", "cloud_anthropic"},
	}

	for agentID, want := range cases {
		c, err := r.ClassifyAgent(agentID)
		if err != nil {
			t.Errorf("ClassifyAgent(%q): %v", agentID, err)
			continue
		}
		if c.Archetype != want.archetype {
			t.Errorf("%s archetype: want %q, got %q", agentID, want.archetype, c.Archetype)
		}
		if c.ModelTier != want.modelTier {
			t.Errorf("%s model_tier: want %q, got %q", agentID, want.modelTier, c.ModelTier)
		}
		if c.ProviderType != want.providerType {
			t.Errorf("%s provider_type: want %q, got %q", agentID, want.providerType, c.ProviderType)
		}
	}
}

// CT-2: archetype classification — agentic_coder passes through directly
func TestF5_CT2_ArchetypeClassification(t *testing.T) {
	r, err := registry.LoadBytes([]byte(classifyOrgYAML))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	c, err := r.ClassifyAgent("coder-agent")
	if err != nil {
		t.Fatalf("ClassifyAgent: %v", err)
	}
	if c.Archetype != "agentic_coder" {
		t.Errorf("Archetype: want agentic_coder, got %q", c.Archetype)
	}
}

// CT-3: model tier derivation — claude-sonnet-4-6 → "balanced"
func TestF5_CT3_ModelTierDerivation(t *testing.T) {
	r, err := registry.LoadBytes([]byte(classifyOrgYAML))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	c, err := r.ClassifyAgent("coder-agent")
	if err != nil {
		t.Fatalf("ClassifyAgent: %v", err)
	}
	if c.ModelTier != "balanced" {
		t.Errorf("ModelTier: want balanced, got %q", c.ModelTier)
	}
}

// CT-4: provider type derivation — anthropic → "cloud_anthropic"
func TestF5_CT4_ProviderTypeDerivation(t *testing.T) {
	r, err := registry.LoadBytes([]byte(classifyOrgYAML))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	c, err := r.ClassifyAgent("coder-agent")
	if err != nil {
		t.Fatalf("ClassifyAgent: %v", err)
	}
	if c.ProviderType != "cloud_anthropic" {
		t.Errorf("ProviderType: want cloud_anthropic, got %q", c.ProviderType)
	}
}

// CT-5: local override merge — local archetype overrides org.yaml value
func TestF5_CT5_LocalOverrideMerge(t *testing.T) {
	dir := t.TempDir()

	orgPath := filepath.Join(dir, "org.yaml")
	os.WriteFile(orgPath, []byte(classifyOrgYAML), 0o644)

	localPath := filepath.Join(dir, "taxonomy.local.yaml")
	os.WriteFile(localPath, []byte(`
agents:
  - id: coder-agent
    archetype: tool_runner
`), 0o644)

	r, err := registry.LoadWithOverrides(orgPath, localPath)
	if err != nil {
		t.Fatalf("LoadWithOverrides: %v", err)
	}
	c, err := r.ClassifyAgent("coder-agent")
	if err != nil {
		t.Fatalf("ClassifyAgent: %v", err)
	}
	if c.Archetype != "tool_runner" {
		t.Errorf("Archetype: want tool_runner (from local override), got %q", c.Archetype)
	}
}

// CT-6: agent_tool not in registry → error from ClassifyAgent (caller groups as "unknown")
func TestF5_CT6_UnknownAgentHandling(t *testing.T) {
	r, err := registry.LoadBytes([]byte(classifyOrgYAML))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	_, err = r.ClassifyAgent("nonexistent-agent")
	if err == nil {
		t.Error("expected error for agent not in registry")
	}
}

// CT-7: missing taxonomy.local.yaml → LoadWithOverrides succeeds with org.yaml values only
func TestF5_CT7_MissingLocalFileOK(t *testing.T) {
	dir := t.TempDir()
	orgPath := filepath.Join(dir, "org.yaml")
	os.WriteFile(orgPath, []byte(classifyOrgYAML), 0o644)

	r, err := registry.LoadWithOverrides(orgPath, filepath.Join(dir, "nonexistent.local.yaml"))
	if err != nil {
		t.Fatalf("expected no error for missing local file, got: %v", err)
	}
	c, err := r.ClassifyAgent("coder-agent")
	if err != nil {
		t.Fatalf("ClassifyAgent: %v", err)
	}
	if c.Archetype != "agentic_coder" {
		t.Errorf("Archetype: want agentic_coder, got %q", c.Archetype)
	}
}

// CT-8: agent model not in any tier → ModelTier returns "untiered"
func TestF5_CT8_VocabularyIncomplete(t *testing.T) {
	yaml := `
schema_version: "2.0"
org: test
agents:
  - id: oddmodel-agent
    default_model: gpt-99
vocabulary:
  archetypes: [agentic_coder]
  model_tiers:
    balanced: [claude-sonnet-4-6]
  provider_types:
    cloud_anthropic: [anthropic]
`
	r, err := registry.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	// After validation, default_model is reset to DefaultModel (W011 fired)
	// So the agent will have the default model, which IS in the tier.
	// Test with a custom fixture where we bypass validation by using a valid model but missing vocabulary.
	yaml2 := `
schema_version: "2.0"
org: test
agents:
  - id: no-tier-agent
    default_model: claude-sonnet-4-6
vocabulary:
  archetypes: [agentic_coder]
  model_tiers:
    frontier: [claude-opus-4-7]
  provider_types:
    cloud_anthropic: [anthropic]
`
	r2, err := registry.LoadBytes([]byte(yaml2))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	// After W011, default model is applied (claude-sonnet-4-6)
	// But frontier tier only has claude-opus-4-7, so claude-sonnet-4-6 is not in any tier.
	// But wait — W011 fires and resets to DefaultModel. DefaultModel is claude-sonnet-4-6.
	// frontier: [claude-opus-4-7] — neither claude-sonnet-4-6 (which has no tier)
	c, err := r2.ClassifyAgent("no-tier-agent")
	if err != nil {
		t.Fatalf("ClassifyAgent: %v", err)
	}
	if c.ModelTier != "untiered" {
		t.Errorf("ModelTier: want untiered for model not in any vocabulary tier, got %q", c.ModelTier)
	}
	_ = r
}
