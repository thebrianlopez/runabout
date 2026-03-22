package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var (
	version = "0.1.0"
	commit  = "dev"
	date    = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "sharehook",
		Short:   "Webhook service for Android share → tmux bridge",
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
	}

	rootCmd.AddCommand(serveCmd())

	t := instrument(rootCmd, "sharehook")
	err := rootCmd.Execute()
	t.emit(err)
	if err != nil {
		os.Exit(1)
	}
}

func serveCmd() *cobra.Command {
	var (
		port    int
		token   string
		session string
		debug   bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the webhook HTTP server",
		Long: `Start the sharehook HTTP server that accepts POST /share requests
from Android HTTP Shortcuts and routes them to tmux sessions.

Configuration via flags or environment variables:
  SHAREHOOK_TOKEN        Bearer token for authentication
  SHAREHOOK_PORT         Listen port (default 8080)
  SHAREHOOK_TMUX_SESSION Target tmux session (default "android-share")`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				token = os.Getenv("SHAREHOOK_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("bearer token required: set --token or SHAREHOOK_TOKEN")
			}

			if envPort := os.Getenv("SHAREHOOK_PORT"); envPort != "" && !cmd.Flags().Changed("port") {
				fmt.Sscanf(envPort, "%d", &port)
			}

			if envSession := os.Getenv("SHAREHOOK_TMUX_SESSION"); envSession != "" && !cmd.Flags().Changed("session") {
				session = envSession
			}

			ring := NewRingLog(100)
			log.SetOutput(ring.Writer())
			if debug {
				log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
				log.Printf("[DEBUG] config: port=%d session=%q debug=true", port, session)
			}

			tmux := &TmuxRunner{DefaultSession: session, Debug: debug}
			router := NewRouter(tmux, debug)
			srv := NewServer(token, router, ring, debug)

			httpServer := &http.Server{
				Addr:         fmt.Sprintf(":%d", port),
				Handler:      srv.Mux(),
				ReadTimeout:  5 * time.Second,
				WriteTimeout: 10 * time.Second,
				IdleTimeout:  60 * time.Second,
			}

			errCh := make(chan error, 1)
			go func() {
				log.Printf("sharehook listening on :%d (session=%s)", port, session)
				errCh <- httpServer.ListenAndServe()
			}()

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

			select {
			case <-sig:
				log.Println("shutting down...")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return httpServer.Shutdown(ctx)
			case err := <-errCh:
				if err != http.ErrServerClosed {
					return err
				}
				return nil
			}
		},
	}

	cmd.Flags().IntVar(&port, "port", 8080, "listen port")
	cmd.Flags().StringVar(&token, "token", "", "bearer token for authentication (or SHAREHOOK_TOKEN)")
	cmd.Flags().StringVar(&session, "session", "android-share", "target tmux session name")
	cmd.Flags().BoolVar(&debug, "debug", false, "enable debug logging to stdout")

	return cmd
}
