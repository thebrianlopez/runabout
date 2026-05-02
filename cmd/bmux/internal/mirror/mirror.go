// Package mirror manages per-pane xterm headless VT instances via a singleton
// Node.js subprocess (xterm-mirror.js). Feeds %output bytes from F1 and
// serializes grid state as ANSI escape sequences for client reconnect.
package mirror

import (
	_ "embed"
)

//go:embed mirror_bundle.js
var mirrorBundle []byte

// HeadlessMirrorManager manages per-pane xterm headless VT instances via Node subprocess.
type HeadlessMirrorManager interface {
	// Write feeds %output bytes into the pane's headless VT instance.
	// Creates the mirror if it does not exist. Non-blocking (queued to subprocess).
	Write(paneID string, data []byte) error

	// Snapshot serializes the current grid state of paneID as ANSI escape sequences.
	// Resizes the headless terminal to cols×rows before serializing.
	// Returns ANSI bytes suitable for writing directly to an xterm.js instance.
	Snapshot(paneID string, cols, rows int) ([]byte, error)

	// Resize updates the headless terminal dimensions without serializing.
	Resize(paneID string, cols, rows int) error

	// Destroy releases the headless VT instance for paneID.
	Destroy(paneID string) error

	// ActivePanes returns the list of pane IDs with live mirrors.
	ActivePanes() []string

	// Close shuts down the subprocess and releases all resources.
	Close() error
}

// Error codes for HeadlessMirrorManager operations.
const (
	ErrCodeNodeNotFound          = "node_not_found"
	ErrCodeSubprocessCrashed     = "mirror_subprocess_crashed"
	ErrCodeSnapshotTimeout       = "snapshot_timeout"
	ErrCodeWriteQueueOverflow    = "write_queue_overflow"
	ErrCodeIPCParseError         = "ipc_parse_error"
)

// MirrorError is a typed error from mirror operations.
type MirrorError struct {
	Code    string
	Message string
}

func (e *MirrorError) Error() string { return e.Message }
