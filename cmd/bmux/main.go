package main

import (
	"fmt"
	"os"
)

// Version variables injected at build time via ldflags.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func versionString() string {
	return fmt.Sprintf("bmux %s (commit %s, built %s)", version, commit, date)
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
