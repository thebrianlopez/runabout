package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/blo-grindr/bmux/internal/config"
	"github.com/blo-grindr/bmux/internal/daemon"
)

func newStopCmd(paths *config.Paths) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the bmux daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			dm := daemon.NewManager(paths)
			if err := dm.Stop(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "bmux daemon stopped")
			return nil
		},
	}
}
