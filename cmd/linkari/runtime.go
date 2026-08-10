package main

// EPIC-038 M1 + OAuth2 fix: ExecutionRuntime abstraction for subprocess isolation.
//
// Defines the interface that all subprocess invocations (ffmpeg, whisper-cli,
// claude CLI) converge on. Three implementations exist:
//
//   - LocalRuntime: delegates to the existing runFfmpegConvert, runWhisperCLI,
//     and runClaudeHaiku function vars — zero behavioral change for the common case.
//   - ContainerRuntime: gVisor-sandboxed implementation for ffmpeg and whisper.
//   - HybridRuntime: routes ffmpeg and whisper through ContainerRuntime but
//     always routes InvokeClaudeSubprocess through LocalRuntime. See HybridRuntime
//     godoc for the OAuth2 rationale.
//
// NewExecutionRuntime is the factory. It reads SandboxConfig and routes to
// HybridRuntime when sandbox.enabled is true, otherwise LocalRuntime.
// M6 adds runtime.Ping() + graceful fallback on top of this seam.
//
// NOTE: claude CLI is always executed via LocalRuntime regardless of sandbox.enabled.
// See HybridRuntime for the architectural rationale.

import (
	"context"
	"errors"
)

// ErrContainerUnavailable is returned by ContainerRuntime when the CRI socket
// is not yet wired (M1 stub state) or when the runtime is unreachable.
var ErrContainerUnavailable = errors.New("container runtime unavailable")

// ErrContainerOOM is wrapped into errors returned by ContainerRuntime methods
// when the container exits with code 137 (SIGKILL from the kernel OOM killer).
// Callers may use errors.Is to detect OOM specifically for telemetry.
var ErrContainerOOM = errors.New("container OOM killed")

// ExecutionRuntime is the subprocess isolation contract.
// All invocations of ffmpeg, whisper-cli, and the claude CLI must go through
// an ExecutionRuntime so the sandbox boundary can be swapped without touching
// call sites in server_score.go or cmd_triage.go.
type ExecutionRuntime interface {
	// InvokeFFmpeg converts inputPath to outputPath via ffmpeg.
	// The conversion is always 16kHz mono WAV (the whisper pipeline's expected format).
	InvokeFFmpeg(ctx context.Context, inputPath, outputPath string) error

	// InvokeWhisperTranscribe transcribes wavPath using the given model.
	// modelPath may be empty; implementations fall back to the compiled-in default.
	// Returns the raw transcript text.
	InvokeWhisperTranscribe(ctx context.Context, wavPath, modelPath string) (string, error)

	// InvokeClaudeSubprocess calls the claude CLI for a single-turn Haiku inference.
	// Returns the plain-text response output.
	InvokeClaudeSubprocess(ctx context.Context, systemPrompt, content string) (string, error)
}

// SandboxConfig is the YAML-deserialisable block that controls the container
// runtime. It lives under the `sandbox:` key in server.yaml.
//
// When sandbox.enabled is true, ffmpeg and whisper invocations are routed through
// ContainerRuntime (gVisor sandbox). The claude CLI is always routed through
// LocalRuntime regardless of this setting — see HybridRuntime for the rationale.
//
// Example server.yaml fragment:
//
//	sandbox:
//	  enabled: true
//	  runtime_socket: /run/containerd/containerd.sock
//	  image_registry: ghcr.io/blo-grindr/linkari
//	  memory_limit_mb: 2048
//	  cpu_limit_cores: 2.0
type SandboxConfig struct {
	// Enabled routes ffmpeg and whisper invocations through ContainerRuntime.
	// InvokeClaudeSubprocess always runs locally (see HybridRuntime).
	// When false (default), LocalRuntime is used and no container deps are required.
	Enabled bool `toml:"enabled"`

	// RuntimeSocket is the path to the CRI/containerd Unix socket.
	// Default: /run/containerd/containerd.sock
	RuntimeSocket string `toml:"runtime_socket"`

	// ImageRegistry is the OCI image registry prefix for sandbox images.
	// Images are pulled as <ImageRegistry>/ffmpeg, whisper, claude-sandbox.
	ImageRegistry string `toml:"image_registry"`

	// MemoryLimitMB is the per-container memory ceiling in megabytes.
	// 0 means no limit (not recommended for production). Default: 2048.
	MemoryLimitMB int `toml:"memory_limit_mb"`

	// CPULimitCores is the per-container CPU quota as fractional cores.
	// 0 means no limit. Default: 2.0.
	CPULimitCores float64 `toml:"cpu_limit_cores"`
}

// runtimeSocket returns the effective CRI socket path.
// For Lima: set runtime_socket in server.yaml to ~/.lima/lima-gvisor/containerd.sock
// (the socket forwarded by lima-gvisor.yaml's portForwards block).
// The default /run/containerd/containerd.sock is only reachable from inside the VM.
func (s SandboxConfig) runtimeSocket() string {
	if s.RuntimeSocket != "" {
		return s.RuntimeSocket
	}
	return "/run/containerd/containerd.sock"
}

// NewExecutionRuntime returns the appropriate ExecutionRuntime for the given
// SandboxConfig. Routes to HybridRuntime when sandbox.enabled is true
// (ffmpeg and whisper sandboxed; claude always local), otherwise LocalRuntime.
func NewExecutionRuntime(cfg SandboxConfig) ExecutionRuntime {
	if cfg.Enabled {
		return &HybridRuntime{container: &ContainerRuntime{cfg: cfg}}
	}
	return &LocalRuntime{}
}

// ─── LocalRuntime ─────────────────────────────────────────────────────────────

// LocalRuntime delegates to the existing function vars (runFfmpegConvert,
// runWhisperCLI, runClaudeHaiku). It is the default implementation and
// preserves the full test-seam story: callers that override execFfmpegConvert
// et al. continue to work because LocalRuntime calls through those vars.
type LocalRuntime struct{}

func (LocalRuntime) InvokeFFmpeg(ctx context.Context, inputPath, outputPath string) error {
	return execFfmpegConvert(ctx, inputPath, outputPath)
}

func (LocalRuntime) InvokeWhisperTranscribe(ctx context.Context, wavPath, modelPath string) (string, error) {
	return execWhisper(ctx, wavPath, modelPath)
}

func (LocalRuntime) InvokeClaudeSubprocess(ctx context.Context, systemPrompt, content string) (string, error) {
	return backendComplete(ctx, nil, systemPrompt, content)
}

// ─── HybridRuntime ────────────────────────────────────────────────────────────

// HybridRuntime routes ffmpeg and whisper through ContainerRuntime (gVisor
// sandbox) but always routes InvokeClaudeSubprocess through LocalRuntime.
//
// Why claude CLI is always local:
//
//  1. OAuth2 session tokens: The claude CLI authenticates via OAuth2 session
//     tokens stored in ~/.claude/. Mounting that directory into a container
//     concentrates credential risk in the container image layer — any escape
//     or image leak would expose long-lived auth material.
//
//  2. Prompt injection is not a syscall problem: gVisor's value is syscall
//     interposition. It cannot prevent a model from exfiltrating context in
//     its natural-language output, which is the actual threat surface for
//     claude invocations. A gVisor boundary provides no meaningful mitigation
//     for prompt-injection attacks — it only adds latency and complexity.
//
// The HybridRuntime is returned by NewExecutionRuntime and
// NewExecutionRuntimeWithPing when sandbox.enabled is true.
type HybridRuntime struct {
	container *ContainerRuntime
	local     LocalRuntime
}

func (h *HybridRuntime) InvokeFFmpeg(ctx context.Context, inputPath, outputPath string) error {
	return h.container.InvokeFFmpeg(ctx, inputPath, outputPath)
}

func (h *HybridRuntime) InvokeWhisperTranscribe(ctx context.Context, wavPath, modelPath string) (string, error) {
	return h.container.InvokeWhisperTranscribe(ctx, wavPath, modelPath)
}

// InvokeClaudeSubprocess always delegates to LocalRuntime.
// See HybridRuntime godoc for the OAuth2 + prompt-injection rationale.
func (h *HybridRuntime) InvokeClaudeSubprocess(ctx context.Context, systemPrompt, content string) (string, error) {
	return h.local.InvokeClaudeSubprocess(ctx, systemPrompt, content)
}

// ─── ContainerRuntime ─────────────────────────────────────────────────────────

// ContainerRuntime is the gVisor-sandboxed implementation.
// M4 (runtime_container.go) implements InvokeFFmpeg and InvokeWhisperTranscribe
// using the containerd v2 client + gVisor runsc.
// M5 adds mount path resolution and stdin/stdout piping.
// M6 adds timeout policy and native exec fallback.
type ContainerRuntime struct {
	cfg SandboxConfig
}
