package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Contract Tests ---

// CT-1: ClaudeCodeInstrumentor.Detect() = true when ~/.claude/ exists
func TestClaudeCodeDetect_True(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &ClaudeCodeInstrumentor{homeDir: dir}
	if !inst.Detect() {
		t.Fatal("expected Detect() = true when .claude/ exists")
	}
}

// CT-2: ClaudeCodeInstrumentor.Detect() = false when ~/.claude/ absent
func TestClaudeCodeDetect_False(t *testing.T) {
	dir := t.TempDir()
	inst := &ClaudeCodeInstrumentor{homeDir: dir}
	if inst.Detect() {
		t.Fatal("expected Detect() = false when .claude/ absent")
	}
}

// CT-3: Instrument() creates hook file at correct path with correct content
func TestClaudeCodeInstrument_CreatesHookFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &ClaudeCodeInstrumentor{homeDir: dir}
	if err := inst.Instrument(context.Background(), InstrumentOpts{HomeDir: dir}); err != nil {
		t.Fatalf("Instrument() error: %v", err)
	}
	hookPath := filepath.Join(dir, ".claude", "hooks", "castex-lifecycle.fish")
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("hook file not created: %v", err)
	}
	if !strings.Contains(string(data), "castex") {
		t.Error("hook content should reference castex")
	}
}

// CT-4: IsInstrumented() = true when hook file already present
func TestClaudeCodeIsInstrumented_True(t *testing.T) {
	dir := t.TempDir()
	hookPath := filepath.Join(dir, ".claude", "hooks", "castex-lifecycle.fish")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("# hook"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := &ClaudeCodeInstrumentor{homeDir: dir}
	if !inst.IsInstrumented() {
		t.Fatal("expected IsInstrumented() = true when hook file present")
	}
}

// CT-5: Instrument() is idempotent - no file modification on second call
func TestClaudeCodeInstrument_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &ClaudeCodeInstrumentor{homeDir: dir}
	opts := InstrumentOpts{HomeDir: dir}
	if err := inst.Instrument(context.Background(), opts); err != nil {
		t.Fatalf("first Instrument() error: %v", err)
	}
	hookPath := inst.hookFilePath()
	stat1, _ := os.Stat(hookPath)

	if err := inst.Instrument(context.Background(), opts); err != nil {
		t.Fatalf("second Instrument() error: %v", err)
	}
	stat2, _ := os.Stat(hookPath)
	if stat1.Size() != stat2.Size() {
		t.Error("hook file modified on second Instrument() call")
	}
}

// CT-6: agents.jsonl written with correct fields after success
func TestDetectionRegistry_WritesCorrectFields(t *testing.T) {
	dir := t.TempDir()
	reg := &DetectionRegistry{path: filepath.Join(dir, "agents.jsonl")}
	rec := AgentRecord{
		AgentID:        "claude-code",
		HookPath:       "/tmp/hook.fish",
		EventBusPath:   "/tmp/events",
		InstrumentedAt: "20260606T000000Z",
		Status:         "instrumented",
		CastexVersion:  "dev",
	}
	if err := reg.Write(rec); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "agents.jsonl"))
	if err != nil {
		t.Fatalf("agents.jsonl not created: %v", err)
	}
	var got AgentRecord
	if err := json.Unmarshal(bytes.TrimSpace(data), &got); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if got.AgentID != "claude-code" {
		t.Errorf("agent_id = %q, want claude-code", got.AgentID)
	}
	if got.HookPath == "" {
		t.Error("hook_path should be non-empty")
	}
	if got.Status != "instrumented" {
		t.Errorf("status = %q, want instrumented", got.Status)
	}
}

// CT-7: agents.jsonl deduplication - re-run produces one record per agent
func TestDetectionRegistry_Deduplication(t *testing.T) {
	dir := t.TempDir()
	reg := &DetectionRegistry{path: filepath.Join(dir, "agents.jsonl")}
	rec := AgentRecord{AgentID: "claude-code", Status: "instrumented"}
	if err := reg.Write(rec); err != nil {
		t.Fatal(err)
	}
	if err := reg.Write(rec); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "agents.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	if lines != 1 {
		t.Errorf("expected 1 record after duplicate writes, got %d", lines)
	}
}

// CT-8: --dry-run makes zero filesystem writes
func TestInitRunDryRun_ZeroWrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := InstrumentOpts{DryRun: true, HomeDir: dir, EventBus: filepath.Join(dir, "events")}
	var buf bytes.Buffer
	cmd := newInitCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	if err := runInit(cmd, opts, ""); err != nil {
		t.Fatalf("runInit dry-run error: %v", err)
	}
	// Hook file must not exist
	hookPath := filepath.Join(dir, ".claude", "hooks", "castex-lifecycle.fish")
	if _, err := os.Stat(hookPath); err == nil {
		t.Error("hook file should not be created in dry-run mode")
	}
	// agents.jsonl must not exist
	agentsPath := filepath.Join(dir, ".castex", "agents.jsonl")
	if _, err := os.Stat(agentsPath); err == nil {
		t.Error("agents.jsonl should not be created in dry-run mode")
	}
}

// CT-9: Undetected agent is skipped - exit 0, no error
func TestInit_UndetectedAgentSkipped(t *testing.T) {
	dir := t.TempDir()
	// No .codex dir → CodexInstrumentor skipped
	opts := InstrumentOpts{HomeDir: dir, EventBus: filepath.Join(dir, "events")}
	var buf bytes.Buffer
	cmd := newInitCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	// Only run codex so we can verify skip behavior specifically
	if err := runInit(cmd, opts, "codex"); err != nil {
		t.Fatalf("expected no error for undetected agent, got: %v", err)
	}
	if !strings.Contains(buf.String(), "not detected") {
		t.Errorf("expected 'not detected' in output, got: %s", buf.String())
	}
}

// CT-10: hook_write_failed (E101) when hook dir is read-only
func TestClaudeCodeInstrument_HookDirReadOnly(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping read-only test as root")
	}
	dir := t.TempDir()
	// Create .claude/ first (writable), then make hooks/ read-only.
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooksDir := filepath.Join(dir, ".claude", "hooks")
	if err := os.Mkdir(hooksDir, 0o555); err != nil {
		t.Fatal(err)
	}
	inst := &ClaudeCodeInstrumentor{homeDir: dir}
	err := inst.Instrument(context.Background(), InstrumentOpts{HomeDir: dir})
	if err == nil {
		t.Fatal("expected error on read-only hook dir, got nil")
	}
	if !strings.Contains(err.Error(), "E101") {
		t.Errorf("expected E101 error code, got: %v", err)
	}
}

// CT-11: instrumentation_partial (E104) - overall exit 1 when any agent fails
func TestInit_PartialInstrumentation_ExitsOne(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping read-only test as root")
	}
	dir := t.TempDir()
	// Create .claude/ (writable), then hooks/ as read-only to force failure.
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooksDir := filepath.Join(dir, ".claude", "hooks")
	if err := os.Mkdir(hooksDir, 0o555); err != nil {
		t.Fatal(err)
	}
	opts := InstrumentOpts{HomeDir: dir, EventBus: filepath.Join(dir, "events")}
	var buf bytes.Buffer
	cmd := newInitCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := runInit(cmd, opts, "claude-code")
	if err == nil {
		t.Fatal("expected E104 error, got nil")
	}
	if !strings.Contains(err.Error(), "E104") {
		t.Errorf("expected E104 error code, got: %v", err)
	}
}

// CT-12: PiInstrumentor.IsInstrumented() = true when hook file already present
func TestPiIsInstrumented_True(t *testing.T) {
	dir := t.TempDir()
	hookPath := filepath.Join(dir, ".pi", "hooks", "castex-lifecycle.fish")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("# hook"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := &PiInstrumentor{homeDir: dir}
	if !inst.IsInstrumented() {
		t.Fatal("expected IsInstrumented() = true when Pi hook file present")
	}
}

// --- Behavioral Tests ---

// BT-1: Fresh machine: Claude Code + Pi detected and instrumented end-to-end
func TestInit_BT1_FreshMachine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := InstrumentOpts{HomeDir: dir, EventBus: filepath.Join(dir, "events")}
	var buf bytes.Buffer
	cmd := newInitCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	if err := runInit(cmd, opts, ""); err != nil {
		t.Fatalf("runInit error: %v", err)
	}
	// Both should be instrumented
	if !(&ClaudeCodeInstrumentor{homeDir: dir}).IsInstrumented() {
		t.Error("claude-code not instrumented")
	}
	if !(&PiInstrumentor{homeDir: dir}).IsInstrumented() {
		t.Error("pi not instrumented")
	}
	// agents.jsonl should have two entries
	data, err := os.ReadFile(filepath.Join(dir, ".castex", "agents.jsonl"))
	if err != nil {
		t.Fatal("agents.jsonl not written")
	}
	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("expected 2 records in agents.jsonl, got %d", lines)
	}
}

// BT-2: Re-run on fully instrumented machine: no changes, correct output
func TestInit_BT2_AlreadyInstrumented(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := InstrumentOpts{HomeDir: dir, EventBus: filepath.Join(dir, "events")}

	// First run
	cmd1 := newInitCmd()
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetErr(&bytes.Buffer{})
	if err := runInit(cmd1, opts, "claude-code"); err != nil {
		t.Fatalf("first runInit error: %v", err)
	}

	// Second run - should say "already instrumented"
	var buf bytes.Buffer
	cmd2 := newInitCmd()
	cmd2.SetOut(&buf)
	cmd2.SetErr(&bytes.Buffer{})
	if err := runInit(cmd2, opts, "claude-code"); err != nil {
		t.Fatalf("second runInit error: %v", err)
	}
	if !strings.Contains(buf.String(), "already instrumented") {
		t.Errorf("expected 'already instrumented' on re-run, got: %s", buf.String())
	}
}

// BT-3: --agent claude-code flag: only ClaudeCodeInstrumentor runs
func TestInit_BT3_AgentFilter(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := InstrumentOpts{HomeDir: dir, EventBus: filepath.Join(dir, "events")}
	var buf bytes.Buffer
	cmd := newInitCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	if err := runInit(cmd, opts, "claude-code"); err != nil {
		t.Fatalf("runInit error: %v", err)
	}
	// Only claude-code should be instrumented
	if !(&ClaudeCodeInstrumentor{homeDir: dir}).IsInstrumented() {
		t.Error("claude-code should be instrumented")
	}
	if (&PiInstrumentor{homeDir: dir}).IsInstrumented() {
		t.Error("pi should NOT be instrumented with claude-code filter")
	}
}

// BT-4: Codex not installed: skipped gracefully, exit 0
func TestInit_BT4_CodexNotInstalled(t *testing.T) {
	dir := t.TempDir()
	opts := InstrumentOpts{HomeDir: dir, EventBus: filepath.Join(dir, "events")}
	var buf bytes.Buffer
	cmd := newInitCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	if err := runInit(cmd, opts, "codex"); err != nil {
		t.Fatalf("expected exit 0 for missing codex, got: %v", err)
	}
}

// --- Regression Guards ---

// RG-1: Hook content never contains API key patterns or credential strings
func TestInit_RG1_NoCredentialsInHook(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &ClaudeCodeInstrumentor{homeDir: dir}
	if err := inst.Instrument(context.Background(), InstrumentOpts{HomeDir: dir}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(inst.hookFilePath())
	content := string(data)
	credPatterns := []string{"ANTHROPIC_API_KEY", "sk-ant-", "api_key", "password", "secret", "token ="}
	for _, pat := range credPatterns {
		if strings.Contains(content, pat) {
			t.Errorf("hook content contains credential pattern %q", pat)
		}
	}
}

// RG-2: agents.jsonl never grows duplicate agent records on repeated runs
func TestInit_RG2_NoDuplicateAgentRecords(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := InstrumentOpts{HomeDir: dir, EventBus: filepath.Join(dir, "events")}

	for i := 0; i < 3; i++ {
		cmd := newInitCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := runInit(cmd, opts, "claude-code"); err != nil {
			t.Fatalf("run %d error: %v", i, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, ".castex", "agents.jsonl"))
	if err != nil {
		t.Fatal("agents.jsonl not created")
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 record after 3 runs, got %d", count)
	}
}
