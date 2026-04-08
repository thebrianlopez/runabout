package main

import (
	"context"
	"fmt"
	"io"
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
	rootCmd.AddCommand(scoreCmd())
	rootCmd.AddCommand(searchCmd())
	rootCmd.AddCommand(backfillCmd())
	rootCmd.AddCommand(digestCmd())
	rootCmd.AddCommand(evalCmd())
	rootCmd.AddCommand(triageCmd())
	rootCmd.AddCommand(profileCmd())
	rootCmd.AddCommand(completionCmd(rootCmd))

	registerCompletions(rootCmd)

	t := instrument(rootCmd, "linkari")
	err := rootCmd.Execute()
	t.emit(err)
	if err != nil {
		os.Exit(1)
	}
}

func serveCmd() *cobra.Command {
	var (
		port          int
		token         string
		debug         bool
		firebaseSA    string
		queueDB       string
		tlsEnabled    bool
		certFile      string
		keyFile       string
		tsnetEnabled  bool
		tsnetHostname  string
		tsnetStateDir  string
		tsnetAuthKey   string
		notifyMinScore int
		shell          string
		shellArgs      string
		configFile     string
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
  LINKARI_NOTIFY_MIN_SCORE  Minimum score for /notify FCM push (default: per-profile threshold)
  LINKARI_LOG_FILE     Append all log output to this file path
  LINKARI_TLS          Enable TLS when set to "1" or "true"
  LINKARI_CERT_FILE    TLS certificate PEM path (default ~/.config/linkari/cert.pem)
  LINKARI_KEY_FILE     TLS private key PEM path (default ~/.config/linkari/key.pem)
  LINKARI_SHELL        Shell binary for tmux windows (default fish)
  LINKARI_SHELL_ARGS   Shell command flag (default -c)

Tailscale Funnel (--tsnet):
  LINKARI_TSNET            Enable Tailscale Funnel when set to "1" or "true"
  LINKARI_TSNET_HOSTNAME   Tailscale node hostname (default "linkari")
  LINKARI_TSNET_STATE_DIR  tsnet state directory (default ~/.config/linkari/tsnet)

When --tsnet is set, linkari serves on BOTH the local port (plain HTTP, for
localhost debug) and via Tailscale Funnel (HTTPS, public Android ingress).
TLS is handled by Tailscale; no cert management required.

On first run with --tsnet, an auth URL is printed to complete Tailscale login.
For unattended startup, set TS_AUTHKEY in the environment.`,
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

			// tsnet env fallbacks.
			if !tsnetEnabled {
				tsnetEnabled = os.Getenv("LINKARI_TSNET") == "1" || os.Getenv("LINKARI_TSNET") == "true"
			}
			if tsnetHostname == "" {
				if h := os.Getenv("LINKARI_TSNET_HOSTNAME"); h != "" {
					tsnetHostname = h
				}
			}
			if tsnetAuthKey == "" {
				tsnetAuthKey = os.Getenv("TS_AUTHKEY")
			}
			if tsnetStateDir == "" {
				tsnetStateDir = os.Getenv("LINKARI_TSNET_STATE_DIR")
			}
			if tsnetStateDir == "" {
				tsnetStateDir = filepath.Join(configDir, "tsnet")
			}
			if tsnetEnabled {
				if err := os.MkdirAll(tsnetStateDir, 0700); err != nil {
					return fmt.Errorf("creating tsnet state dir: %w", err)
				}
			}
			if tsnetEnabled && tlsEnabled {
				log.Printf("WARN: --tls and --tsnet both set; --tls applies only to the local listener")
			}

			// Notify min score env fallback.
			if notifyMinScore == 0 {
				if v := os.Getenv("LINKARI_NOTIFY_MIN_SCORE"); v != "" {
					fmt.Sscanf(v, "%d", &notifyMinScore)
				}
			}

			queue, err := NewQueue(queueDB, debug)
			if err != nil {
				return fmt.Errorf("opening queue: %w", err)
			}
			defer queue.Close()

			ring := NewRingLog(100)
			logWriter := ring.Writer()

			// Optional file logging via LINKARI_LOG_FILE.
			logFilePath := os.Getenv("LINKARI_LOG_FILE")
			var logFile *os.File
			if logFilePath != "" {
				if err := os.MkdirAll(filepath.Dir(logFilePath), 0755); err != nil {
					return fmt.Errorf("creating log file directory: %w", err)
				}
				f, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
				if err != nil {
					return fmt.Errorf("opening log file: %w", err)
				}
				logFile = f
				logWriter = io.MultiWriter(logWriter, f)
			}
			defer func() {
				if logFile != nil {
					logFile.Close()
				}
			}()

			log.SetOutput(logWriter)
			if debug {
				log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
				log.Printf("[DEBUG] config: port=%d debug=true firebase_sa=%q queue_db=%q", port, firebaseSA, queueDB)
			}

			// Resolve shell from flag → env → default.
			if shell == "" {
				shell = os.Getenv("LINKARI_SHELL")
			}
			if shellArgs == "" {
				shellArgs = os.Getenv("LINKARI_SHELL_ARGS")
			}
			tmux := &TmuxRunner{Debug: debug, Shell: shell, ShellArgs: shellArgs}

			// Load action config if available.
			if configFile == "" {
				configFile = os.Getenv("LINKARI_CONFIG")
			}
			var router *Router
			cfg, cfgErr := LoadConfig(configFile)
			if cfgErr != nil {
				if configFile != "" {
					// Explicit config path was set — fail if it can't be loaded.
					return fmt.Errorf("load config: %w", cfgErr)
				}
				log.Printf("no action config found, using built-in defaults: %v", cfgErr)
				router = NewRouterFromConfig(tmux, builtinConfig(), debug)
			} else {
				log.Printf("loaded %d actions from config", len(cfg.Actions))
				router = NewRouterFromConfig(tmux, cfg, debug)

				// EPIC-042 M7: apply [server] section as the lowest-precedence
				// fallback layer. Flag > env > config > default. Anything that
				// is still zero-valued at this point gets the config value.
				if cfg.Server.NotifyMinScore != 0 && notifyMinScore == 0 {
					notifyMinScore = cfg.Server.NotifyMinScore
				}
				if cfg.Server.Shell != "" && tmux.Shell == "" {
					tmux.Shell = cfg.Server.Shell
				}
				if cfg.Server.ShellArgs != "" && tmux.ShellArgs == "" {
					tmux.ShellArgs = cfg.Server.ShellArgs
				}
				if cfg.Server.LogFile != "" && logFilePath == "" {
					log.Printf("config server.log_file: %s (note: file logging applied at startup; restart to take effect)", cfg.Server.LogFile)
				}
				// server_url is consumed by fish callbacks via /actions or env;
				// surface it in the log so operators can verify what shipped.
				if cfg.Server.ServerURL != "" {
					log.Printf("config server.server_url: %s (advertised to clients)", cfg.Server.ServerURL)
				}
			}
			// Validate debug fault-injection env var before binding; fatal on bad value.
			if code := ValidateRegisterFaultEnv(); code != 0 {
				log.Printf("WARN: %s=%d active — POST /register will short-circuit with %d (debug only)", registerFaultEnv, code, code)
			}
			srv := NewServer(token, router, queue, ring, debug, fcmTokenSource)
			srv.notifyMinScore = notifyMinScore

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
			if notifyMinScore > 0 {
				log.Printf("notify min score override: %d", notifyMinScore)
			}

			log.Printf("queue enabled (db=%s)", queueDB)
			StartReplay(queue, router, tmux, 30*time.Second, debug)
			srv.StartPushWorker(cmd.Context())

			httpServer := &http.Server{
				Addr:         fmt.Sprintf(":%d", port),
				Handler:      srv.Mux(),
				ReadTimeout:  5 * time.Second,
				WriteTimeout: 10 * time.Second,
				IdleTimeout:  60 * time.Second,
			}

			errCh := make(chan error, 2)

			// Start local listener.
			go func() {
				if tlsEnabled {
					log.Printf("linkari listening on :%d (local, TLS)", port)
					errCh <- httpServer.ListenAndServeTLS(certFile, keyFile)
				} else {
					log.Printf("linkari listening on :%d (local)", port)
					errCh <- httpServer.ListenAndServe()
				}
			}()

			// Start tsnet Funnel listener if enabled.
			var tsnetSrv *TsnetServer
			var tsnetHTTPServer *http.Server

			if tsnetEnabled {
				tsnetSrv = NewTsnetServer(TsnetConfig{
					Hostname: tsnetHostname,
					StateDir: tsnetStateDir,
					AuthKey:  tsnetAuthKey,
					Debug:    debug,
				})
				ln, err := tsnetSrv.Start(cmd.Context())
				if err != nil {
					log.Printf("WARN: tsnet failed to start: %v — continuing with local listener only", err)
				} else {
					srv.SetTsnetAddr(tsnetSrv.FQDN())
					tsnetHTTPServer = &http.Server{
						Handler:      srv.FunnelMux(),
						ReadTimeout:  10 * time.Second,
						WriteTimeout: 30 * time.Second,
						IdleTimeout:  120 * time.Second,
					}
					go func() {
						errCh <- tsnetHTTPServer.Serve(ln)
					}()
				}
			}

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

			for {
				select {
				case s := <-sig:
					if s == syscall.SIGHUP {
						// Hot-reload action config.
						newCfg, reloadErr := LoadConfig(configFile)
						if reloadErr != nil {
							log.Printf("SIGHUP: config reload failed: %v", reloadErr)
							continue
						}
						router.Reload(newCfg)
						log.Printf("SIGHUP: reloaded %d actions from config", len(newCfg.Actions))
						continue
					}
					log.Println("shutting down...")
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()

					if tsnetHTTPServer != nil {
						if err := tsnetHTTPServer.Shutdown(ctx); err != nil {
							log.Printf("tsnet HTTP shutdown: %v", err)
						}
					}
					if tsnetSrv != nil {
						if err := tsnetSrv.Close(); err != nil {
							log.Printf("tsnet close: %v", err)
						}
					}

					return httpServer.Shutdown(ctx)
				case err := <-errCh:
					if err != nil && err != http.ErrServerClosed {
						return err
					}
					return nil
				}
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
	cmd.Flags().BoolVar(&tsnetEnabled, "tsnet", false, "enable Tailscale Funnel listener (or LINKARI_TSNET=1)")
	cmd.Flags().StringVar(&tsnetHostname, "tsnet-hostname", "linkari", "Tailscale node hostname (or LINKARI_TSNET_HOSTNAME)")
	cmd.Flags().StringVar(&tsnetStateDir, "tsnet-state-dir", "", "tsnet state directory (default ~/.config/linkari/tsnet, or LINKARI_TSNET_STATE_DIR)")
	cmd.Flags().StringVar(&tsnetAuthKey, "tsnet-authkey", "", "Tailscale auth key (or TS_AUTHKEY env)")
	cmd.Flags().IntVar(&notifyMinScore, "notify-min-score", 0, "minimum score for /notify FCM push (0 = use per-profile default, or LINKARI_NOTIFY_MIN_SCORE)")
	cmd.Flags().StringVar(&shell, "shell", "", "shell binary for tmux windows (default fish, or LINKARI_SHELL)")
	cmd.Flags().StringVar(&shellArgs, "shell-args", "", "shell command flag for tmux windows (default -c, or LINKARI_SHELL_ARGS)")
	cmd.Flags().StringVar(&configFile, "config", "", "path to actions.yaml config (default ~/.config/linkari/actions.yaml, or LINKARI_CONFIG)")

	return cmd
}
