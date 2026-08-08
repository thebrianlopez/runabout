//go:build darwin || linux || android

// serve_detach_posix.go — POSIX fork-detach primitive for `linkari serve --detach`.
//
// EPIC-049 M3. Uses os/exec re-exec (not a bare fork) because Go's runtime
// does not safely support fork-without-exec (goroutines, GC, etc.).
//
// Flow:
//
//	Parent (--detach set):
//	  1. Check stale PID file; error if process is live.
//	  2. Create os.Pipe — parent reads, child writes "READY\n" when listener binds.
//	  3. Re-exec binary without --detach; pass write-end as fd 3 via ExtraFiles.
//	     Env LINKARI_DETACH_PIPE_FD=3 tells child to signal on that fd.
//	  4. Close write-end in parent; read from pipe.
//	  5. On "READY\n": write PID file, print message, os.Exit(0).
//	  6. On pipe error/EOF without data: propagate child failure, exit 1.
//
//	Child (LINKARI_DETACH_PIPE_FD set, --detach absent):
//	  - Runs normal serve path.
//	  - After net.Listen succeeds: calls signalDetachReady() to write "READY\n".
//	  - Closes pipe fd so parent unblocks.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const detachPipeFDEnv = "LINKARI_DETACH_PIPE_FD"

// maybeDetach is the POSIX implementation. If detach=true, re-execs the
// binary without --detach, waits for the child's ready signal, writes the
// PID file, prints a message, and calls os.Exit(0). Never returns to caller
// when detach=true. No-op (returns nil) when detach=false.
func maybeDetach(detach bool) error {
	if !detach {
		return nil
	}

	effectivePaths, err := resolveEffectivePaths(nil)
	if err != nil {
		return fmt.Errorf("detach: state dir: %w", err)
	}
	stateDir := effectivePaths.StateDir
	if err := ensureDir(stateDir); err != nil {
		return fmt.Errorf("detach: state dir: %w", err)
	}
	pidPath := filepath.Join(stateDir, "linkari.pid")

	// Stale PID check.
	if data, readErr := os.ReadFile(pidPath); readErr == nil {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr == nil && pid > 0 {
			if isProcessAlive(pid) {
				return fmt.Errorf("detach: linkari is already running (pid=%d)\n  kill: kill $(cat %s)", pid, pidPath)
			}
			// Dead PID — remove stale file.
			os.Remove(pidPath)
		}
	}

	// Create ready pipe.
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("detach: create pipe: %w", err)
	}

	// Filter --detach (and --detach=true) from the arg list.
	childArgs := filterDetachArg(os.Args[1:])

	cmd := exec.Command(os.Args[0], childArgs...)
	cmd.Env = append(os.Environ(), detachPipeFDEnv+"=3")
	cmd.ExtraFiles = []*os.File{readyW} // becomes fd 3 in child
	// Redirect stdin/stdout/stderr away from the parent's TTY.
	// The child sets up its own log sink (server.yaml log_file or ring buffer).
	devnull, dnErr := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if dnErr == nil {
		cmd.Stdin = devnull
		cmd.Stdout = devnull
		cmd.Stderr = devnull
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if startErr := cmd.Start(); startErr != nil {
		readyR.Close()
		readyW.Close()
		return fmt.Errorf("detach: start child: %w", startErr)
	}
	// Parent closes write-end so pipe EOF fires when child also closes it.
	readyW.Close()
	if devnull != nil {
		devnull.Close()
	}

	// Block until child signals ready or fails.
	var buf [16]byte
	n, _ := readyR.Read(buf[:])
	readyR.Close()

	if n == 0 {
		// Pipe closed without data — child exited before binding.
		_ = cmd.Wait()
		return fmt.Errorf("detach: child exited before binding listener (check log file for details)")
	}

	// Child is running. Write PID file.
	pid := cmd.Process.Pid
	pidData := strconv.Itoa(pid) + "\n"
	if writeErr := os.WriteFile(pidPath, []byte(pidData), 0o644); writeErr != nil {
		fmt.Fprintf(os.Stderr, "linkari: warning: could not write PID file %s: %v\n", pidPath, writeErr)
	}

	fmt.Fprintf(os.Stdout, "linkari: started in background (pid=%d)\n  PID file: %s\n  Stop:     kill $(cat %s)\n", pid, pidPath, pidPath)
	os.Exit(0)
	return nil // unreachable
}

// signalDetachReady signals the parent that the local listener is bound.
// Writes "READY\n" to the fd specified by LINKARI_DETACH_PIPE_FD. No-op
// if the env var is absent (non-detach serve invocation).
func signalDetachReady() {
	fdStr := os.Getenv(detachPipeFDEnv)
	if fdStr == "" {
		return
	}
	fd, err := strconv.Atoi(fdStr)
	if err != nil {
		return
	}
	f := os.NewFile(uintptr(fd), "detach-ready-pipe")
	if f == nil {
		return
	}
	f.WriteString("READY\n")
	f.Close()
}

// isProcessAlive returns true if the given PID corresponds to a live process.
// Uses syscall.Kill(pid, 0) which is safe and does not send a real signal.
// False-positive risk on PID reuse (e.g. after reboot) is accepted; this is
// a personal-use tool and PID collision frequency is negligible.
func isProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// filterDetachArg removes --detach and --detach=true from an arg slice.
func filterDetachArg(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--detach" || a == "--detach=true" || a == "--detach=1" {
			continue
		}
		out = append(out, a)
	}
	return out
}
