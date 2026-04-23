package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/blo-grindr/bmux/internal/config"
)

const (
	defaultReadyTimeout = 5 * time.Second
	defaultStopTimeout  = 5 * time.Second
	readyPollInterval   = 100 * time.Millisecond
)

// manager is the concrete DaemonManager implementation.
type manager struct {
	paths        *config.Paths
	readyTimeout time.Duration
	stopTimeout  time.Duration
	newServeCmd  func(configPath string) *exec.Cmd
}

// NewManager creates a DaemonManager backed by the given Paths.
func NewManager(paths *config.Paths) DaemonManager {
	return &manager{
		paths:        paths,
		readyTimeout: defaultReadyTimeout,
		stopTimeout:  defaultStopTimeout,
		newServeCmd:  defaultServeCmd,
	}
}

// newManagerWithOpts is the testable constructor.
func newManagerWithOpts(paths *config.Paths, readyTimeout, stopTimeout time.Duration, newCmd func(string) *exec.Cmd) *manager {
	return &manager{
		paths:        paths,
		readyTimeout: readyTimeout,
		stopTimeout:  stopTimeout,
		newServeCmd:  newCmd,
	}
}

func defaultServeCmd(configPath string) *exec.Cmd {
	exe, _ := os.Executable()
	return exec.Command(exe, "serve", "--config", configPath)
}

// Start spawns bmux serve, writes the PID file, and waits for the ready
// sentinel to appear.
func (m *manager) Start(ctx context.Context, configPath string) error {
	pidPath := m.paths.PIDFile()

	// Check for an already-running daemon.
	wasStale, stalePID, err := cleanStalePID(pidPath)
	if err != nil {
		return fmt.Errorf("check pid file: %w", err)
	}
	if wasStale {
		// Stale PID cleaned — log warning (caller logs this via error code E22).
		_ = stalePID
	}
	// Re-read after stale cleanup.
	pid, err := readPID(pidPath)
	if err != nil {
		return fmt.Errorf("read pid file: %w", err)
	}
	if pid > 0 && isAlive(pid) {
		return errAlreadyRunning(pid)
	}

	// Ensure state and cache directories exist.
	if err := os.MkdirAll(m.paths.StateHome(), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := os.MkdirAll(m.paths.CacheHome(), 0o700); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	// Open log file (append).
	logPath := m.paths.LogFile()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer logFile.Close()

	// Spawn the serve process with stdout/stderr → log file.
	cmd := m.newServeCmd(configPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// SysProcAttr: detach from parent process group so it survives shell exit.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn bmux serve: %w", err)
	}

	// Write PID file immediately.
	if err := writePID(pidPath, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("write pid file: %w", err)
	}

	// Wait for the ready sentinel.
	readyPath := m.paths.ReadyFile()
	deadline := time.Now().Add(m.readyTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(readyPath); err == nil {
			return nil // ready!
		}
		// Check if the process died early.
		if !isAlive(cmd.Process.Pid) {
			_ = removePID(pidPath)
			return errStartTimeout(logPath)
		}
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = removePID(pidPath)
			return ctx.Err()
		case <-time.After(readyPollInterval):
		}
	}

	// Timeout: kill the process and report.
	_ = cmd.Process.Kill()
	_ = removePID(pidPath)
	return errStartTimeout(logPath)
}

// Stop sends SIGTERM to the daemon and waits for it to exit.
// Escalates to SIGKILL after stopTimeout.
func (m *manager) Stop(ctx context.Context) error {
	pidPath := m.paths.PIDFile()
	pid, err := readPID(pidPath)
	if err != nil {
		return fmt.Errorf("read pid file: %w", err)
	}
	if pid == 0 || !isAlive(pid) {
		_ = removePID(pidPath) // clean any stale file
		return errNotRunning()
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return errNotRunning()
	}

	// Send SIGTERM.
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM to pid %d: %w", pid, err)
	}

	// Wait for exit.
	deadline := time.Now().Add(m.stopTimeout)
	for time.Now().Before(deadline) {
		if !isAlive(pid) {
			// Process exited cleanly.
			_ = removePID(pidPath)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyPollInterval):
		}
	}

	// Timeout: escalate to SIGKILL (E24 warning — caller logs).
	_ = proc.Signal(syscall.SIGKILL)
	// Give it a moment to die, then clean up.
	time.Sleep(200 * time.Millisecond)
	_ = removePID(pidPath)
	return nil
}

// Status reads the current daemon state from status.json.
func (m *manager) Status(_ context.Context) (*DaemonStatus, error) {
	pid, err := readPID(m.paths.PIDFile())
	if err != nil {
		return nil, fmt.Errorf("read pid file: %w", err)
	}
	if pid == 0 || !isAlive(pid) {
		return nil, errNotRunning()
	}
	return ReadStatus(m.paths.StatusFile())
}

// IsRunning returns whether the daemon process is alive.
func (m *manager) IsRunning() (bool, int, error) {
	pid, err := readPID(m.paths.PIDFile())
	if err != nil {
		return false, 0, err
	}
	if pid == 0 {
		return false, 0, nil
	}
	return isAlive(pid), pid, nil
}

// WriteReady creates the ready sentinel file at the paths' ReadyFile location.
// Called by the serve process once initialization is complete.
func WriteReady(paths *config.Paths) error {
	return os.WriteFile(paths.ReadyFile(), []byte("ready\n"), 0o600)
}

// RemoveReady deletes the ready sentinel file. Called on daemon shutdown.
func RemoveReady(paths *config.Paths) {
	_ = os.Remove(paths.ReadyFile())
}

// WriteSelfPID writes the current process's PID to the PID file.
// Called by the serve process at startup.
func WriteSelfPID(paths *config.Paths) error {
	return writePID(paths.PIDFile(), os.Getpid())
}

// must returns v; panics on non-nil err. Used for errors that should be
// impossible (e.g. marshaling a known-good struct).
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// Discard provides an io.Writer that discards all output (used in tests).
var Discard io.Writer = io.Discard
