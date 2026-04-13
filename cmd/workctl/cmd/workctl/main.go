package main

import (
	"os"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/telemetry"
)

func main() {
	root := rootCmd()
	t := telemetry.Instrument(root, "workctl")
	err := root.Execute()
	t.Emit(err)
	if err != nil {
		os.Exit(1)
	}
}
