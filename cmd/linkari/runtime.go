package main

// EPIC-038 M1: ExecutionRuntime abstraction for subprocess isolation.
//
// Defines the interface that all subprocess invocations (ffmpeg, whisper-cli,
// claude CLI) converge on. Two implementations ship in M1:
//
//   - LocalRuntime: delegates to the existing runFfmpegConvert, runWhisperCLI,
//     and runClaudeHaiku function vars — zero behavioral change for the common case.
//   - ContainerRuntime: stub; returns ErrContainerUnavailable until M4 wires
//     the real CRI client and container lifecycle.
//
// NewExecutionRuntime is the factory. It reads SandboxConfig and routes to
// ContainerRuntime when sandbox.enabled is true, otherwise LocalRuntime.
// M6 adds runtime.Ping() + graceful fallback on top of this seam.

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
// Example server.yaml fragment:
//
//	sandbox:
//	  enabled: true
//	  runtime_socket: /run/containerd/containerd.sock
//	  image_registry: ghcr.io/blo-grindr/linkari
//	  memory_limit_mb: 2048
//	  cpu_limit_cores: 2.0
type SandboxConfig struct {
	// Enabled routes all subprocess invocations through ContainerRuntime.
	// When false (default), LocalRuntime is used and no container deps are required.
	Enabled bool `yaml:"enabled"`

	// RuntimeSocket is the path to the CRI/containerd Unix socket.
	// Default: /run/containerd/containerd.sock
	RuntimeSocket string `yaml:"runtime_socket"`

	// ImageRegistry is the OCI image registry prefix for sandbox images.
	// Images are pulled as <ImageRegistry>/ffmpeg, whisper, claude-sandbox.
	ImageRegistry string `yaml:"image_registry"`

	// MemoryLimitMB is the per-container memory ceiling in megabytes.
	// 0 means no limit (not recommended for production). Default: 2048.
	MemoryLimitMB int `yaml:"memory_limit_mb"`

	// CPULimitCores is the per-container CPU quota as fractional cores.
	// 0 means no limit. Default: 2.0.
	CPULimitCores float64 `yaml:"cpu_limit_cores"`
}

// runtimeSocket returns the effective CRI socket path.
func (s SandboxConfig) runtimeSocket() string {
	if s.RuntimeSocket != "" {
		return s.RuntimeSocket
	}
	return "/run/containerd/containerd.sock"
}

// NewExecutionRuntime returns the appropriate ExecutionRuntime for the given
// SandboxConfig. Routes to ContainerRuntime when sandbox.enabled is true,
// otherwise LocalRuntime (zero overhead, existing behaviour).
func NewExecutionRuntime(cfg SandboxConfig) ExecutionRuntime {
	if cfg.Enabled {
		return &ContainerRuntime{cfg: cfg}
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
	return execHaiku(ctx, systemPrompt, content)
}

// ─── ContainerRuntime ─────────────────────────────────────────────────────────

// ContainerRuntime is the gVisor-sandboxed implementation.
// M4 (runtime_container.go) implements InvokeFFmpeg, InvokeWhisperTranscribe,
// and InvokeClaudeSubprocess using the containerd v2 client + gVisor runsc.
// M5 adds mount path resolution and stdin/stdout piping.
// M6 adds timeout policy and native exec fallback.
type ContainerRuntime struct {
	cfg SandboxConfig
}
