package ssh

import (
	"errors"
	"fmt"
)

// SSHError is a typed error from SSH connection or session management.
type SSHError struct {
	Code    string
	Message string
}

func (e *SSHError) Error() string { return e.Message }

// ErrSessionClosed is returned by SendInput when the session is not connected.
var ErrSessionClosed = errors.New("session is closed")

func errHostUnreachable(host string, port int, detail string) *SSHError {
	return &SSHError{
		Code:    "ssh_host_unreachable",
		Message: fmt.Sprintf("cannot reach %s:%d: %s", host, port, detail),
	}
}

func errAuthFailed(host string) *SSHError {
	return &SSHError{
		Code:    "ssh_auth_failed",
		Message: fmt.Sprintf("cannot authenticate to %s: check identity_file or ssh-agent", host),
	}
}

func errHostKeyMismatch(host string) *SSHError {
	return &SSHError{
		Code:    "ssh_host_key_mismatch",
		Message: fmt.Sprintf("host key mismatch for %s — update ~/.ssh/known_hosts", host),
	}
}

func errControlModeRejected(host string) *SSHError {
	return &SSHError{
		Code:    "control_mode_rejected",
		Message: fmt.Sprintf("remote tmux does not support control mode for %s — upgrade to tmux 3.2+", host),
	}
}

func errTmuxNotFound() *SSHError {
	return &SSHError{
		Code:    "tmux_not_found",
		Message: "tmux not found in PATH — install tmux to continue",
	}
}

func errSessionProjectionFailed(host, detail string) *SSHError {
	return &SSHError{
		Code:    "session_projection_failed",
		Message: fmt.Sprintf("failed to create local session for %s: %s", host, detail),
	}
}
