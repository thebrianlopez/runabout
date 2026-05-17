package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/version"
)

func versionCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Long:  "Print workctl version, commit hash, build date, Go version, and platform.",
		Args:  cobra.NoArgs,
		// Override the root PersistentPreRunE so version works with zero
		// configuration — no env vars, no config file, no API tokens required.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			info := version.Get()
			if jsonOut {
				fmt.Println(info.JSON())
			} else {
				fmt.Println(info)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output version information as JSON")

	return cmd
}
