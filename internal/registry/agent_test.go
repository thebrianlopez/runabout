package registry_test

// EPIC-091 (F2) contract tests: CT-1 through CT-10
// Covers AgentRecord schema: id validation, defaults, cwd semantics, vocabulary membership.
// Run with: go test ./internal/registry/... -run F2

import (
	"testing"

	"github.com/thebrianlopez/runabout/internal/registry"
)

const agentVocab = `
schema_version: "2.0"
org: test
vocabulary:
  archetypes: [agentic_coder, orchestrator, shell_assistant, tool_runner]
  model_tiers:
    balanced: [claude-sonnet-4-6]
    frontier: [claude-opus-4-7]
  provider_types:
    cloud_anthropic: [anthropic]
    cloud_openai: [openai]
`

// CT-1: agent entry with no id → E010 (Fatal), entry skipped
func TestF2_CT1_IDRequired(t *testing.T) {
	yaml := agentVocab + `
agents:
  - archetype: agentic_coder
`
	r, err := registry.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if r.AgentCount() != 0 {
		t.Errorf("expected 0 agents (entry skipped), got %d", r.AgentCount())
	}
	hasE010 := false
	for _, e := range r.Errors() {
		if e.Code == "E010" {
			hasE010 = true
		}
	}
	if !hasE010 {
		t.Error("expected E010 for missing id")
	}
}

// CT-2: id "BadAgent" (CamelCase) → E011 warning, entry still loaded
func TestF2_CT2_IDKebabCaseEnforced(t *testing.T) {
	yaml := agentVocab + `
agents:
  - id: BadAgent
`
	r, err := registry.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if r.AgentCount() != 1 {
		t.Errorf("expected agent loaded despite bad ID, got count=%d", r.AgentCount())
	}
	hasE011 := false
	for _, e := range r.Errors() {
		if e.Code == "E011" {
			hasE011 = true
		}
	}
	if !hasE011 {
		t.Error("expected E011 warning for invalid kebab-case ID")
	}
}

// CT-3: id "my-agent-01" → valid kebab-case, no error
func TestF2_CT3_KebabCasePasses(t *testing.T) {
	yaml := agentVocab + `
agents:
  - id: my-agent-01
`
	r, err := registry.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	for _, e := range r.Errors() {
		if e.Code == "E011" {
			t.Errorf("unexpected E011 for valid ID: %s", e.Message)
		}
	}
	if _, ok := r.LookupAgent("my-agent-01"); !ok {
		t.Error("expected my-agent-01 to be loaded")
	}
}

// CT-4: bare entry (id only) → all defaults applied
func TestF2_CT4_DefaultsApplied(t *testing.T) {
	yaml := agentVocab + `
agents:
  - id: bare-agent
`
	r, err := registry.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	a, ok := r.LookupAgent("bare-agent")
	if !ok {
		t.Fatal("expected bare-agent to be loaded")
	}
	if a.Archetype != registry.DefaultArchetype {
		t.Errorf("archetype: want %q, got %q", registry.DefaultArchetype, a.Archetype)
	}
	if a.DefaultModel != registry.DefaultModel {
		t.Errorf("default_model: want %q, got %q", registry.DefaultModel, a.DefaultModel)
	}
	if a.Provider != registry.DefaultProvider {
		t.Errorf("provider: want %q, got %q", registry.DefaultProvider, a.Provider)
	}
	if len(a.Languages) != 0 {
		t.Errorf("languages: want [], got %v", a.Languages)
	}
	if a.CWD != nil {
		t.Errorf("cwd: want nil, got %v", *a.CWD)
	}
}

// CT-5: cwd: null → IsWorkspaceScoped() = true
func TestF2_CT5_CWDNullWorkspaceScoped(t *testing.T) {
	yaml := agentVocab + `
agents:
  - id: ws-agent
    cwd: null
`
	r, err := registry.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	a, ok := r.LookupAgent("ws-agent")
	if !ok {
		t.Fatal("expected ws-agent to be loaded")
	}
	if !a.IsWorkspaceScoped() {
		t.Error("expected IsWorkspaceScoped()=true for cwd: null")
	}
	if a.CWD != nil {
		t.Errorf("expected CWD=nil, got %v", *a.CWD)
	}
}

// CT-6: explicit non-null cwd → IsWorkspaceScoped() = false
func TestF2_CT6_ExplicitCWDStandalone(t *testing.T) {
	yaml := agentVocab + `
agents:
  - id: standalone-agent
    cwd: "~/.automation-metrics/"
`
	r, err := registry.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	a, ok := r.LookupAgent("standalone-agent")
	if !ok {
		t.Fatal("expected standalone-agent to be loaded")
	}
	if a.IsWorkspaceScoped() {
		t.Error("expected IsWorkspaceScoped()=false for non-null cwd")
	}
	if a.CWD == nil || *a.CWD != "~/.automation-metrics/" {
		t.Errorf("expected cwd=~/.automation-metrics/, got %v", a.CWD)
	}
}

// CT-7: invalid archetype → W010, default applied
func TestF2_CT7_InvalidArchetype(t *testing.T) {
	yaml := agentVocab + `
agents:
  - id: bad-arch-agent
    archetype: unknown_type
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
		t.Error("expected W010 for invalid archetype")
	}
	a, _ := r.LookupAgent("bad-arch-agent")
	if a.Archetype != registry.DefaultArchetype {
		t.Errorf("expected default archetype applied, got %q", a.Archetype)
	}
}

// CT-8: invalid default_model → W011, default applied
func TestF2_CT8_InvalidModel(t *testing.T) {
	yaml := agentVocab + `
agents:
  - id: bad-model-agent
    default_model: gpt-99
`
	r, err := registry.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	hasW011 := false
	for _, e := range r.Errors() {
		if e.Code == "W011" {
			hasW011 = true
		}
	}
	if !hasW011 {
		t.Error("expected W011 for invalid default_model")
	}
	a, _ := r.LookupAgent("bad-model-agent")
	if a.DefaultModel != registry.DefaultModel {
		t.Errorf("expected default model applied, got %q", a.DefaultModel)
	}
}

// CT-9: invalid provider → W012, default applied
func TestF2_CT9_InvalidProvider(t *testing.T) {
	yaml := agentVocab + `
agents:
  - id: bad-provider-agent
    provider: unknown_llm
`
	r, err := registry.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	hasW012 := false
	for _, e := range r.Errors() {
		if e.Code == "W012" {
			hasW012 = true
		}
	}
	if !hasW012 {
		t.Error("expected W012 for invalid provider")
	}
	a, _ := r.LookupAgent("bad-provider-agent")
	if a.Provider != registry.DefaultProvider {
		t.Errorf("expected default provider applied, got %q", a.Provider)
	}
}

// CT-10: vocabulary block absent → W003 emitted, agent still loaded
func TestF2_CT10_VocabularyAbsent(t *testing.T) {
	yaml := `
schema_version: "2.0"
org: test
agents:
  - id: no-vocab-agent
`
	r, err := registry.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	hasW003 := false
	for _, e := range r.Errors() {
		if e.Code == "W003" {
			hasW003 = true
		}
	}
	if !hasW003 {
		t.Error("expected W003 when vocabulary block is absent")
	}
	if _, ok := r.LookupAgent("no-vocab-agent"); !ok {
		t.Error("expected agent to be loaded even when vocabulary absent")
	}
}

// BT-5: ModelTier() returns correct tier for claude-sonnet-4-6
func TestF2_BT5_ModelTierResolution(t *testing.T) {
	yaml := agentVocab + `
agents:
  - id: sonnet-agent
    default_model: claude-sonnet-4-6
`
	r, err := registry.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	a, ok := r.LookupAgent("sonnet-agent")
	if !ok {
		t.Fatal("expected sonnet-agent")
	}
	vocab := r.Vocabulary()
	if vocab == nil {
		t.Fatal("expected vocabulary")
	}
	tier, ok := a.ModelTier(*vocab)
	if !ok || tier != "balanced" {
		t.Errorf("expected ModelTier=balanced, got %q (ok=%v)", tier, ok)
	}
}
