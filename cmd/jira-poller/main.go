package main

import (
	"fmt"
	"os"
)

// Build-time variables injected via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	fmt.Fprintf(os.Stderr, "jira-poller %s (%s, %s)\n", version, commit, date)
	os.Exit(0)
}
