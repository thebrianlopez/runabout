package main

// EPIC-038 M6: Timeout propagation, native exec fallback, graceful shutdown.
//
// Implements:
//   - ContainerTimeoutPolicy: SIGTERM → grace period → SIGKILL shutdown sequence
//   - ContainerRuntime.Ping(): CRI socket reachability health check used at startup
//   - NewExecutionRuntimeWithPing: factory that falls back to LocalRuntime when the
//     CRI socket is unreachable, logging "runtime_unavailable" for ops visibility
//
// The timeout policy is applied in containerRunWithIO. When the context fires:
//   1. SIGTERM is sent to the task process
//   2. We wait up to GracePeriod for the task to exit cleanly
//   3. SIGKILL is sent unconditionally after the grace window

import (
	"context"
	"fmt"
	"log/slog"
	"syscall"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
)

// ContainerTimeoutPolicy describes how a container is stopped on context expiry.
type ContainerTimeoutPolicy struct {
	// GracePeriod is how long to wait after SIGTERM before issuing SIGKILL.
	// Zero means skip SIGTERM and SIGKILL immediately (hard kill).
	GracePeriod time.Duration
}

// DefaultTimeoutPolicy is the production-recommended policy: SIGTERM first,
// then SIGKILL after 5 seconds if the process has not exited.
var DefaultTimeoutPolicy = ContainerTimeoutPolicy{GracePeriod: 5 * time.Second}

// killGraceful sends SIGTERM to the task, waits up to p.GracePeriod for the
// process to exit via waitC, then sends SIGKILL if still running.
// If GracePeriod is zero, SIGKILL is sent immediately.
//
// waitC must be the channel returned by task.Wait before task.Start.
func (p ContainerTimeoutPolicy) killGraceful(task containerd.Task, waitC <-chan containerd.ExitStatus) {
	if p.GracePeriod <= 0 {
		_ = task.Kill(context.Background(), syscall.SIGKILL)
		return
	}

	_ = task.Kill(context.Background(), syscall.SIGTERM)
	select {
	case <-waitC:
		// Exited cleanly after SIGTERM.
		return
	case <-time.After(p.GracePeriod):
		_ = task.Kill(context.Background(), syscall.SIGKILL)
	}
}

// Ping verifies that the CRI socket is reachable and that containerd is alive.
// Returns nil if the socket is accessible and the containerd server responds.
// Used by NewExecutionRuntimeWithPing to gate the ContainerRuntime selection.
func (r *ContainerRuntime) Ping(ctx context.Context) error {
	client, err := containerd.New(r.cfg.runtimeSocket())
	if err != nil {
		return fmt.Errorf("containerd connect %s: %w", r.cfg.runtimeSocket(), err)
	}
	defer client.Close()

	// Verify the server is live by listing namespaces — lightweight RPC with no
	// side-effects. WithNamespace is required by the containerd client; use the
	// linkari namespace to stay consistent with normal operations.
	ctrdCtx := namespaces.WithNamespace(ctx, linkariNamespace)
	if _, err := client.Version(ctrdCtx); err != nil {
		return fmt.Errorf("containerd ping: %w", err)
	}
	return nil
}

// NewExecutionRuntimeWithPing constructs the appropriate ExecutionRuntime for
// the given SandboxConfig, probing the CRI socket when sandbox.enabled is true.
//
// If the probe succeeds, a HybridRuntime is returned (ffmpeg and whisper
// sandboxed via ContainerRuntime; claude CLI always local — see HybridRuntime).
// If the probe fails, NewExecutionRuntimeWithPing logs "runtime_unavailable" and
// falls back to LocalRuntime so callers continue to work without container deps.
// When sandbox.enabled is false, LocalRuntime is returned without any probe.
func NewExecutionRuntimeWithPing(ctx context.Context, cfg SandboxConfig) ExecutionRuntime {
	if !cfg.Enabled {
		return &LocalRuntime{}
	}
	cr := &ContainerRuntime{cfg: cfg}
	if err := cr.Ping(ctx); err != nil {
		slog.Warn(
			"container runtime unavailable — falling back to local exec",
			"event_type", "runtime_unavailable",
			"socket", cfg.runtimeSocket(),
			"error", err,
		)
		return &LocalRuntime{}
	}
	slog.Info(
		"container runtime ready",
		"event_type", "runtime_ready",
		"socket", cfg.runtimeSocket(),
	)
	return &HybridRuntime{container: cr}
}
