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
		debug      bool
		firebaseSA string
		queueDB    string
		tlsEnabled bool
		certFile   string
		keyFile    string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the webhook HTTP server",
		Long: `Start the linkari HTTP server that accepts POST /share requests
from Android HTTP Shortcuts and routes them to tmux sessions.

Configuration via flags or environment variables:
  LINKARI_TOKEN        Bearer token for authentication
  LINKARI_PORT         Listen port (default 8080)
  LINKARI_QUEUE_DB     SQLite queue database path (default ~/.config/linkari/queue.db)
  LINKARI_TLS          Enable TLS when set to "1" or "true"
  LINKARI_CERT_FILE    TLS certificate PEM path (default ~/.config/linkari/cert.pem)
  LINKARI_KEY_FILE     TLS private key PEM path (default ~/.config/linkari/key.pem)`,
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

			// TLS env fallbacks (flags take precedence when explicitly set).
			if !tlsEnabled {
				tlsEnabled = os.Getenv("LINKARI_TLS") == "1" || os.Getenv("LINKARI_TLS") == "true"
			}
			configDir := filepath.Dir(queueDB)
			if certFile == "" {
				certFile = os.Getenv("LINKARI_CERT_FILE")
			}
			if certFile == "" {
				certFile = filepath.Join(configDir, "cert.pem")
			}
			if keyFile == "" {
				keyFile = os.Getenv("LINKARI_KEY_FILE")
			}
			if keyFile == "" {
				keyFile = filepath.Join(configDir, "key.pem")
			}
			if tlsEnabled {
				if _, err := os.Stat(certFile); err != nil {
					return fmt.Errorf("TLS cert file not found: %s (run: mkcert -cert-file %s -key-file %s localhost 127.0.0.1)", certFile, certFile, keyFile)
				}
				if _, err := os.Stat(keyFile); err != nil {
					return fmt.Errorf("TLS key file not found: %s (run: mkcert -cert-file %s -key-file %s localhost 127.0.0.1)", keyFile, certFile, keyFile)
				}
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
				log.Printf("[DEBUG] config: port=%d debug=true firebase_sa=%q queue_db=%q", port, firebaseSA, queueDB)
			}

			tmux := &TmuxRunner{Debug: debug}
			router := NewRouter(tmux, debug, token, port)
			srv := NewServer(token, router, queue, ring, debug, fcmTokenSource)

			// Event logging — append to logs/ next to queue db.
			eventsPath := filepath.Join(filepath.Dir(queueDB), "linkari_events.jsonl")
			events, err := NewEventLogger(eventsPath)
			if err != nil {
				log.Printf("WARN: event logger disabled: %v", err)
			} else {
				srv.events = events
				log.Printf("event logging enabled (path=%s)", eventsPath)
			}
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
				if tlsEnabled {
					log.Printf("linkari listening on :%d (TLS)", port)
					errCh <- httpServer.ListenAndServeTLS(certFile, keyFile)
				} else {
					log.Printf("linkari listening on :%d", port)
					errCh <- httpServer.ListenAndServe()
				}
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
	cmd.Flags().BoolVar(&debug, "debug", false, "enable debug logging to stdout")
	cmd.Flags().StringVar(&firebaseSA, "firebase-sa", "", "path to Firebase service account JSON (or LINKARI_FIREBASE_SA)")
	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().BoolVar(&tlsEnabled, "tls", false, "enable TLS (requires mkcert-generated cert/key, or LINKARI_TLS=1)")
	cmd.Flags().StringVar(&certFile, "cert-file", "", "TLS certificate PEM (default ~/.config/linkari/cert.pem, or LINKARI_CERT_FILE)")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "TLS private key PEM (default ~/.config/linkari/key.pem, or LINKARI_KEY_FILE)")

	return cmd
}
