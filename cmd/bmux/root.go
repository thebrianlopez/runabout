package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/blo-grindr/bmux/internal/config"
)

func newRootCmd() *cobra.Command {
	paths := config.NewPaths()

	var logFormat string
	var logLevel string
	var verbose bool
	var socketName string
	var configPath string
	var showVersion bool

	root := &cobra.Command{
		Use:           "bmux",
		Short:         "Remote tmux session manager over SSH",
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// --verbose/-v is shorthand for --log-level=debug
			level := slog.LevelInfo
			if verbose {
				level = slog.LevelDebug
			} else {
				switch logLevel {
				case "debug":
					level = slog.LevelDebug
				case "warn":
					level = slog.LevelWarn
				case "error":
					level = slog.LevelError
				default:
					level = slog.LevelInfo
				}
			}
			opts := &slog.HandlerOptions{Level: level}
			var handler slog.Handler
			if logFormat == "json" {
				handler = slog.NewJSONHandler(os.Stderr, opts)
			} else {
				handler = slog.NewTextHandler(os.Stderr, opts)
			}
			slog.SetDefault(slog.New(handler))
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				fmt.Fprintln(cmd.OutOrStdout(), versionString())
				return nil
			}
			return cmd.Help()
		},
	}

	root.Flags().BoolVar(&showVersion, "version", false, "print version and exit")
	root.PersistentFlags().StringVar(&logFormat, "log-format", "text", "log output format: text, json")
	root.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "shorthand for --log-level=debug")
	root.PersistentFlags().StringVar(&socketName, "socket", "bmux", "local tmux socket name")
	root.PersistentFlags().StringVar(&configPath, "config", "", "config file path (default: $XDG_CONFIG_HOME/bmux/config.yaml)")

	root.AddCommand(
		newStartCmd(paths, &configPath),
		newStopCmd(paths),
		newStatusCmd(paths),
		newSocketPathCmd(paths),
		newConfigCmd(paths),
		newDoctorCmd(paths),
		newServeCmd(paths, &socketName, &configPath),
	)

	root.AddCommand(newAttachCmd(paths, &socketName))
	root.AddCommand(newCompletionCmd(root))

	return root
}
