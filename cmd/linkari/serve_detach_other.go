//go:build !darwin && !linux && !android

// serve_detach_other.go — non-POSIX stub for `linkari serve --detach`.
//
// EPIC-049 M3. All POSIX targets (darwin, linux, android/Termux) are handled
// by serve_detach_posix.go. This stub guards unsupported platforms.
package main

import "fmt"

func maybeDetach(detach bool) error {
	if !detach {
		return nil
	}
	return fmt.Errorf("--detach is not supported on this platform (requires POSIX fork/setsid semantics)")
}

func signalDetachReady() {
	// Non-POSIX: no detach support, this is a no-op.
}
