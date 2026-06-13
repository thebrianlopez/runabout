package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var showVersion bool

	root := &cobra.Command{
		Use:           "castex",
		Short:         "AI usage cost and classification reporting from automation-metrics events",
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				fmt.Fprintf(cmd.OutOrStdout(), "castex %s (commit %s, built %s)\n", version, commit, date)
				return nil
			}
			return cmd.Help()
		},
	}

	root.Flags().BoolVar(&showVersion, "version", false, "print version and exit")
	root.AddCommand(newReportCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newDirectiveCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newConsensusCmd())

	return root
}
