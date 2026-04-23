package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/blo-grindr/bmux/internal/config"
)

func newSocketPathCmd(paths *config.Paths) *cobra.Command {
	return &cobra.Command{
		Use:   "socket-path",
		Short: "Print the tmux socket directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), paths.SocketDir())
			return nil
		},
	}
}
