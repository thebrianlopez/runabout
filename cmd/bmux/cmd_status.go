package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/blo-grindr/bmux/internal/config"
	"github.com/blo-grindr/bmux/internal/daemon"
)

func newStatusCmd(paths *config.Paths) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"list"},
		Short:   "Show daemon and host connection status",
		RunE: func(cmd *cobra.Command, args []string) error {
			dm := daemon.NewManager(paths)
			status, err := dm.Status(cmd.Context())
			if err != nil {
				return err
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(status)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "daemon PID: %d\n", status.PID)
			for _, h := range status.Hosts {
				fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", h.Name, h.Status)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output status as JSON")
	return cmd
}
