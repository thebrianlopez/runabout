package main

// EPIC-038 M5: Path resolution + stdin/stdout piping across container boundary.
//
// This file implements:
//   - ProcessIO: interface for stdin/stdout marshalling into/out of containers
//   - sandboxPaths: canonical mount-point resolver for model/transcript/profile dirs
//   - ioResolver: resolves host paths → container paths and creates bind mounts
//   - containerRunWithIO: extends containerRun with real bind mounts + ProcessIO
//
// Host path → container path mapping:
//
//   Host (resolved from env)          Container (fixed)
//   ─────────────────────────────     ─────────────────
//   $LINKARI_MODEL_DIR or default     /models         (ro)
//   $LINKARI_PROFILE_PATH or default  /profiles       (ro)
//   per-invocation tmpdir             /linkari/io     (rw, tmpfs)
//
// ContainerRuntime.InvokeFFmpeg and InvokeWhisperTranscribe
// are updated in this file to use containerRunWithIO.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// containerMountMode controls read/write permissions on a bind mount.
type containerMountMode int

const (
	mountRO containerMountMode = iota
	mountRW
)

// containerMount describes a single bind mount for a container invocation.
type containerMount struct {
	HostPath      string             // absolute path on the host (or inside Lima VM)
	ContainerPath string             // absolute path inside the container
	Mode          containerMountMode // ro or rw
}

// ProcessIO defines how data flows across the container boundary.
// For ffmpeg and whisper the files are passed via bind mounts; stdin/stdout
// are used for the claude subprocess where the content is piped inline.
type ProcessIO struct {
	// Stdin is the reader connected to the container's stdin. Nil = /dev/null.
	Stdin io.Reader
	// Stdout captures the container's stdout. Nil = discard.
	Stdout io.Writer
	// Stderr captures the container's stderr. Nil = discard.
	Stderr io.Writer
}

// sandboxPaths resolves the host-side directories that will be bind-mounted
// into containers. Env vars override defaults; all paths must exist or the
// invocation fails.
type sandboxPaths struct {
	ModelDir   string // $LINKARI_MODEL_DIR or ~/.local/share/whisper
	ProfileDir string // $LINKARI_PROFILE_PATH or ~/.config/linkari/profiles
	IODir      string // per-invocation tmpdir mounted at /linkari/io
}

// resolveSandboxPaths builds sandboxPaths for a container invocation.
// ioDir is the caller-provided per-invocation tmpdir (or "" to allocate one).
func resolveSandboxPaths(ioDir string) (*sandboxPaths, error) {
	home, _ := os.UserHomeDir()

	modelDir := os.Getenv("LINKARI_MODEL_DIR")
	if modelDir == "" {
		modelDir = filepath.Join(home, ".local", "share", "whisper")
	}

	profileDir := os.Getenv("LINKARI_PROFILE_PATH")
	if profileDir == "" {
		profileDir = filepath.Join(home, ".config", "linkari", "profiles")
	}

	if ioDir == "" {
		var err error
		ioDir, err = os.MkdirTemp("", "linkari-io-*")
		if err != nil {
			return nil, fmt.Errorf("sandbox io tmpdir: %w", err)
		}
	}

	return &sandboxPaths{
		ModelDir:   modelDir,
		ProfileDir: profileDir,
		IODir:      ioDir,
	}, nil
}

// mounts returns the bind mount list for a containerised invocation.
// Models and profiles are read-only; the I/O dir is read-write.
func (p *sandboxPaths) mounts() []containerMount {
	return []containerMount{
		{HostPath: p.ModelDir, ContainerPath: "/models", Mode: mountRO},
		{HostPath: p.ProfileDir, ContainerPath: "/profiles", Mode: mountRO},
		{HostPath: p.IODir, ContainerPath: "/linkari/io", Mode: mountRW},
	}
}

// remapPath remaps a host-side absolute path to its container-side equivalent.
// Returns (containerPath, true) if the path is under a known host directory,
// or (original, false) if no remapping applies.
func (p *sandboxPaths) remapPath(hostPath string) (string, bool) {
	mappings := []struct{ host, container string }{
		{p.ModelDir, "/models"},
		{p.ProfileDir, "/profiles"},
		{p.IODir, "/linkari/io"},
	}
	for _, m := range mappings {
		if rel, err := filepath.Rel(m.host, hostPath); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join(m.container, rel), true
		}
	}
	return hostPath, false
}

// containerRunWithIO extends containerRun with bind mounts, ProcessIO, and
// network policy. policy controls whether the container gets an isolated net
// namespace (PolicyNone) or shares the host network (PolicyHost).
func (r *ContainerRuntime) containerRunWithIO(
	ctx context.Context,
	image string,
	args []string,
	env []string,
	mounts []containerMount,
	pio ProcessIO,
	policy ContainerNetworkPolicy,
) (*containerRunResult, error) {
	client, err := containerd.New(r.cfg.runtimeSocket())
	if err != nil {
		return nil, fmt.Errorf("containerd connect %s: %w", r.cfg.runtimeSocket(), err)
	}
	defer client.Close()

	ctrdCtx := namespaces.WithNamespace(ctx, linkariNamespace)

	img, err := client.Pull(ctrdCtx, image, containerd.WithPullUnpack)
	if err != nil {
		return nil, fmt.Errorf("container pull %s: %w", image, err)
	}

	containerID := fmt.Sprintf("linkari-%s-%d", imageLeaf(image), monotonicID())

	// Convert containerMount list to OCI mount specs.
	var ociMounts []specs.Mount
	for _, m := range mounts {
		opts := []string{"bind"}
		if m.Mode == mountRO {
			opts = append(opts, "ro")
		} else {
			opts = append(opts, "rw")
		}
		ociMounts = append(ociMounts, specs.Mount{
			Destination: m.ContainerPath,
			Source:      m.HostPath,
			Type:        "bind",
			Options:     opts,
		})
	}

	specOpts := []oci.SpecOpts{
		oci.WithProcessArgs(args...),
		oci.WithEnv(env),
		oci.WithDefaultPathEnv,
		oci.WithMounts(ociMounts),
	}
	specOpts = append(specOpts, policy.networkSpecOpts()...)
	if r.cfg.MemoryLimitMB > 0 {
		specOpts = append(specOpts, oci.WithMemoryLimit(uint64(r.cfg.MemoryLimitMB)<<20))
	}
	if r.cfg.CPULimitCores > 0 {
		period := uint64(100_000)
		quota := int64(r.cfg.CPULimitCores * float64(period))
		specOpts = append(specOpts, oci.WithCPUCFS(quota, period))
	}

	container, err := client.NewContainer(
		ctrdCtx, containerID,
		containerd.WithNewSnapshot(containerID, img),
		containerd.WithNewSpec(specOpts...),
		containerd.WithRuntime(gvisorRuntime, nil),
	)
	if err != nil {
		return nil, fmt.Errorf("container create %s: %w", containerID, err)
	}
	defer func() {
		_ = container.Delete(context.Background(), containerd.WithSnapshotCleanup)
	}()

	// Wire ProcessIO.
	stdin := pio.Stdin
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	stdout := pio.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := pio.Stderr
	if stderr == nil {
		stderr = io.Discard
	}

	task, err := container.NewTask(ctrdCtx, cio.NewCreator(
		cio.WithStreams(io.NopCloser(stdin), stdout, stderr),
	))
	if err != nil {
		return nil, fmt.Errorf("container task create %s: %w", containerID, err)
	}
	defer func() {
		_, _ = task.Delete(context.Background(), containerd.WithProcessKill)
	}()

	waitC, err := task.Wait(ctrdCtx)
	if err != nil {
		return nil, fmt.Errorf("container wait register %s: %w", containerID, err)
	}
	if err := task.Start(ctrdCtx); err != nil {
		return nil, fmt.Errorf("container start %s: %w", containerID, err)
	}

	slog.Info(
		"container_runtime: task started",
		"event_type", "container_start",
		"container_id", containerID,
		"image", image,
	)

	select {
	case status := <-waitC:
		if err := status.Error(); err != nil {
			return nil, fmt.Errorf("container wait %s: %w", containerID, err)
		}
		// Capture stdout/stderr from the writer buffers.
		// If the caller passed a *bytes.Buffer we can read it; otherwise truncated.
		result := &containerRunResult{
			ExitCode: status.ExitCode(),
		}
		if buf, ok := stdout.(*strings.Builder); ok {
			result.Stdout = buf.String()
		}
		if buf, ok := stderr.(*strings.Builder); ok {
			result.Stderr = buf.String()
		}

		slog.Info(
			"container_runtime: task exited",
			"event_type", "container_exit",
			"container_id", containerID,
			"exit_code", result.ExitCode,
			"stdout_len", len(result.Stdout),
		)

		// M7: detect OOM kill (exit code 137 = SIGKILL by the kernel OOM killer).
		if result.ExitCode == 137 {
			slog.Warn(
				"container_runtime: OOM kill detected",
				"event_type", "container_oom_kill",
				"container_id", containerID,
				"image", image,
				"memory_limit_mb", r.cfg.MemoryLimitMB,
			)
			return result, fmt.Errorf("container %s: %w (memory_limit_mb=%d)",
				containerID, ErrContainerOOM, r.cfg.MemoryLimitMB)
		}

		if result.ExitCode != 0 {
			return result, fmt.Errorf("container %s: exit %d (stderr: %s)",
				containerID, result.ExitCode, trimOutput(result.Stderr, 512))
		}
		return result, nil
	case <-ctx.Done():
		DefaultTimeoutPolicy.killGraceful(task, waitC)
		return nil, fmt.Errorf("container %s: %w", containerID, ctx.Err())
	}
}

// monotonicID returns a nanosecond timestamp for use in container IDs.
var monotonicID = func() int64 {
	return time.Now().UnixNano()
}

// ─── Updated InvokeXxx using containerRunWithIO ───────────────────────────────

// Note: These methods override the M4 InvokeXxx defined in runtime_container.go.
// They provide bind mounts and proper I/O piping. The compiler enforces that only
// one definition exists per method — we keep both files for clarity and remove
// the M4 implementations below.
//
// The M4 InvokeFFmpeg and InvokeWhisperTranscribe are
// removed from runtime_container.go and replaced here.

// InvokeFFmpeg converts inputPath to outputPath inside a sandboxed container.
// inputPath and outputPath are first written/read via the /linkari/io volume.
func (r *ContainerRuntime) InvokeFFmpeg(ctx context.Context, inputPath, outputPath string) error {
	paths, err := resolveSandboxPaths("")
	if err != nil {
		return err
	}
	defer os.RemoveAll(paths.IODir)

	// Copy input into the io dir so the container can read it.
	ioInput := filepath.Join(paths.IODir, filepath.Base(inputPath))
	if err := copyFile(inputPath, ioInput); err != nil {
		return fmt.Errorf("sandbox: stage input: %w", err)
	}

	containerInput := filepath.Join("/linkari/io", filepath.Base(inputPath))
	containerOutput := "/linkari/io/output.wav"

	var stderr strings.Builder
	_, err = r.containerRunWithIO(
		ctx,
		r.cfg.ImageRegistry+"/ffmpeg:latest",
		[]string{"-i", containerInput, "-ar", "16000", "-ac", "1", "-y", containerOutput},
		nil,
		paths.mounts(),
		ProcessIO{Stderr: &stderr},
		PolicyNone, // ffmpeg needs no network access
	)
	if err != nil {
		return err
	}

	// Copy output back to the caller's expected path.
	ioOutput := filepath.Join(paths.IODir, "output.wav")
	return copyFile(ioOutput, outputPath)
}

// InvokeWhisperTranscribe transcribes wavPath via a sandboxed whisper-cli.
func (r *ContainerRuntime) InvokeWhisperTranscribe(ctx context.Context, wavPath, modelPath string) (string, error) {
	paths, err := resolveSandboxPaths("")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(paths.IODir)

	// Stage WAV into the io dir.
	ioWav := filepath.Join(paths.IODir, filepath.Base(wavPath))
	if err := copyFile(wavPath, ioWav); err != nil {
		return "", fmt.Errorf("sandbox: stage wav: %w", err)
	}

	containerWav := filepath.Join("/linkari/io", filepath.Base(wavPath))
	if modelPath == "" {
		modelPath = "/models/ggml-large-v3-turbo.bin" // baked into image
	}

	var stdout, stderr strings.Builder
	_, err = r.containerRunWithIO(
		ctx,
		r.cfg.ImageRegistry+"/whisper:latest",
		[]string{"--model", modelPath, "--file", containerWav, "--no-timestamps", "--output-txt"},
		nil,
		paths.mounts(),
		ProcessIO{Stdout: &stdout, Stderr: &stderr},
		PolicyNone, // whisper needs no network access
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

// copyFile copies src to dst, creating dst if absent.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
