package registry_test

// EPIC-087 (F1) contract tests: CT-1 through CT-8
// Covers org.yaml v2.0 schema constraints.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thebrianlopez/runabout/internal/registry"
)

const minimalV2YAML = `
schema_version: "2.0"
org: test
agents:
  - id: test-agent
    cwd: /some/path
    archetype: agentic_coder
    default_model: claude-sonnet-4-6
    provider: anthropic
vocabulary:
  archetypes: [agentic_coder, orchestrator, shell_assistant, tool_runner]
  model_tiers:
    balanced: [claude-sonnet-4-6, gpt-4o-mini]
    frontier: [claude-opus-4-7]
  provider_types:
    cloud_anthropic: [anthropic]
    cloud_openai: [openai]
`

// CT-1: schema_version present → parsed as-is
func TestRegistry_CT1_SchemaVersionPresent(t *testing.T) {
	r, err := registry.LoadBytes([]byte(minimalV2YAML))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if r.SchemaVersion() != "2.0" {
		t.Errorf("expected schema_version=2.0, got %q", r.SchemaVersion())
	}
}

// CT-2: no schema_version → defaults to "1.0" and agents still load; W001 emitted
func TestRegistry_CT2_V1BackwardCompat(t *testing.T) {
	yaml := `
org: test
agents:
  - id: legacy-agent
    cwd: /legacy/path
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
	if r.SchemaVersion() != "1.0" {
		t.Errorf("expected default version=1.0, got %q", r.SchemaVersion())
	}
	if _, ok := r.LookupAgent("legacy-agent"); !ok {
		t.Fatal("expected legacy-agent to load under v1 compat")
	}
	hasW001 := false
	for _, e := range r.Errors() {
		if e.Code == "W001" {
			hasW001 = true
		}
	}
	if !hasW001 {
		t.Error("expected W001 warning for missing schema_version")
	}
}

// CT-3: two agents sharing the same id → E002 from Validate()
func TestRegistry_CT3_DuplicateIDDetection(t *testing.T) {
	yaml := `
schema_version: "2.0"
org: test
agents:
  - id: foo
    cwd: /path/a
  - id: foo
    cwd: /path/b
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
	hasE002 := false
	for _, e := range r.Validate() {
		if e.Code == "E002" {
			hasE002 = true
		}
	}
	if !hasE002 {
		t.Error("expected E002 for duplicate agent ID")
	}
}

// CT-4: two agents sharing the same non-null cwd → E003 from Validate()
func TestRegistry_CT4_DuplicateCWDDetection(t *testing.T) {
	yaml := `
schema_version: "2.0"
org: test
agents:
  - id: agent-a
    cwd: /same/path
  - id: agent-b
    cwd: /same/path
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
	hasE003 := false
	for _, e := range r.Validate() {
		if e.Code == "E003" {
			hasE003 = true
		}
	}
	if !hasE003 {
		t.Error("expected E003 for duplicate CWD")
	}
}

// CT-5: agent archetype not in vocabulary.archetypes → W010 emitted
func TestRegistry_CT5_VocabularyMembership(t *testing.T) {
	yaml := `
schema_version: "2.0"
org: test
agents:
  - id: test-agent
    archetype: unknown_archetype
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
	hasW010 := false
	for _, e := range r.Errors() {
		if e.Code == "W010" {
			hasW010 = true
		}
	}
	if !hasW010 {
		t.Error("expected W010 for archetype not in vocabulary")
	}
}

// CT-6: valid org.yaml → yq can query .agents[].id (integration, requires yq)
func TestRegistry_CT6_YQQueryability(t *testing.T) {
	if _, err := exec.LookPath("yq"); err != nil {
		t.Skip("yq not installed")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "org.yaml")
	if err := os.WriteFile(path, []byte(minimalV2YAML), 0644); err != nil {
		t.Fatalf("writing org.yaml: %v", err)
	}
	out, err := exec.Command("yq", ".agents[].id", path).Output()
	if err != nil {
		t.Fatalf("yq failed: %v", err)
	}
	if len(out) == 0 {
		t.Error("expected yq to return agent IDs, got empty output")
	}
}

// CT-7: vocabulary.archetypes is empty → E004 from Validate()
func TestRegistry_CT7_VocabularyCompleteness(t *testing.T) {
	yaml := `
schema_version: "2.0"
org: test
agents:
  - id: test-agent
vocabulary:
  archetypes: []
  model_tiers:
    balanced: [claude-sonnet-4-6]
  provider_types:
    cloud_anthropic: [anthropic]
`
	r, err := registry.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	hasE004 := false
	for _, e := range r.Validate() {
		if e.Code == "E004" {
			hasE004 = true
		}
	}
	if !hasE004 {
		t.Error("expected E004 for empty archetypes list")
	}
}

// CT-8: agent with cwd: null → loads with CWD=nil (workspace-scoped)
func TestRegistry_CT8_AgentCWDNullValid(t *testing.T) {
	yaml := `
schema_version: "2.0"
org: test
agents:
  - id: workspace-agent
    cwd: null
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
	a, ok := r.LookupAgent("workspace-agent")
	if !ok {
		t.Fatal("expected workspace-agent to be loaded")
	}
	if a.CWD != nil {
		t.Errorf("expected CWD=nil for workspace-scoped agent, got %v", *a.CWD)
	}
}

// BT-1: production org.yaml loads all 11 agents (integration)
func TestRegistry_BT1_ProductionOrgYAML(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}
	path := filepath.Join(home, "code/personal/docs/org.yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("production org.yaml not found at %s", path)
	}
	r, err := registry.Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if r.AgentCount() < 11 {
		t.Errorf("expected at least 11 agents, got %d", r.AgentCount())
	}
}

// BT-3: vocabulary model tier lookup — claude-sonnet-4-6 → "balanced"
func TestRegistry_BT3_VocabularyModelTierLookup(t *testing.T) {
	r, err := registry.LoadBytes([]byte(minimalV2YAML))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	a, ok := r.LookupAgent("test-agent")
	if !ok {
		t.Fatal("expected test-agent")
	}
	vocab := r.Vocabulary()
	if vocab == nil {
		t.Fatal("expected vocabulary block")
	}
	tier, ok := a.ModelTier(*vocab)
	if !ok || tier != "balanced" {
		t.Errorf("expected tier=balanced, got %q (ok=%v)", tier, ok)
	}
}

// BT-4: vocabulary provider lookup — anthropic → "cloud_anthropic"
func TestRegistry_BT4_VocabularyProviderLookup(t *testing.T) {
	r, err := registry.LoadBytes([]byte(minimalV2YAML))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	a, ok := r.LookupAgent("test-agent")
	if !ok {
		t.Fatal("expected test-agent")
	}
	vocab := r.Vocabulary()
	if vocab == nil {
		t.Fatal("expected vocabulary block")
	}
	class, ok := a.ProviderClass(*vocab)
	if !ok || class != "cloud_anthropic" {
		t.Errorf("expected class=cloud_anthropic, got %q (ok=%v)", class, ok)
	}
}
