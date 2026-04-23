package daemon

import "fmt"

// DaemonError is a typed error from daemon lifecycle operations.
type DaemonError struct {
	Code    string
	Message string
}

func (e *DaemonError) Error() string { return e.Message }

func errAlreadyRunning(pid int) *DaemonError {
	return &DaemonError{
		Code:    "daemon_already_running",
		Message: fmt.Sprintf("bmux is already running (pid %d) — use bmux stop first", pid),
	}
}

func errNotRunning() *DaemonError {
	return &DaemonError{
		Code:    "daemon_not_running",
		Message: "bmux is not running",
	}
}

func errStartTimeout(logPath string) *DaemonError {
	return &DaemonError{
		Code:    "daemon_start_timeout",
		Message: fmt.Sprintf("bmux serve did not become ready within timeout — check logs: %s", logPath),
	}
}

func errStateUnreadable(detail string) *DaemonError {
	return &DaemonError{
		Code:    "state_file_unreadable",
		Message: fmt.Sprintf("cannot read daemon status — daemon may be starting: %s", detail),
	}
}
