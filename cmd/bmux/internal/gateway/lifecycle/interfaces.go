// Package lifecycle manages the ordered startup and shutdown of all gateway
// subsystems (F1 ControlModeBridge, F2 HeadlessMirrorManager, F4 SessionRegistry,
// F3 WebSocket Gateway). It is the single entry point for the daemon to start/stop
// the mobile gateway stack.
package lifecycle

import "context"

// ControlModeBridge is the F1 interface (stub until F1 is implemented).
type ControlModeBridge interface {
	Start(ctx context.Context) error
	Stop()
}

// SessionRegistry is the subset of the F4 interface used for lifecycle.
type SessionRegistry interface {
	Start(ctx context.Context) error
	Stop()
	ActivePanes() []string
}

// MirrorCloser is the subset of F2 HeadlessMirrorManager used for lifecycle.
type MirrorCloser interface {
	Close() error
	ActivePanes() []string
}

// GatewayRunner is the subset of the F3 Gateway interface used for lifecycle.
type GatewayRunner interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	ClientCount() int
	Addr() string
}

// GatewayManager coordinates start/stop of all gateway subsystems.
type GatewayManager interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Status() GatewayStatus
}

// GatewayStatus is the aggregated runtime state of the gateway stack.
type GatewayStatus struct {
	Running     bool
	Addr        string
	ClientCount int
	BridgeState string
	ActivePanes []string
}

// Deps bundles all subsystem dependencies for GatewayManager.
type Deps struct {
	Bridge   ControlModeBridge
	Registry SessionRegistry
	Mirror   MirrorCloser
	Gateway  GatewayRunner
}
