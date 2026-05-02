package lifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/blo-grindr/bmux/internal/config"
)

// manager is the concrete GatewayManager implementation.
type manager struct {
	cfg     config.GatewayConfig
	deps    Deps
	mu      sync.Mutex
	running bool
	startAt time.Time
}

// New creates a GatewayManager. When cfg.Enabled is false, Start/Stop are no-ops.
func New(cfg config.GatewayConfig, deps Deps) GatewayManager {
	return &manager{cfg: cfg, deps: deps}
}

// Start boots the subsystems in required order: Bridge → Registry → Gateway.
// Mirror (F2) does not require an explicit Start; it is started on first Write.
// If any subsystem fails, the error is logged and the remaining starts are skipped,
// but the SSH bridge (Phase 1) is never affected.
func (m *manager) Start(ctx context.Context) error {
	if !m.cfg.Enabled {
		slog.Debug("gateway_disabled", "reason", "gateway.enabled=false")
		return nil
	}

	// 1. F1 ControlModeBridge
	slog.Debug("gateway_subsystem_starting", "subsystem", "bridge")
	if err := m.deps.Bridge.Start(ctx); err != nil {
		slog.Error("gateway_subsystem_failed", "subsystem", "bridge", "error", err)
		return fmt.Errorf("gateway: bridge start: %w", err)
	}

	// 2. F4 SessionRegistry
	slog.Debug("gateway_subsystem_starting", "subsystem", "registry")
	if err := m.deps.Registry.Start(ctx); err != nil {
		slog.Error("gateway_subsystem_failed", "subsystem", "registry", "error", err)
		// Non-fatal: stop bridge, return error.
		m.deps.Bridge.Stop()
		return fmt.Errorf("gateway: registry start: %w", err)
	}

	// 3. F3 WebSocket Gateway
	slog.Debug("gateway_subsystem_starting", "subsystem", "gateway")
	if err := m.deps.Gateway.Start(ctx); err != nil {
		slog.Error("gateway_subsystem_failed", "subsystem", "gateway", "error", err)
		m.deps.Registry.Stop()
		m.deps.Mirror.Close()
		m.deps.Bridge.Stop()
		return fmt.Errorf("gateway: ws start: %w", err)
	}

	addr := m.deps.Gateway.Addr()
	slog.Info("gateway_started", "addr", addr)

	// Emit LAN binding warning if host is 0.0.0.0.
	if warn := CheckLANBinding(m.cfg.Host); warn != "" {
		slog.Warn("gateway_lan_binding", "host", m.cfg.Host, "warning", warn)
	}

	m.mu.Lock()
	m.running = true
	m.startAt = time.Now()
	m.mu.Unlock()

	return nil
}

// Stop shuts down subsystems in reverse order: Gateway → Registry → Mirror → Bridge.
// All WebSocket connections receive close frames before Stop returns (delegated to F3).
func (m *manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return nil
	}
	uptime := time.Since(m.startAt)
	m.running = false
	m.mu.Unlock()

	// 1. F3 Gateway — closes all WS connections first.
	slog.Debug("gateway_subsystem_stopping", "subsystem", "gateway")
	if err := m.deps.Gateway.Stop(ctx); err != nil {
		slog.Warn("gateway_stop_error", "subsystem", "gateway", "error", err)
	}

	// 2. F4 SessionRegistry
	slog.Debug("gateway_subsystem_stopping", "subsystem", "registry")
	m.deps.Registry.Stop()

	// 3. F2 HeadlessMirrorManager
	slog.Debug("gateway_subsystem_stopping", "subsystem", "mirror")
	if err := m.deps.Mirror.Close(); err != nil {
		slog.Warn("gateway_stop_error", "subsystem", "mirror", "error", err)
	}

	// 4. F1 ControlModeBridge
	slog.Debug("gateway_subsystem_stopping", "subsystem", "bridge")
	m.deps.Bridge.Stop()

	slog.Info("gateway_stopped", "uptime_sec", int(uptime.Seconds()))
	return nil
}

// Status returns the current gateway stack state.
func (m *manager) Status() GatewayStatus {
	m.mu.Lock()
	running := m.running
	m.mu.Unlock()

	if !running {
		return GatewayStatus{Running: false}
	}

	panes := m.deps.Mirror.ActivePanes()
	if panes == nil {
		panes = []string{}
	}

	return GatewayStatus{
		Running:     true,
		Addr:        m.deps.Gateway.Addr(),
		ClientCount: m.deps.Gateway.ClientCount(),
		BridgeState: "running",
		ActivePanes: panes,
	}
}

// CheckConfigPerms returns a non-empty warning string if path is world-readable.
// It never returns an error — a world-readable config is a warning, not a fatal.
func CheckConfigPerms(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	mode := info.Mode().Perm()
	// 0o004 = world-read bit
	if mode&0o004 != 0 {
		return fmt.Sprintf("config file %s is world-readable (mode %04o) — consider chmod 600", path, mode)
	}
	return ""
}

// CheckLANBinding returns a non-empty warning string if host is 0.0.0.0.
func CheckLANBinding(host string) string {
	if host == "0.0.0.0" {
		return "gateway is bound to 0.0.0.0 — accessible on all interfaces; ensure Tailscale firewall is active"
	}
	return ""
}
