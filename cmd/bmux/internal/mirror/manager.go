package mirror

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/google/uuid"
)

//go:generate sh js/build.sh

// Options configures the HeadlessMirrorManager.
type Options struct {
	// IdleTimeout is the duration of inactivity before a pane mirror is destroyed.
	// Defaults to 1 hour if zero.
	IdleTimeout time.Duration
}

// manager is the concrete HeadlessMirrorManager implementation.
type manager struct {
	opts    Options
	nodeBin string

	mu            sync.Mutex
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	cancelRestart context.CancelFunc

	pending      map[string]chan ipcResponse
	pendingMu    sync.Mutex

	panes   map[string]*paneState
	closed  bool
	restartCount int
}

type paneState struct {
	idleTimer *time.Timer
	lastWrite time.Time
}

const maxPendingWrites = 1000
const snapshotTimeout = 500 * time.Millisecond

// NewHeadlessMirrorManager creates and starts a new HeadlessMirrorManager.
// Returns E01 node_not_found if node is absent from PATH.
func NewHeadlessMirrorManager(opts Options) (HeadlessMirrorManager, error) {
	return newManagerWithNodePath("node", opts)
}

func newManagerWithNodePath(nodeBin string, opts Options) (HeadlessMirrorManager, error) {
	if _, err := exec.LookPath(nodeBin); err != nil {
		return nil, &MirrorError{
			Code:    ErrCodeNodeNotFound,
			Message: "bmux: node not found — xterm mirror requires Node.js ≥18",
		}
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = time.Hour
	}
	m := &manager{
		opts:    opts,
		nodeBin: nodeBin,
		pending: make(map[string]chan ipcResponse),
		panes:   make(map[string]*paneState),
	}
	if err := m.start(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *manager) start() error {
	bundle, err := writeBundleToTemp()
	if err != nil {
		return fmt.Errorf("write mirror bundle: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, m.nodeBin, bundle)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return &MirrorError{
			Code:    ErrCodeNodeNotFound,
			Message: fmt.Sprintf("failed to start node subprocess: %v", err),
		}
	}

	m.mu.Lock()
	m.cmd = cmd
	m.stdin = stdin
	m.cancelRestart = cancel
	m.mu.Unlock()

	slog.Info("mirror_subprocess_started",
		"pid", cmd.Process.Pid,
	)

	go m.readResponses(stdout)
	go m.watchSubprocess(cmd, bundle)

	return nil
}

func writeBundleToTemp() (string, error) {
	f, err := os.CreateTemp("", "xterm-mirror-*.js")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(mirrorBundle); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func (m *manager) watchSubprocess(cmd *exec.Cmd, bundlePath string) {
	err := cmd.Wait()

	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()

	if closed {
		_ = os.Remove(bundlePath)
		return
	}

	exitCode := -1
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	slog.Error("mirror_subprocess_crashed", "exit_code", exitCode)

	// Auto-restart.
	m.mu.Lock()
	m.restartCount++
	rc := m.restartCount
	m.mu.Unlock()

	slog.Warn("mirror_subprocess_restarted", "restart_count", rc)

	// Reject all pending snapshot requests.
	m.pendingMu.Lock()
	for id, ch := range m.pending {
		ch <- ipcResponse{ID: id, OK: false, Error: ErrCodeSubprocessCrashed}
		delete(m.pending, id)
	}
	m.pendingMu.Unlock()

	_ = os.Remove(bundlePath)

	if err := m.start(); err != nil {
		slog.Error("mirror_subprocess_restart_failed", "error", err)
		return
	}

	// Re-initialize all active pane mirrors with a blank write.
	m.mu.Lock()
	panes := make([]string, 0, len(m.panes))
	for id := range m.panes {
		panes = append(panes, id)
	}
	m.mu.Unlock()

	for _, paneID := range panes {
		_ = m.Write(paneID, []byte{})
	}
}

func (m *manager) readResponses(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		resp, err := unmarshalResponse(line)
		if err != nil {
			slog.Error("mirror_ipc_parse_error", "line", string(line), "error", err)
			continue
		}
		m.pendingMu.Lock()
		ch, ok := m.pending[resp.ID]
		if ok {
			delete(m.pending, resp.ID)
		}
		m.pendingMu.Unlock()
		if ok {
			ch <- resp
		}
	}
}

func (m *manager) sendRequest(req ipcRequest) error {
	b, err := marshalRequest(req)
	if err != nil {
		return err
	}
	m.mu.Lock()
	w := m.stdin
	m.mu.Unlock()
	if w == nil {
		return &MirrorError{Code: ErrCodeSubprocessCrashed, Message: "subprocess not running"}
	}
	_, err = w.Write(b)
	return err
}

func (m *manager) sendAndWait(req ipcRequest) (ipcResponse, error) {
	ch := make(chan ipcResponse, 1)
	m.pendingMu.Lock()
	m.pending[req.ID] = ch
	m.pendingMu.Unlock()

	if err := m.sendRequest(req); err != nil {
		m.pendingMu.Lock()
		delete(m.pending, req.ID)
		m.pendingMu.Unlock()
		return ipcResponse{}, err
	}

	select {
	case resp := <-ch:
		if !resp.OK {
			return ipcResponse{}, &MirrorError{
				Code:    ErrCodeSubprocessCrashed,
				Message: resp.Error,
			}
		}
		return resp, nil
	case <-time.After(snapshotTimeout):
		m.pendingMu.Lock()
		delete(m.pending, req.ID)
		m.pendingMu.Unlock()
		slog.Warn("mirror_snapshot_timeout", "pane_id", req.Pane, "timeout_ms", snapshotTimeout.Milliseconds())
		return ipcResponse{}, &MirrorError{
			Code:    ErrCodeSnapshotTimeout,
			Message: fmt.Sprintf("snapshot timeout for pane %s after %v", req.Pane, snapshotTimeout),
		}
	}
}

// Write feeds %output bytes into the pane's headless VT instance.
func (m *manager) Write(paneID string, data []byte) error {
	slog.Debug("mirror_write", "pane_id", paneID, "data_bytes", len(data))

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return &MirrorError{Code: ErrCodeSubprocessCrashed, Message: "manager closed"}
	}
	ps, ok := m.panes[paneID]
	if !ok {
		ps = &paneState{}
		m.panes[paneID] = ps
	}
	ps.lastWrite = time.Now()

	// Reset idle timer.
	if ps.idleTimer != nil {
		ps.idleTimer.Reset(m.opts.IdleTimeout)
	} else {
		ps.idleTimer = time.AfterFunc(m.opts.IdleTimeout, func() {
			idleStart := time.Now()
			_ = m.Destroy(paneID)
			slog.Info("mirror_idle_destroyed",
				"pane_id", paneID,
				"idle_sec", time.Since(idleStart).Seconds())
		})
	}
	m.mu.Unlock()

	encoded := base64.StdEncoding.EncodeToString(data)
	req := ipcRequest{
		ID:   newID(),
		Op:   "write",
		Pane: paneID,
		Data: encoded,
	}
	// Fire-and-forget: send request without waiting for ack.
	return m.sendRequest(req)
}

// Snapshot serializes the current grid state as ANSI escape sequences.
func (m *manager) Snapshot(paneID string, cols, rows int) ([]byte, error) {
	start := time.Now()
	defer func() {
		slog.Debug("mirror_snapshot",
			"pane_id", paneID,
			"cols", cols,
			"rows", rows,
			"duration_ms", time.Since(start).Milliseconds())
	}()

	req := ipcRequest{
		ID:   newID(),
		Op:   "snapshot",
		Pane: paneID,
		Cols: cols,
		Rows: rows,
	}

	resp, err := m.sendAndWait(req)
	if err != nil {
		return nil, err
	}

	if resp.Data == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(resp.Data)
	if err != nil {
		return nil, &MirrorError{
			Code:    ErrCodeIPCParseError,
			Message: fmt.Sprintf("failed to decode snapshot data: %v", err),
		}
	}
	return decoded, nil
}

// Resize updates the headless terminal dimensions.
func (m *manager) Resize(paneID string, cols, rows int) error {
	req := ipcRequest{
		ID:   newID(),
		Op:   "resize",
		Pane: paneID,
		Cols: cols,
		Rows: rows,
	}
	return m.sendRequest(req)
}

// Destroy releases the headless VT instance for paneID.
func (m *manager) Destroy(paneID string) error {
	m.mu.Lock()
	ps, ok := m.panes[paneID]
	if ok {
		if ps.idleTimer != nil {
			ps.idleTimer.Stop()
		}
		delete(m.panes, paneID)
	}
	m.mu.Unlock()

	if !ok {
		return nil
	}

	req := ipcRequest{
		ID:   newID(),
		Op:   "destroy",
		Pane: paneID,
	}
	return m.sendRequest(req)
}

// ActivePanes returns the list of pane IDs with live mirrors.
func (m *manager) ActivePanes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	panes := make([]string, 0, len(m.panes))
	for id := range m.panes {
		panes = append(panes, id)
	}
	return panes
}

// Close shuts down the subprocess and releases all resources.
func (m *manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	cancel := m.cancelRestart
	stdin := m.stdin
	for _, ps := range m.panes {
		if ps.idleTimer != nil {
			ps.idleTimer.Stop()
		}
	}
	m.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if cancel != nil {
		cancel()
	}
	return nil
}

func newID() string {
	id, err := uuid.NewRandom()
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return id.String()
}

