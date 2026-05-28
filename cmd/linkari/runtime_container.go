package main

// EPIC-038 M4: CRI client + container lifecycle management.
//
// Implements ContainerRuntime.run — the core container execution primitive that
// all three InvokeXxx methods delegate to. Uses the containerd v2 Go client
// to start/stop/wait containers under the gVisor runsc runtime.
//
// M5 adds mount path resolution and stdin/stdout piping across the container
// boundary. For M4, stdin is devnull, stdout/stderr are captured into a buffer.
//
// Container lifecycle per invocation:
//   connect → pull-if-absent → snapshot → create → task.Start →
//   task.Wait → read output → task.Delete → container.Delete

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"syscall"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
)

// linkariNamespace is the containerd namespace for all linkari container ops.
// Isolates linkari containers from other containerd tenants on the same socket.
const linkariNamespace = "linkari"

// gvisorRuntime is the containerd runtime ID for gVisor runsc.
// Matches the RuntimeClass registered in infra/lima-gvisor.yaml.
const gvisorRuntime = "io.containerd.runsc.v1"

// containerRunResult holds the output of a completed container invocation.
type containerRunResult struct {
	Stdout   string
	Stderr   string
	ExitCode uint32
}

// containerRun is the core container execution primitive. It:
//  1. Opens a containerd client on the CRI socket
//  2. Pulls the image if not already cached
//  3. Creates an OCI container with the supplied args, env, and resource limits
//  4. Starts the container task and waits for exit
//  5. Returns captured stdout/stderr and the exit code
//
// All I/O paths and bind mounts are wired in M5. For M4, stdin is /dev/null
// and stdout/stderr are captured from the task's stdio pipes.
func (r *ContainerRuntime) containerRun(ctx context.Context, image string, args []string, env []string) (*containerRunResult, error) {
	// Open containerd client. The socket is typically accessed inside the Lima
	// or OrbStack VM — the caller is responsible for ensuring the CRI socket
	// is reachable from the process's perspective.
	client, err := containerd.New(r.cfg.runtimeSocket())
	if err != nil {
		return nil, fmt.Errorf("containerd connect %s: %w", r.cfg.runtimeSocket(), err)
	}
	defer client.Close()

	ctrdCtx := namespaces.WithNamespace(ctx, linkariNamespace)

	// Pull image if not present in the local content store.
	slog.Debug("container_runtime: pulling image", "image", image)
	img, err := client.Pull(
		ctrdCtx, image,
		containerd.WithPullUnpack,
	)
	if err != nil {
		return nil, fmt.Errorf("container pull %s: %w", image, err)
	}

	// Unique container ID: linkari-<image-leaf>-<nanoseconds>.
	leaf := imageLeaf(image)
	containerID := fmt.Sprintf("linkari-%s-%d", leaf, time.Now().UnixNano())

	// Build OCI spec. M5 adds WithMounts for /linkari/io bind mounts.
	specOpts := []oci.SpecOpts{
		oci.WithProcessArgs(args...),
		oci.WithEnv(env),
		oci.WithDefaultPathEnv,
	}
	if r.cfg.MemoryLimitMB > 0 {
		memBytes := int64(r.cfg.MemoryLimitMB) << 20
		specOpts = append(specOpts, oci.WithMemoryLimit(uint64(memBytes)))
	}
	// CPU quota via CFS: cpus × 100000 µs per 100000 µs period.
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
		// Best-effort cleanup; ignore errors on context cancellation.
		_ = container.Delete(context.Background(), containerd.WithSnapshotCleanup)
	}()

	// Capture stdout/stderr. M5 replaces this with piped I/O for whisper/claude.
	var stdout, stderr bytes.Buffer
	task, err := container.NewTask(ctrdCtx, cio.NewCreator(
		cio.WithStreams(io.NopCloser(strings.NewReader("")), &stdout, &stderr),
	))
	if err != nil {
		return nil, fmt.Errorf("container task create %s: %w", containerID, err)
	}
	defer func() {
		_, _ = task.Delete(context.Background(), containerd.WithProcessKill)
	}()

	// Register exit channel before Start to avoid a race where the process
	// exits before Wait is called.
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

	// Wait for completion or context cancellation.
	select {
	case status := <-waitC:
		if err := status.Error(); err != nil {
			return nil, fmt.Errorf("container wait %s: %w", containerID, err)
		}
		result := &containerRunResult{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			ExitCode: status.ExitCode(),
		}
		slog.Info(
			"container_runtime: task exited",
			"event_type", "container_exit",
			"container_id", containerID,
			"exit_code", result.ExitCode,
			"stdout_len", len(result.Stdout),
		)
		if result.ExitCode != 0 {
			return result, fmt.Errorf("container %s: exit %d (stderr: %s)",
				containerID, result.ExitCode, trimOutput(result.Stderr, 512))
		}
		return result, nil

	case <-ctx.Done():
		// Context expired — kill the task. M6 adds the SIGTERM→grace→SIGKILL policy.
		_ = task.Kill(context.Background(), syscall.SIGKILL)
		return nil, fmt.Errorf("container %s: %w", containerID, ctx.Err())
	}
}

// imageLeaf returns the rightmost path component before the tag, used to
// build human-readable container IDs. "ghcr.io/blo/linkari/ffmpeg:latest" → "ffmpeg".
func imageLeaf(image string) string {
	// Strip tag.
	if i := strings.LastIndex(image, ":"); i > 0 {
		image = image[:i]
	}
	// Last path segment.
	if i := strings.LastIndex(image, "/"); i >= 0 {
		image = image[i+1:]
	}
	return image
}

// trimOutput returns the last n bytes of s, prefixed with "..." if truncated.
func trimOutput(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// InvokeFFmpeg and InvokeWhisperTranscribe are implemented in runtime_io.go
// (M5) using containerRunWithIO + bind mounts.
