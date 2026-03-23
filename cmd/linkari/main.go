package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var (
	version = "0.1.0"
	commit  = "dev"
	date    = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "linkari",
		Short:   "Webhook service for Android share → tmux bridge",
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
	}

	rootCmd.AddCommand(serveCmd())

	t := instrument(rootCmd, "linkari")
	err := rootCmd.Execute()
	t.emit(err)
	if err != nil {
		os.Exit(1)
	}
}

func serveCmd() *cobra.Command {
	var (
		port       int
		token      string
		session    string
		debug      bool
		firebaseSA string
		queueDB    string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the webhook HTTP server",
		Long: `Start the linkari HTTP server that accepts POST /share requests
from Android HTTP Shortcuts and routes them to tmux sessions.

Configuration via flags or environment variables:
  LINKARI_TOKEN        Bearer token for authentication
  LINKARI_PORT         Listen port (default 8080)
  LINKARI_TMUX_SESSION Target tmux session (default "android-share")
  LINKARI_QUEUE_DB     SQLite queue database path (default ~/.config/linkari/queue.db)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				token = os.Getenv("LINKARI_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("bearer token required: set --token or LINKARI_TOKEN")
			}

			if envPort := os.Getenv("LINKARI_PORT"); envPort != "" && !cmd.Flags().Changed("port") {
				fmt.Sscanf(envPort, "%d", &port)
			}

			if envSession := os.Getenv("LINKARI_TMUX_SESSION"); envSession != "" && !cmd.Flags().Changed("session") {
				session = envSession
			}

			// Resolve firebase service account path.
			if firebaseSA == "" {
				firebaseSA = os.Getenv("LINKARI_FIREBASE_SA")
			}

			var fcmTokenSource oauth2.TokenSource
			if firebaseSA != "" {
				saJSON, err := os.ReadFile(firebaseSA)
				if err != nil {
					return fmt.Errorf("reading firebase service account: %w", err)
				}
				creds, err := google.CredentialsFromJSON(context.Background(), saJSON,
					"https://www.googleapis.com/auth/firebase.messaging",
				)
				if err != nil {
					return fmt.Errorf("parsing firebase credentials: %w", err)
				}
				fcmTokenSource = creds.TokenSource
			}

			// Resolve queue database path.
			if queueDB == "" {
				queueDB = os.Getenv("LINKARI_QUEUE_DB")
			}
			if queueDB == "" {
				home, _ := os.UserHomeDir()
				queueDB = home + "/.config/linkari/queue.db"
			}
			if err := os.MkdirAll(filepath.Dir(queueDB), 0755); err != nil {
				return fmt.Errorf("creating queue db directory: %w", err)
			}

			queue, err := NewQueue(queueDB, debug)
			if err != nil {
				return fmt.Errorf("opening queue: %w", err)
			}
			defer queue.Close()

			ring := NewRingLog(100)
			log.SetOutput(ring.Writer())
			if debug {
				log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
				log.Printf("[DEBUG] config: port=%d session=%q debug=true firebase_sa=%q queue_db=%q", port, session, firebaseSA, queueDB)
			}

			tmux := &TmuxRunner{DefaultSession: session, Debug: debug}
			router := NewRouter(tmux, debug, token, port)
			srv := NewServer(token, router, queue, ring, debug, fcmTokenSource)
			if fcmTokenSource != nil {
				log.Printf("FCM push notifications enabled (sa=%s)", firebaseSA)
			} else {
				log.Printf("FCM push notifications disabled (no firebase SA configured)")
			}

			log.Printf("queue enabled (db=%s)", queueDB)
			StartReplay(queue, router, tmux, 30*time.Second, debug)

			httpServer := &http.Server{
				Addr:         fmt.Sprintf(":%d", port),
				Handler:      srv.Mux(),
				ReadTimeout:  5 * time.Second,
				WriteTimeout: 10 * time.Second,
				IdleTimeout:  60 * time.Second,
			}

			errCh := make(chan error, 1)
			go func() {
				log.Printf("linkari listening on :%d (session=%s)", port, session)
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
	cmd.Flags().StringVar(&token, "token", "", "bearer token for authentication (or LINKARI_TOKEN)")
	cmd.Flags().StringVar(&session, "session", "android-share", "target tmux session name")
	cmd.Flags().BoolVar(&debug, "debug", false, "enable debug logging to stdout")
	cmd.Flags().StringVar(&firebaseSA, "firebase-sa", "", "path to Firebase service account JSON (or LINKARI_FIREBASE_SA)")
	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")

	return cmd
}
