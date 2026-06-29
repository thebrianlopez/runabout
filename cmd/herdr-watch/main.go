package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{Use: "herdr-watch", Short: "Termux watcher for Herdr session transitions"}
	rootCmd.AddCommand(pollCmd())
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func pollCmd() *cobra.Command {
	var statePath string
	var command string
	cmd := &cobra.Command{Use: "poll", RunE: func(cmd *cobra.Command, args []string) error {
		_ = statePath
		_ = command
		fmt.Fprintln(cmd.OutOrStdout(), "herdr-watch poll scaffold")
		return nil
	}}
	cmd.Flags().StringVar(&statePath, "state", "", "state file path")
	cmd.Flags().StringVar(&command, "command", "herdr agent list", "poll command")
	return cmd
}
