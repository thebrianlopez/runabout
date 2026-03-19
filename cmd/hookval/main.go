package main

import (
	"fmt"
	"os"

	"github.com/blo-grindr/runabout/internal/telemetry"
	versionpkg "github.com/blo-grindr/runabout/internal/version"
	"github.com/spf13/cobra"
)

var (
	version = "0.1.0"
	commit  = "dev"
	date    = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "hookval",
		Short:   "Validate and document Claude hook context signals",
		Version: versionpkg.Format(version, commit, date),
	}

	rootCmd.AddCommand(validateCmd())
	rootCmd.AddCommand(genDocsCmd())
	rootCmd.AddCommand(lintSchemaCmd())

	t := telemetry.Instrument(rootCmd, "hookval")
	err := rootCmd.Execute()
	t.Emit(err)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
