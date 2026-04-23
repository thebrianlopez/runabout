package main

import (
	"github.com/spf13/cobra"

	"github.com/blo-grindr/bmux/internal/config"
)

func newConfigCmd(paths *config.Paths) *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Manage bmux configuration",
	}

	var force bool
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Create a default config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			return config.InitConfig(paths.ConfigFile(), force)
		},
	}
	initCmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")

	configCmd.AddCommand(initCmd)
	return configCmd
}
