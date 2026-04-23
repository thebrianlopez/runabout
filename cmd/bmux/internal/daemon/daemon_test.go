package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blo-grindr/bmux/internal/config"
)

// setupPaths creates isolated XDG directories for a test and returns Paths.
func setupPaths(t *testing.T) *config.Paths {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return config.NewPaths()
}

// readySentinelCmd returns a command that touches the ready sentinel then sleeps.
// The process stays alive so Start() can confirm the PID is live.
func readySentinelCmd(readyPath string) func(string) *exec.Cmd {
	return func(_ string) *exec.Cmd {
		return exec.Command("sh", "-c", fmt.Sprintf("touch %q && sleep 60", readyPath))
	}
}

// neverReadyCmd returns a command that sleeps without ever writing the sentinel.
func neverReadyCmd() func(string) *exec.Cmd {
	return func(_ string) *exec.Cmd {
		return exec.Command("sleep", "60")
	}
}

// ignoreTermCmd returns a command that ignores SIGTERM and sleeps.
func ignoreTermCmd() func(string) *exec.Cmd {
	return func(_ string) *exec.Cmd {
		return exec.Command("sh", "-c", "trap '' TERM; sleep 60")
	}
}

// killProcess kills a process by PID (best effort).
func killProcess(pid int) {
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Signal(syscall.SIGKILL)
	}
}

// deadPID returns a PID that is guaranteed to be dead by spawning a short-lived
// process and waiting for it to exit.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())
	return cmd.ProcessState.Pid()
}

// --- Contract Tests ---

// CT-1: Start writes PID file with the correct PID.
func TestStart_WritesPIDFile(t *testing.T) {
	paths := setupPaths(t)
	require.NoError(t, os.MkdirAll(paths.StateHome(), 0o700))

	m := newManagerWithOpts(paths, 2*time.Second, 1*time.Second, readySentinelCmd(paths.ReadyFile()))

	err := m.Start(context.Background(), "/fake/config.yaml")
	require.NoError(t, err)

	pid, err := readPID(paths.PIDFile())
	require.NoError(t, err)
	require.Greater(t, pid, 0)
	assert.True(t, isAlive(pid), "spawned process should be alive")

	t.Cleanup(func() { killProcess(pid) })
}

// CT-2: Start returns daemon_already_running when a live daemon exists.
func TestStart_AlreadyRunning(t *testing.T) {
	paths := setupPaths(t)
	require.NoError(t, os.MkdirAll(paths.StateHome(), 0o700))

	// Write our own PID as the "running daemon".
	livePID := os.Getpid()
	require.NoError(t, writePID(paths.PIDFile(), livePID))

	m := newManagerWithOpts(paths, 1*time.Second, 1*time.Second, neverReadyCmd())
	_, err := m.Start(context.Background(), "/fake/config.yaml"), fmt.Errorf("placeholder")
	_ = err
	// Call the actual Start.
	startErr := m.Start(context.Background(), "/fake/config.yaml")
	require.Error(t, startErr)
	var de *DaemonError
	require.ErrorAs(t, startErr, &de)
	assert.Equal(t, "daemon_already_running", de.Code)
	assert.Contains(t, de.Message, fmt.Sprintf("pid %d", livePID))
}

// CT-3: Stale PID file is cleaned and Start proceeds.
func TestStart_StalePIDCleaned(t *testing.T) {
	paths := setupPaths(t)
	require.NoError(t, os.MkdirAll(paths.StateHome(), 0o700))

	// Write a dead PID.
	stale := deadPID(t)
	require.NoError(t, writePID(paths.PIDFile(), stale))

	m := newManagerWithOpts(paths, 2*time.Second, 1*time.Second, readySentinelCmd(paths.ReadyFile()))
	err := m.Start(context.Background(), "/fake/config.yaml")
	require.NoError(t, err, "start should succeed after cleaning stale PID")

	pid, readErr := readPID(paths.PIDFile())
	require.NoError(t, readErr)
	assert.NotEqual(t, stale, pid, "PID file should contain new process PID, not stale one")
	assert.True(t, isAlive(pid))

	t.Cleanup(func() { killProcess(pid) })
}

// CT-4: Stop sends SIGTERM and removes the PID file.
func TestStop_SendsSIGTERM(t *testing.T) {
	paths := setupPaths(t)
	require.NoError(t, os.MkdirAll(paths.StateHome(), 0o700))

	// Spawn a real sleeper.
	cmd := exec.Command("sleep", "60")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	require.NoError(t, writePID(paths.PIDFile(), pid))
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	m := newManagerWithOpts(paths, 1*time.Second, 2*time.Second, neverReadyCmd())
	require.NoError(t, m.Stop(context.Background()))

	// Process should be dead.
	assert.False(t, isAlive(pid), "process should be dead after Stop")
	// PID file should be removed.
	_, err := os.Stat(paths.PIDFile())
	assert.True(t, os.IsNotExist(err), "PID file should be removed after Stop")
}

// CT-5: Stop returns daemon_not_running when no PID file exists.
func TestStop_NotRunning(t *testing.T) {
	paths := setupPaths(t)
	require.NoError(t, os.MkdirAll(paths.StateHome(), 0o700))

	m := newManagerWithOpts(paths, 1*time.Second, 1*time.Second, neverReadyCmd())
	err := m.Stop(context.Background())
	require.Error(t, err)
	var de *DaemonError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "daemon_not_running", de.Code)
}

// CT-6: Status reads and returns DaemonStatus from the state file.
func TestStatus_ReadsStateFile(t *testing.T) {
	paths := setupPaths(t)
	require.NoError(t, os.MkdirAll(paths.StateHome(), 0o700))

	// Write a live PID (ourselves).
	livePID := os.Getpid()
	require.NoError(t, writePID(paths.PIDFile(), livePID))

	// Write a status.json.
	want := &DaemonStatus{
		PID: livePID,
		Hosts: []HostStatus{
			{Name: "dev", Status: "connected"},
			{Name: "build", Status: "reconnecting"},
		},
	}
	require.NoError(t, WriteStatus(paths.StatusFile(), want))

	m := newManagerWithOpts(paths, 1*time.Second, 1*time.Second, neverReadyCmd())
	got, err := m.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, want.PID, got.PID)
	require.Len(t, got.Hosts, 2)
	assert.Equal(t, "dev", got.Hosts[0].Name)
	assert.Equal(t, "connected", got.Hosts[0].Status)
}

// CT-7: Status returns daemon_not_running when no PID file exists.
func TestStatus_NotRunning(t *testing.T) {
	paths := setupPaths(t)
	require.NoError(t, os.MkdirAll(paths.StateHome(), 0o700))

	m := newManagerWithOpts(paths, 1*time.Second, 1*time.Second, neverReadyCmd())
	_, err := m.Status(context.Background())
	require.Error(t, err)
	var de *DaemonError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "daemon_not_running", de.Code)
}

// CT-8: WriteStatus writes atomically via rename — no .tmp file left behind.
func TestWriteStatus_Atomic(t *testing.T) {
	dir := t.TempDir()
	statusPath := dir + "/status.json"
	tmpPath := statusPath + ".tmp"

	status := &DaemonStatus{
		PID:   12345,
		Hosts: []HostStatus{{Name: "dev", Status: "connected"}},
	}

	require.NoError(t, WriteStatus(statusPath, status))

	// Final file must exist and be valid JSON.
	data, err := os.ReadFile(statusPath)
	require.NoError(t, err)
	var got DaemonStatus
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, 12345, got.PID)

	// Temp file must not be left behind.
	_, err = os.Stat(tmpPath)
	assert.True(t, os.IsNotExist(err), ".tmp file must not exist after successful WriteStatus")
}

// CT-9: Start returns daemon_start_timeout when ready sentinel never appears.
func TestStart_ReadyTimeout(t *testing.T) {
	paths := setupPaths(t)
	require.NoError(t, os.MkdirAll(paths.StateHome(), 0o700))

	// Use a very short timeout and a command that never writes the sentinel.
	m := newManagerWithOpts(paths, 200*time.Millisecond, 1*time.Second, neverReadyCmd())
	err := m.Start(context.Background(), "/fake/config.yaml")
	require.Error(t, err)
	var de *DaemonError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "daemon_start_timeout", de.Code)
}

// CT-10: Stop escalates to SIGKILL when process ignores SIGTERM.
func TestStop_SIGKILLEscalation(t *testing.T) {
	paths := setupPaths(t)
	require.NoError(t, os.MkdirAll(paths.StateHome(), 0o700))

	// Spawn a process that ignores SIGTERM.
	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 60")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	require.NoError(t, writePID(paths.PIDFile(), pid))
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	// Stop with a short timeout — should escalate to SIGKILL.
	m := newManagerWithOpts(paths, 1*time.Second, 300*time.Millisecond, neverReadyCmd())
	err := m.Stop(context.Background())
	// Stop should succeed (after SIGKILL).
	require.NoError(t, err)

	// Give SIGKILL a moment to land.
	time.Sleep(200 * time.Millisecond)
	assert.False(t, isAlive(pid), "process should be dead after SIGKILL escalation")
}

// --- Behavioral Tests ---

// BT-4: Double-stop returns daemon_not_running, no panic.
func TestStop_DoubleStop(t *testing.T) {
	paths := setupPaths(t)
	require.NoError(t, os.MkdirAll(paths.StateHome(), 0o700))

	// Spawn and stop once.
	cmd := exec.Command("sleep", "60")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	require.NoError(t, writePID(paths.PIDFile(), pid))
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	m := newManagerWithOpts(paths, 1*time.Second, 2*time.Second, neverReadyCmd())
	require.NoError(t, m.Stop(context.Background()))

	// Second stop must not panic and must return daemon_not_running.
	err := m.Stop(context.Background())
	require.Error(t, err)
	var de *DaemonError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "daemon_not_running", de.Code)
}

// RG-1: PID file must not be removed before the process exits.
func TestStop_PIDFileRemovedAfterExit(t *testing.T) {
	paths := setupPaths(t)
	require.NoError(t, os.MkdirAll(paths.StateHome(), 0o700))

	cmd := exec.Command("sleep", "60")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	require.NoError(t, writePID(paths.PIDFile(), pid))
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	m := newManagerWithOpts(paths, 1*time.Second, 2*time.Second, neverReadyCmd())
	require.NoError(t, m.Stop(context.Background()))

	// By the time Stop() returns the process must be dead.
	assert.False(t, isAlive(pid), "process must be dead when Stop() returns")
	// And PID file must be gone.
	_, err := os.Stat(paths.PIDFile())
	assert.True(t, os.IsNotExist(err), "PID file must not exist after Stop() returns")
}
