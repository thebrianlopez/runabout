package chainindex

import "os/exec"

// newCmd constructs an exec.Cmd. Extracted so tests that need exec can reference
// the same constructor the production code uses.
func newCmd(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
