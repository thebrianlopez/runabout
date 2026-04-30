package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"tailscale.com/tsnet"
)

var (
	version = "0.1.0"
	commit  = "dev"
	date    = "unknown"
)

var (
	dbPath   string
	stateDir string
	debug    bool
)

// tsnetLike abstracts *tsnet.Server for testability.
type tsnetLike interface {
	Up(ctx context.Context) error
	HTTPClient() *http.Client
	Listen(network, addr string) (net.Listener, error)
	Close() error
}

// tsnetAdapter adapts *tsnet.Server (whose Up returns (*netip.AddrPort, error)) to tsnetLike.
type tsnetAdapter struct{ s *tsnet.Server }

func (a *tsnetAdapter) Up(ctx context.Context) error                        { _, err := a.s.Up(ctx); return err }
func (a *tsnetAdapter) HTTPClient() *http.Client                            { return a.s.HTTPClient() }
func (a *tsnetAdapter) Listen(network, addr string) (net.Listener, error)   { return a.s.Listen(network, addr) }
func (a *tsnetAdapter) Close() error                                         { return a.s.Close() }

func newTsnetServer() tsnetLike {
	s := &tsnet.Server{
		Hostname: "plaid-service",
		AuthKey:  getenv("TS_AUTHKEY"),
		Dir:      filepath.Join(stateDir, "tsnet"),
	}
	return &tsnetAdapter{s}
}

func main() {
	rootCmd := &cobra.Command{
		Use:     "plaid-service",
		Short:   "Plaid bank sync daemon — polls transactions, stores in SQLite, emits events",
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
	}

	home, _ := os.UserHomeDir()
	defaultDB := filepath.Join(home, ".plaid-service", "plaid.db")
	defaultState := filepath.Join(home, ".plaid-service")

	rootCmd.PersistentFlags().StringVar(&dbPath, "db", defaultDB, "path to SQLite database")
	rootCmd.PersistentFlags().StringVar(&stateDir, "state-dir", defaultState, "directory for tsnet state and config")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "enable debug logging")

	rootCmd.AddCommand(serveCmd())
	rootCmd.AddCommand(linkStartCmd())
	rootCmd.AddCommand(linkCompleteCmd())

	t := instrument(rootCmd, "plaid-service")
	err := rootCmd.Execute()
	t.emit(err)
	if err != nil {
		os.Exit(1)
	}
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the scheduler daemon (polls all linked items on cron schedule)",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := openDB(dbPath)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer db.Close()

			secrets, err := newTokenStore(cmd.Context())
			if err != nil {
				return fmt.Errorf("init secrets: %w", err)
			}

			ts := newTsnetServer()
			if err := ts.Up(cmd.Context()); err != nil {
				return fmt.Errorf("tailnet join failed: %w", err)
			}
			defer ts.Close()

			ln, err := ts.Listen("tcp", ":80")
			if err != nil {
				return fmt.Errorf("health listener: %w", err)
			}
			go http.Serve(ln, healthHandler(db))

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			go runWeeklyPrune(ctx, db)

			client := newPlaidClient(ts.HTTPClient(), secrets, db)
			sched := newScheduler(db, client, secrets)

			if err := sched.Start(); err != nil {
				return fmt.Errorf("start scheduler: %w", err)
			}
			defer sched.Stop()

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
			<-sig

			return nil
		},
	}
}

func linkStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link-start",
		Short: "Create a Plaid Link token and print the one-time URL",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := openDB(dbPath)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer db.Close()

			secrets, err := newTokenStore(cmd.Context())
			if err != nil {
				return fmt.Errorf("init secrets: %w", err)
			}

			ts := newTsnetServer()
			if err := ts.Up(cmd.Context()); err != nil {
				return fmt.Errorf("tailnet join failed: %w", err)
			}
			defer ts.Close()

			client := newPlaidClient(ts.HTTPClient(), secrets, db)
			return runLinkStart(cmd.Context(), db, client)
		},
	}
}

func linkCompleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link-complete <public_token>",
		Short: "Exchange a public token for access token and register the item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := openDB(dbPath)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer db.Close()

			secrets, err := newTokenStore(cmd.Context())
			if err != nil {
				return fmt.Errorf("init secrets: %w", err)
			}

			ts := newTsnetServer()
			if err := ts.Up(cmd.Context()); err != nil {
				return fmt.Errorf("tailnet join failed: %w", err)
			}
			defer ts.Close()

			client := newPlaidClient(ts.HTTPClient(), secrets, db)
			return runLinkComplete(cmd.Context(), db, client, secrets, args[0])
		},
	}
}
