package registry_test

// EPIC-088 (F3) contract tests: CT-1 through CT-8
// Covers CWD resolution: workspace scan, static fallback, idle/miss states.
// Run with: go test ./internal/registry/... -run F3

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thebrianlopez/runabout/internal/registry"
)

// makeTestRegistry creates a Registry from inline YAML with a standard vocabulary.
func makeTestRegistry(t *testing.T, agentsYAML string) *registry.Registry {
	t.Helper()
	yaml := `
schema_version: "2.0"
org: test
vocabulary:
  archetypes: [agentic_coder, orchestrator]
  model_tiers:
    balanced: [claude-sonnet-4-6]
  provider_types:
    cloud_anthropic: [anthropic]
` + agentsYAML
	r, err := registry.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("makeTestRegistry: %v", err)
	}
	return r
}

// writeWorkspace creates a workspace directory with workspace.yaml under dir/name/.
func writeWorkspace(t *testing.T, dir, name, agentID, agentPath string) string {
	t.Helper()
	wsDir := filepath.Join(dir, name)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", wsDir, err)
	}
	wsYAML := filepath.Join(wsDir, "workspace.yaml")
	content := fmt.Sprintf("repos:\n  - agent: %s\n    path: %s\n", agentID, agentPath)
	if err := os.WriteFile(wsYAML, []byte(content), 0o644); err != nil {
		t.Fatalf("writing workspace.yaml: %v", err)
	}
	return wsYAML
}

// CT-1: static CWD agent resolves to its cwd with state=active, source="static"
func TestF3_CT1_StaticCWDResolution(t *testing.T) {
	r := makeTestRegistry(t, `
agents:
  - id: static-agent
    cwd: /my/static/path
`)
	res := registry.NewResolver(r, filepath.Join(t.TempDir(), "*/workspace.yaml"))
	result, err := res.ResolveCWD("static-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CWD != "/my/static/path" {
		t.Errorf("CWD: want /my/static/path, got %q", result.CWD)
	}
	if result.State != registry.AgentActive {
		t.Errorf("State: want AgentActive, got %v", result.State)
	}
	if result.Source != "static" {
		t.Errorf("Source: want static, got %q", result.Source)
	}
}

// CT-2: workspace-scoped agent with active workspace → returns workspace CWD
func TestF3_CT2_WorkspaceScopedResolution(t *testing.T) {
	dir := t.TempDir()
	agentCWD := filepath.Join(dir, "repos", "my-repo")
	writeWorkspace(t, dir, "feature-123", "ws-agent", agentCWD)

	r := makeTestRegistry(t, `
agents:
  - id: ws-agent
    cwd: null
`)
	glob := filepath.Join(dir, "*/workspace.yaml")
	res := registry.NewResolver(r, glob)
	result, err := res.ResolveCWD("ws-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CWD != agentCWD {
		t.Errorf("CWD: want %q, got %q", agentCWD, result.CWD)
	}
	if result.State != registry.AgentActive {
		t.Errorf("State: want AgentActive, got %v", result.State)
	}
}

// CT-3: workspace-scoped agent with no active workspace → state=idle
func TestF3_CT3_IdleState(t *testing.T) {
	r := makeTestRegistry(t, `
agents:
  - id: idle-agent
    cwd: null
`)
	// Point glob to an empty temp dir
	glob := filepath.Join(t.TempDir(), "*/workspace.yaml")
	res := registry.NewResolver(r, glob)
	result, err := res.ResolveCWD("idle-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != registry.AgentIdle {
		t.Errorf("State: want AgentIdle, got %v", result.State)
	}
	if result.CWD != "" {
		t.Errorf("CWD: want empty for idle state, got %q", result.CWD)
	}
}

// CT-4: agent_id not in registry → state=miss
func TestF3_CT4_MissState(t *testing.T) {
	r := makeTestRegistry(t, `
agents:
  - id: known-agent
    cwd: /path
`)
	res := registry.NewResolver(r, filepath.Join(t.TempDir(), "*/workspace.yaml"))
	result, err := res.ResolveCWD("nonexistent-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != registry.AgentMiss {
		t.Errorf("State: want AgentMiss, got %v", result.State)
	}
}

// CT-5: agent with both static cwd AND workspace match → workspace wins
func TestF3_CT5_WorkspaceBeforeStatic(t *testing.T) {
	dir := t.TempDir()
	workspaceCWD := filepath.Join(dir, "workspace-dir")
	writeWorkspace(t, dir, "active-ws", "dual-agent", workspaceCWD)

	r := makeTestRegistry(t, `
agents:
  - id: dual-agent
    cwd: /static/cwd
`)
	glob := filepath.Join(dir, "*/workspace.yaml")
	res := registry.NewResolver(r, glob)
	result, err := res.ResolveCWD("dual-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CWD != workspaceCWD {
		t.Errorf("expected workspace CWD to win, got %q (want %q)", result.CWD, workspaceCWD)
	}
	if result.State != registry.AgentActive {
		t.Errorf("State: want AgentActive, got %v", result.State)
	}
}

// CT-6: agent in 2 active workspaces → resolves most recent + W102 warning
func TestF3_CT6_MultipleWorkspaceWarning(t *testing.T) {
	dir := t.TempDir()
	cwd1 := filepath.Join(dir, "cwd1")
	cwd2 := filepath.Join(dir, "cwd2")

	ws1 := writeWorkspace(t, dir, "ws-old", "multi-agent", cwd1)
	ws2 := writeWorkspace(t, dir, "ws-new", "multi-agent", cwd2)

	// Make ws1 older than ws2
	past := time.Now().Add(-2 * time.Hour)
	os.Chtimes(ws1, past, past)
	os.Chtimes(ws2, time.Now(), time.Now())

	r := makeTestRegistry(t, `
agents:
  - id: multi-agent
    cwd: null
`)
	glob := filepath.Join(dir, "*/workspace.yaml")
	res := registry.NewResolver(r, glob)
	result, err := res.ResolveCWD("multi-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != registry.AgentActive {
		t.Errorf("State: want AgentActive, got %v", result.State)
	}
	// Most recent workspace should win
	if result.CWD != cwd2 {
		t.Errorf("expected most recent CWD=%q, got %q", cwd2, result.CWD)
	}
	// W102 warning should be present
	if len(result.Warnings) == 0 {
		t.Error("expected W102 warning for multiple workspace match")
	}
}

// CT-7: workspace with status: closed → excluded from scan
func TestF3_CT7_ClosedWorkspaceExcluded(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "closed-ws")
	os.MkdirAll(wsDir, 0o755)
	wsYAML := filepath.Join(wsDir, "workspace.yaml")
	os.WriteFile(wsYAML, []byte(`
status: closed
repos:
  - agent: closed-agent
    path: /some/path
`), 0o644)

	r := makeTestRegistry(t, `
agents:
  - id: closed-agent
    cwd: null
`)
	glob := filepath.Join(dir, "*/workspace.yaml")
	res := registry.NewResolver(r, glob)
	result, err := res.ResolveCWD("closed-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Closed workspace excluded → agent is idle
	if result.State != registry.AgentIdle {
		t.Errorf("State: want AgentIdle (closed workspace excluded), got %v", result.State)
	}
}

// CT-8: one malformed + one valid workspace → valid one resolves correctly
func TestF3_CT8_MalformedWorkspaceSkipped(t *testing.T) {
	dir := t.TempDir()

	// Malformed workspace
	badDir := filepath.Join(dir, "bad-ws")
	os.MkdirAll(badDir, 0o755)
	os.WriteFile(filepath.Join(badDir, "workspace.yaml"), []byte(`%invalid: [yaml`), 0o644)

	// Valid workspace
	validCWD := filepath.Join(dir, "valid-cwd")
	writeWorkspace(t, dir, "good-ws", "skip-agent", validCWD)

	r := makeTestRegistry(t, `
agents:
  - id: skip-agent
    cwd: null
`)
	glob := filepath.Join(dir, "*/workspace.yaml")
	res := registry.NewResolver(r, glob)
	result, err := res.ResolveCWD("skip-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != registry.AgentActive {
		t.Errorf("State: want AgentActive (valid workspace found), got %v", result.State)
	}
	if result.CWD != validCWD {
		t.Errorf("CWD: want %q, got %q", validCWD, result.CWD)
	}
}
