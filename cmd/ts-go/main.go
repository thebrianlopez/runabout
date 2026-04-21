package main

import (
	"fmt"
	"os"
)

var (
	version = "0.1.0"
	commit  = "dev"
	date    = "unknown"
)

var formatFlag string

func main() {
	rootCmd := newRootCmd()

	t := instrument(rootCmd, "ts-go")
	err := rootCmd.Execute()
	t.emit(err)
	if err != nil {
		os.Exit(1)
	}
}

func versionString() string {
	return fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date)
}
