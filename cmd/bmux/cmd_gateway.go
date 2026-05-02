package main

import (
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/blo-grindr/bmux/internal/config"
	"github.com/blo-grindr/bmux/internal/gateway/lifecycle"
)

func newGatewayCmd(paths *config.Paths, configPath *string) *cobra.Command {
	gateway := &cobra.Command{
		Use:   "gateway",
		Short: "Manage the WebSocket gateway",
	}

	gateway.AddCommand(newGatewayStatusCmd(paths))
	gateway.AddCommand(newGatewayTokenCmd(paths, configPath))

	return gateway
}

func newGatewayStatusCmd(paths *config.Paths) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show WebSocket gateway status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadConfig(paths.ConfigFile())
			if err != nil {
				return err
			}

			if !cfg.Gateway.Enabled {
				fmt.Fprintln(cmd.OutOrStdout(), "gateway: disabled")
				return nil
			}

			// Without a live GatewayManager reference we report static config.
			// In a future milestone this will query the daemon via IPC.
			fmt.Fprintf(cmd.OutOrStdout(), "gateway: enabled\n")
			fmt.Fprintf(cmd.OutOrStdout(), "  bind: %s:%d\n", cfg.Gateway.Host, cfg.Gateway.Port)
			return nil
		},
	}
}

func newGatewayTokenCmd(paths *config.Paths, configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "generate",
		Short: "Generate a new gateway auth token and write it to config.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath := *configPath
			if cfgPath == "" {
				cfgPath = paths.ConfigFile()
			}

			// Warn if config is world-readable before generating.
			if warn := lifecycle.CheckConfigPerms(cfgPath); warn != "" {
				slog.Warn("gateway_config_world_readable", "path", cfgPath, "warning", warn)
			}

			token, err := lifecycle.GenerateToken(cfgPath)
			if err != nil {
				return err
			}

			fmt.Fprintln(cmd.OutOrStdout(), token)
			slog.Info("gateway_token_written", "path", cfgPath)
			return nil
		},
	}
}
