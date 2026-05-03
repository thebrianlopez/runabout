package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/blo-grindr/bmux/internal/config"
	"github.com/blo-grindr/bmux/internal/daemon"
)

// execFn is the syscall.Exec implementation — replaceable in tests.
var execFn = func(argv0 string, argv []string, envv []string) error {
	return syscall.Exec(argv0, argv, envv)
}

// attachError is returned when the attach command cannot proceed.
type attachError struct {
	Code    string
	Message string
}

func (e *attachError) Error() string { return fmt.Sprintf("[%s] %s", e.Code, e.Message) }

func errAttachUnknownHost(host string) *attachError {
	return &attachError{
		Code:    "attach_unknown_host",
		Message: fmt.Sprintf("no session for host %q — check 'bmux list' for available hosts", host),
	}
}

func errAttachHostNotReady(host, state string) *attachError {
	return &attachError{
		Code:    "attach_host_not_ready",
		Message: fmt.Sprintf("host %q is %s — wait for connection or check 'bmux status'", host, state),
	}
}

// normalizeTERM replaces tmux-* TERM values with xterm-256color to prevent
// "terminal does not support clear" when attaching from within a tmux session.
func normalizeTERM(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "TERM=tmux") {
			out = append(out, "TERM=xterm-256color")
		} else {
			out = append(out, e)
		}
	}
	return out
}

func newAttachCmd(paths *config.Paths, socketName *string) *cobra.Command {
	return &cobra.Command{
		Use:   "attach [host]",
		Short: "Attach to a remote host's local tmux session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := args[0]

			// Validate host against daemon status and check bridge readiness.
			dm := daemon.NewManager(paths)
			status, err := dm.Status(cmd.Context())
			if err != nil {
				return errAttachUnknownHost(host)
			}
			found := false
			for _, h := range status.Hosts {
				if h.Name == host {
					found = true
					if h.Status != "connected" {
						return errAttachHostNotReady(host, h.Status)
					}
					break
				}
			}
			if !found {
				return errAttachUnknownHost(host)
			}

			// Resolve tmux binary.
			tmuxPath, err := exec.LookPath("tmux")
			if err != nil {
				return fmt.Errorf("tmux not found in PATH")
			}

			name := "bmux"
			if socketName != nil && *socketName != "" {
				name = *socketName
			}
			argv := []string{"tmux", "-L", name, "attach-session", "-t", host}
			return execFn(tmuxPath, argv, normalizeTERM(os.Environ()))
		},
	}
}
