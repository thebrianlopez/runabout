package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/blo-grindr/bmux/internal/config"
	"github.com/blo-grindr/bmux/internal/daemon"
)

func newStartCmd(paths *config.Paths, configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the bmux daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath := *configPath
			if cfgPath == "" {
				cfgPath = paths.ConfigFile()
			}
			dm := daemon.NewManager(paths)
			if err := dm.Start(cmd.Context(), cfgPath); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "bmux daemon started")
			return nil
		},
	}
}
