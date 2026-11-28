// Package tmux: action dispatch backends.
//
// Logging invariant: all tmux/exec invocations that render raw argv MUST log
// via logTmuxExec(cmd), never raw slog.Debug("tmux exec", "args", cmd.Args).
// The raw form renders a []string with %v, space-joining elements without
// quoting - argv boundaries vanish and embedded spaces look like token
// separators. This masked a suspected quoting bug during the 2026-04-09
// linkari-server.log investigation. Regression coverage: tmux_log_test.go.
package main

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// posixQuote returns s quoted for a POSIX shell. Unlike strconv.Quote
// (which uses Go escaping - unsafe for shell paste because $, `, and \
// are re-interpreted inside double-quoted strings), this uses POSIX
// single-quote escaping where the only metacharacter is ' itself
// (escaped as '\”). Safe identifier characters pass through unquoted.
func posixQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-', r == '/', r == '.', r == '=', r == ':', r == ',', r == '@', r == '+':
			// safe
		default:
			safe = false
		}
		if !safe {
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// posixJoin joins argv into a single shell-pasteable command string.
func posixJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = posixQuote(a)
	}
	return strings.Join(parts, " ")
}

// logTmuxExec logs a child-process invocation with structured argv plus a
// POSIX shell-quoted repro string. The repro field is copy-pasteable
// into a terminal to reproduce exactly what the child process saw - essential
// for debugging argv quoting/spacing bugs where slog's default
// []string rendering hides element boundaries.
func logTmuxExec(cmd *exec.Cmd) {
	slog.Debug(
		"tmux exec",
		"argv", cmd.Args,
		"repro", posixJoin(cmd.Args),
	)
}

// needsDispatch reports whether an action kind requires a Dispatcher
// (as opposed to server-side scoring or capture, which never touch tmux/exec
// dispatch).
func needsDispatch(kind ActionKind) bool {
	switch kind {
	case KindLiteral, KindTemplate, KindRegex:
		return true
	default:
		return false
	}
}

// Dispatcher is the backend-agnostic action-dispatch seam. A Dispatcher
// takes a fully rendered shell command and runs it detached; the caller
// never sees session or window naming.
type Dispatcher interface {
	Dispatch(command string) error
}

// Dispatch backend names for [server] dispatch_backend.
const (
	// DispatchBackendAuto picks TmuxDispatcher when a tmux server is running,
	// else ExecDispatcher. Evaluated at each dispatcherFor call.
	DispatchBackendAuto = "auto"
	// DispatchBackendTmux always uses tmux; lazily fails per dispatch when
	// no tmux server is available (callers queue for replay).
	DispatchBackendTmux = "tmux"
	// DispatchBackendExec always uses a detached os/exec spawn.
	DispatchBackendExec = "exec"
)

// NewDispatcher returns a Dispatcher for the given backend and tmux-style
// target ("session:pane"; only the session part is used, exec ignores it).
// backend "" is treated as "auto".
func NewDispatcher(backend string, tr *TmuxRunner, target string) Dispatcher {
	if dispatchEffective(backend) == DispatchBackendTmux {
		return &TmuxDispatcher{runner: tr, session: sessionFromTarget(target)}
	}
	return &ExecDispatcher{shell: tr.shell(), shellArgs: tr.shellArgs()}
}

// dispatchEffective resolves a configured backend name to a concrete backend.
// "auto" resolves at call time: tmux when a tmux server is running, else exec.
// Unknown values behave as "auto" (validate() warns on them).
func dispatchEffective(backend string) string {
	if backend == DispatchBackendTmux {
		return DispatchBackendTmux
	}
	if backend == DispatchBackendExec {
		return DispatchBackendExec
	}
	if tr := (&TmuxRunner{}); tr.serverRunning() {
		return DispatchBackendTmux
	}
	return DispatchBackendExec
}

// sessionFromTarget extracts the session part of a "session:pane" target.
// Empty target falls back to the historical default session name.
func sessionFromTarget(target string) string {
	if s := strings.Split(target, ":")[0]; s != "" {
		return s
	}
	return "linkari"
}

// TmuxRunner groups dispatch config shared by both backends: which shell
// runs spawned commands. Shell and ShellArgs control the shell used inside
// new tmux windows and by ExecDispatcher. Defaults: Shell="fish",
// ShellArgs="-c".
type TmuxRunner struct {
	Debug     bool
	Shell     string // shell binary (default: "fish")
	ShellArgs string // shell command flag (default: "-c")
}

// shell returns the configured shell or "fish".
func (t *TmuxRunner) shell() string {
	if t.Shell != "" {
		return t.Shell
	}
	return "fish"
}

// shellArgs returns the configured shell args or "-c".
func (t *TmuxRunner) shellArgs() string {
	if t.ShellArgs != "" {
		return t.ShellArgs
	}
	return "-c"
}

// TmuxDispatcher dispatches commands into a new tmux window. tmux stays an
// optional backend: it exists for operator visibility (live panes to watch),
// not as a hard dependency.
type TmuxDispatcher struct {
	runner  *TmuxRunner
	session string
}

// Dispatch runs command in a new window in the dispatcher's session.
// The window name is derived from the command (sanitized, truncated) since
// the Dispatcher interface carries only the command string.
func (d *TmuxDispatcher) Dispatch(command string) error {
	name := sanitizeWindowName(command)
	return d.runner.NewWindow(d.session, command, name)
}

// ExecDispatcher dispatches commands as detached child processes
// (fire-and-forget spawn, same pattern as handleInlineTriage).
type ExecDispatcher struct {
	shell     string
	shellArgs string
}

// Dispatch spawns the command detached via the configured shell. stdout and
// stderr are discarded; callers that need output persist it themselves
// (queue, sidecar, FCM push).
func (d *ExecDispatcher) Dispatch(command string) error {
	cmd := exec.Command(d.shell, d.shellArgs, command)
	// Detach from parent stdio so the server doesn't block on the child.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = detachSysProcAttr()
	logTmuxExec(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("dispatch exec: %w", err)
	}
	// Fire and forget: reap the child in the background to avoid zombies.
	go func() { _ = cmd.Wait() }()
	slog.Debug("exec dispatch started", "event_type", "dispatch_exec", "command", command)
	return nil
}

// shellQuote (handler.go) and sanitizeWindowName (handler.go) remain the
// shared naming helpers for template rendering and tmux window names.

// serverRunning checks if the tmux server is running (any sessions exist).
func (t *TmuxRunner) serverRunning() bool {
	cmd := exec.Command("tmux", "list-sessions")
	return cmd.Run() == nil
}

// createSession creates a named tmux session if it doesn't already exist.
func (t *TmuxRunner) createSession(name string) error {
	if t.sessionExists(name) {
		return nil
	}
	slog.Debug("tmux creating session", "session", name)
	cmd := exec.Command("tmux", "new-session", "-d", "-s", name)
	logTmuxExec(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create session %q: %w: %s", name, err, string(out))
	}
	return nil
}

// sessionExists checks if a tmux session with this exact name exists.
// The "=" prefix forces exact matching - without it, tmux resolves "-t linkari"
// to a window named "linkari" in another session (e.g. local:linkari), causing
// new windows to land in the wrong session.
func (t *TmuxRunner) sessionExists(name string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", "="+name)
	return cmd.Run() == nil
}

// NewWindow creates a new tmux window in the given session and runs command in it.
// The optional name parameter sets the window name via -n; if empty, tmux uses its default.
// The window has remain-on-exit set to "failed" so it auto-closes on success
// but stays open when the command exits with a non-zero status.
func (t *TmuxRunner) NewWindow(session, command string, name string) error {
	if session == "" {
		return fmt.Errorf("tmux session name is required")
	}

	slog.Debug(
		"tmux new-window",
		"event_type", "tmux_new_window",
		"session", session,
		"command", command,
		"name", name,
	)

	if err := t.createSession(session); err != nil {
		return fmt.Errorf("ensure session: %w", err)
	}

	// Create new window running the command in the configured shell; exec keeps
	// the shell alive after the command completes (on success, remain-on-exit closes it).
	// Use "=" prefix for exact session matching to prevent tmux from resolving
	// the target to a window with the same name in a different session.
	sh := t.shell()
	shellCmd := fmt.Sprintf("%s; exec %s", command, sh)
	exactSession := "=" + session
	args := []string{"new-window", "-a", "-t", exactSession}
	if name != "" {
		args = append(args, "-n", name)
	}
	args = append(args, sh, t.shellArgs(), shellCmd)
	cmd := exec.Command("tmux", args...)
	logTmuxExec(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("new-window: %w: %s", err, string(out))
	}

	// Set remain-on-exit to "failed" on the newly created (last) window so it
	// only persists when the command fails.
	setCmd := exec.Command("tmux", "set-option", "-p", "-t", exactSession+":{end}", "remain-on-exit", "failed")
	logTmuxExec(setCmd)
	if out, err := setCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("set remain-on-exit: %w: %s", err, string(out))
	}

	slog.Debug("tmux new-window complete")
	return nil
}
