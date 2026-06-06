package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// Instrumentor detects and instruments a single AI coding agent with lifecycle hooks.
type Instrumentor interface {
	Name() string
	Detect() bool
	IsInstrumented() bool
	Instrument(ctx context.Context, opts InstrumentOpts) error
	Verify() bool
}

// InstrumentOpts carries options for the init command.
type InstrumentOpts struct {
	DryRun   bool
	EventBus string
	HomeDir  string
}

// AgentRecord is one entry written to ~/.castex/agents.jsonl.
type AgentRecord struct {
	AgentID        string `json:"agent_id"`
	HookPath       string `json:"hook_path"`
	EventBusPath   string `json:"event_bus_path"`
	InstrumentedAt string `json:"instrumented_at"`
	Status         string `json:"status"`
	CastexVersion  string `json:"castex_version"`
}

// partialInstrumentationError represents E104.
type partialInstrumentationError struct {
	Failed []string
}

func (e *partialInstrumentationError) Error() string {
	return fmt.Sprintf("[E104] instrumentation_partial: failed agents: %v", e.Failed)
}

// DetectionRegistry writes agent records to ~/.castex/agents.jsonl (append-only, dedup by agent_id).
type DetectionRegistry struct {
	path   string
	dryRun bool
}

func (r *DetectionRegistry) Write(rec AgentRecord) error {
	if r.dryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}

	// Read existing records to check for duplicates.
	existing := map[string]bool{}
	if data, err := os.ReadFile(r.path); err == nil {
		for _, line := range splitLines(data) {
			if len(line) == 0 {
				continue
			}
			var r AgentRecord
			if err := json.Unmarshal(line, &r); err == nil && r.AgentID != "" {
				existing[r.AgentID] = true
			}
		}
	}

	if existing[rec.AgentID] {
		return nil // already recorded - dedup
	}

	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", b)
	return err
}

// splitLines splits a byte slice on newlines, returning trimmed non-empty lines.
func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			line := data[start:i]
			out = append(out, line)
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// claudeCodeHookContent is the lifecycle hook written to ~/.claude/hooks/castex-lifecycle.fish.
// It emits session events to the automation-metrics event bus.
const claudeCodeHookContent = `#!/usr/bin/env fish
# castex lifecycle hook - managed by castex init
# Emits session events to automation-metrics event bus. No credentials.
set -l bus_dir ~/.automation-metrics/events
set -l today (date -u +%Y-%m-%d)
mkdir -p $bus_dir
printf '{"schema_version":"1","event_type":"session_event","agent_tool":"claude-code","session_type":"agentic","timestamp":"%s"}\n' \
    (date -u +%Y%m%dT%H%M%SZ) >> $bus_dir/$today.jsonl
`

const piHookContent = `#!/usr/bin/env fish
# castex lifecycle hook - managed by castex init
# Emits session events to automation-metrics event bus. No credentials.
set -l bus_dir ~/.automation-metrics/events
set -l today (date -u +%Y-%m-%d)
mkdir -p $bus_dir
printf '{"schema_version":"1","event_type":"session_event","agent_tool":"pi","session_type":"agentic","timestamp":"%s"}\n' \
    (date -u +%Y%m%dT%H%M%SZ) >> $bus_dir/$today.jsonl
`

const codexHookContent = `#!/usr/bin/env fish
# castex lifecycle hook - managed by castex init
# Emits session events to automation-metrics event bus. No credentials.
set -l bus_dir ~/.automation-metrics/events
set -l today (date -u +%Y-%m-%d)
mkdir -p $bus_dir
printf '{"schema_version":"1","event_type":"session_event","agent_tool":"codex","session_type":"agentic","timestamp":"%s"}\n' \
    (date -u +%Y%m%dT%H%M%SZ) >> $bus_dir/$today.jsonl
`

// ClaudeCodeInstrumentor instruments Claude Code via ~/.claude/hooks/.
type ClaudeCodeInstrumentor struct {
	homeDir string
}

func (i *ClaudeCodeInstrumentor) Name() string { return "claude-code" }

func (i *ClaudeCodeInstrumentor) Detect() bool {
	_, err := os.Stat(filepath.Join(i.homeDir, ".claude"))
	return err == nil
}

func (i *ClaudeCodeInstrumentor) hookFilePath() string {
	return filepath.Join(i.homeDir, ".claude", "hooks", "castex-lifecycle.fish")
}

func (i *ClaudeCodeInstrumentor) IsInstrumented() bool {
	_, err := os.Stat(i.hookFilePath())
	return err == nil
}

func (i *ClaudeCodeInstrumentor) Instrument(_ context.Context, _ InstrumentOpts) error {
	hookDir := filepath.Dir(i.hookFilePath())
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		return &hookWriteError{AgentName: i.Name(), Path: i.hookFilePath(), Cause: err}
	}
	if err := os.WriteFile(i.hookFilePath(), []byte(claudeCodeHookContent), 0o644); err != nil {
		return &hookWriteError{AgentName: i.Name(), Path: i.hookFilePath(), Cause: err}
	}
	return nil
}

func (i *ClaudeCodeInstrumentor) Verify() bool {
	data, err := os.ReadFile(i.hookFilePath())
	if err != nil {
		return false
	}
	return len(data) > 0
}

// hookWriteError wraps E101.
type hookWriteError struct {
	AgentName string
	Path      string
	Cause     error
}

func (e *hookWriteError) Error() string {
	return fmt.Sprintf("[E101] hook_write_failed: agent=%s path=%s: %v", e.AgentName, e.Path, e.Cause)
}

func (e *hookWriteError) Unwrap() error { return e.Cause }

// PiInstrumentor instruments Pi via ~/.pi/hooks/.
type PiInstrumentor struct {
	homeDir string
}

func (i *PiInstrumentor) Name() string { return "pi" }

func (i *PiInstrumentor) Detect() bool {
	_, err := os.Stat(filepath.Join(i.homeDir, ".pi"))
	return err == nil
}

func (i *PiInstrumentor) hookFilePath() string {
	return filepath.Join(i.homeDir, ".pi", "hooks", "castex-lifecycle.fish")
}

func (i *PiInstrumentor) IsInstrumented() bool {
	_, err := os.Stat(i.hookFilePath())
	return err == nil
}

func (i *PiInstrumentor) Instrument(_ context.Context, _ InstrumentOpts) error {
	hookDir := filepath.Dir(i.hookFilePath())
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		return &hookWriteError{AgentName: i.Name(), Path: i.hookFilePath(), Cause: err}
	}
	if err := os.WriteFile(i.hookFilePath(), []byte(piHookContent), 0o644); err != nil {
		return &hookWriteError{AgentName: i.Name(), Path: i.hookFilePath(), Cause: err}
	}
	return nil
}

func (i *PiInstrumentor) Verify() bool {
	data, err := os.ReadFile(i.hookFilePath())
	if err != nil {
		return false
	}
	return len(data) > 0
}

// CodexInstrumentor instruments Codex CLI via ~/.codex/hooks/.
type CodexInstrumentor struct {
	homeDir string
}

func (i *CodexInstrumentor) Name() string { return "codex" }

func (i *CodexInstrumentor) Detect() bool {
	_, err := os.Stat(filepath.Join(i.homeDir, ".codex"))
	return err == nil
}

func (i *CodexInstrumentor) hookFilePath() string {
	return filepath.Join(i.homeDir, ".codex", "hooks", "castex-lifecycle.fish")
}

func (i *CodexInstrumentor) IsInstrumented() bool {
	_, err := os.Stat(i.hookFilePath())
	return err == nil
}

func (i *CodexInstrumentor) Instrument(_ context.Context, _ InstrumentOpts) error {
	hookDir := filepath.Dir(i.hookFilePath())
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		return &hookWriteError{AgentName: i.Name(), Path: i.hookFilePath(), Cause: err}
	}
	if err := os.WriteFile(i.hookFilePath(), []byte(codexHookContent), 0o644); err != nil {
		return &hookWriteError{AgentName: i.Name(), Path: i.hookFilePath(), Cause: err}
	}
	return nil
}

func (i *CodexInstrumentor) Verify() bool {
	data, err := os.ReadFile(i.hookFilePath())
	if err != nil {
		return false
	}
	return len(data) > 0
}

func newInitCmd() *cobra.Command {
	var dryRun bool
	var agentFilter string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Detect installed AI coding agents and instrument them with lifecycle hooks",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			opts := InstrumentOpts{
				DryRun:   dryRun,
				EventBus: filepath.Join(home, ".automation-metrics", "events"),
				HomeDir:  home,
			}
			return runInit(cmd, opts, agentFilter)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be done without making changes")
	cmd.Flags().StringVar(&agentFilter, "agent", "", "instrument only this agent (e.g. claude-code)")
	return cmd
}

func runInit(cmd *cobra.Command, opts InstrumentOpts, agentFilter string) error {
	instrumentors := []Instrumentor{
		&ClaudeCodeInstrumentor{homeDir: opts.HomeDir},
		&PiInstrumentor{homeDir: opts.HomeDir},
		&CodexInstrumentor{homeDir: opts.HomeDir},
	}

	if agentFilter != "" {
		var filtered []Instrumentor
		for _, inst := range instrumentors {
			if inst.Name() == agentFilter {
				filtered = append(filtered, inst)
			}
		}
		instrumentors = filtered
	}

	reg := &DetectionRegistry{
		path:   filepath.Join(opts.HomeDir, ".castex", "agents.jsonl"),
		dryRun: opts.DryRun,
	}

	var failed []string
	for _, inst := range instrumentors {
		if !inst.Detect() {
			fmt.Fprintf(cmd.OutOrStdout(), "castex init: %s: not detected, skipping\n", inst.Name())
			continue
		}
		if inst.IsInstrumented() {
			fmt.Fprintf(cmd.OutOrStdout(), "castex init: %s: already instrumented\n", inst.Name())
			if !opts.DryRun {
				if err := reg.Write(agentRecordFor(inst, opts)); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "castex init: registry write warning: %v\n", err)
				}
			}
			continue
		}
		if opts.DryRun {
			fmt.Fprintf(cmd.OutOrStdout(), "castex init: %s: would instrument (dry-run)\n", inst.Name())
			continue
		}
		if err := inst.Instrument(context.Background(), opts); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "castex init: %s: %v\n", inst.Name(), err)
			failed = append(failed, inst.Name())
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "castex init: %s: instrumented\n", inst.Name())
		if err := reg.Write(agentRecordFor(inst, opts)); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "castex init: registry write warning: %v\n", err)
		}
	}

	if len(failed) > 0 {
		return &partialInstrumentationError{Failed: failed}
	}
	return nil
}

type hookPathProvider interface {
	hookFilePath() string
}

func agentRecordFor(inst Instrumentor, opts InstrumentOpts) AgentRecord {
	hookPath := ""
	if hp, ok := inst.(hookPathProvider); ok {
		hookPath = hp.hookFilePath()
	}
	return AgentRecord{
		AgentID:        inst.Name(),
		HookPath:       hookPath,
		EventBusPath:   opts.EventBus,
		InstrumentedAt: time.Now().UTC().Format("20060102T150405Z"),
		Status:         "instrumented",
		CastexVersion:  version,
	}
}
