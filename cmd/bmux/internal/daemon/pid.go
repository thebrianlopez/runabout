package daemon

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// writePID writes pid to path, creating parent directories as needed.
func writePID(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

// readPID reads the PID from path. Returns (0, err) if the file is absent or
// cannot be parsed.
func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read pid file %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse pid file %s: %w", path, err)
	}
	return pid, nil
}

// removePID deletes the PID file. Ignores ENOENT.
func removePID(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// isAlive returns true if pid refers to a running, non-zombie process.
//
// On Unix, FindProcess always succeeds; we use Signal(0) to test liveness.
// However, Signal(0) returns success for zombie processes (children that exited
// but whose parent hasn't called wait yet). We handle this by attempting a
// non-blocking wait first: if the process is our child and has exited, we reap
// it and return false. This is safe to call multiple times.
func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Non-blocking wait: reaps zombie children of the current process.
	// In production the daemon is adopted by PID 1 (Setsid), so Wait4 returns
	// ECHILD immediately and we fall through to Signal(0).
	// In tests the spawned process is our child, so Wait4 reaps it when dead.
	var ws syscall.WaitStatus
	wpid, _ := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	if wpid == pid {
		// Was our child; reaped → dead.
		return false
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// cleanStalePID checks whether the PID file at path refers to a dead process.
// If so, it removes the file and returns (true, stalePID, nil).
// If the process is alive, returns (false, livePID, nil).
// If no PID file exists, returns (false, 0, nil).
func cleanStalePID(path string) (wasStale bool, pid int, err error) {
	pid, err = readPID(path)
	if err != nil {
		return false, 0, err
	}
	if pid == 0 {
		// No PID file.
		return false, 0, nil
	}
	if isAlive(pid) {
		return false, pid, nil
	}
	// Stale: process is dead. Remove the file.
	if err := removePID(path); err != nil {
		return true, pid, err
	}
	return true, pid, nil
}
