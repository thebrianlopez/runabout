package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/blo-grindr/bmux/internal/bridge"
	"github.com/blo-grindr/bmux/internal/config"
	"github.com/blo-grindr/bmux/internal/daemon"
	"github.com/blo-grindr/bmux/internal/ssh"
)

func newServeCmd(paths *config.Paths, socketName, configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:    "serve",
		Short:  "Run the bmux daemon process (internal)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath := *configPath
			if cfgPath == "" {
				cfgPath = paths.ConfigFile()
			}

			cfg, err := config.LoadConfig(cfgPath)
			if err != nil {
				return err
			}

			if err := daemon.WriteSelfPID(paths); err != nil {
				return err
			}
			defer daemon.RemoveReady(paths)

			socketDir := paths.SocketDir()
			if err := os.MkdirAll(socketDir, 0o700); err != nil {
				return err
			}

			name := "bmux"
			if socketName != nil && *socketName != "" {
				name = *socketName
			}

			br, err := bridge.NewLocalTmuxBridge(socketDir, name)
			if err != nil {
				return err
			}

			mgr := ssh.NewManager()

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			hostStatuses := make([]daemon.HostStatus, 0, len(cfg.Hosts))
			for _, host := range cfg.Hosts {
				slog.Info("connecting host", "name", host.Name, "ssh_host", host.SSHHost)
				status := "connected"
				if _, err := mgr.Connect(ctx, host); err != nil {
					slog.Warn("failed to connect host", "name", host.Name, "err", err)
					status = "disconnected"
				}
				if err := br.EnsureSession(host.Name); err != nil {
					slog.Warn("failed to ensure local session", "name", host.Name, "err", err)
				}
				hostStatuses = append(hostStatuses, daemon.HostStatus{Name: host.Name, Status: status})
			}

			if err := daemon.WriteStatus(paths.StatusFile(), &daemon.DaemonStatus{
				PID:   os.Getpid(),
				Hosts: hostStatuses,
			}); err != nil {
				slog.Warn("failed to write status file", "err", err)
			}

			var stopGateway func()
			if cfg.Gateway.Enabled {
				stop, err := startGatewayStack(ctx, cfg)
				if err != nil {
					slog.Error("gateway start failed", "err", err)
					return err
				}
				stopGateway = stop
			}

			if err := daemon.WriteReady(paths); err != nil {
				if stopGateway != nil {
					stopGateway()
				}
				return err
			}

			slog.Info("bmux serve ready", "hosts", len(cfg.Hosts), "socket", name, "gateway", cfg.Gateway.Enabled)

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
			select {
			case <-sigCh:
				slog.Info("shutting down")
			case <-ctx.Done():
			}

			if stopGateway != nil {
				stopGateway()
			}
			for _, host := range cfg.Hosts {
				if err := mgr.Disconnect(host.Name); err != nil {
					slog.Warn("disconnect error", "name", host.Name, "err", err)
				}
			}
			return nil
		},
	}
}
