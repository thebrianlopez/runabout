package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/blo-grindr/runabout/cmd/linkari/internal/linklog"
	"github.com/blo-grindr/runabout/cmd/linkari/internal/secrets"
	"github.com/blo-grindr/runabout/cmd/linkari/internal/xdgpath"
)

// provenanceEntry buffers a (field, source, fingerprint, tier) tuple resolved
// before log.SetOutput is called. Flushed via flushProvenance once the
// configured log sink is wired so lines land in the ring/log-file.
//
// Format pinned by EPIC-047 locked decision #8:
//
//	linkari: secret <field> resolved from <source> fp=<8-hex> tier=<tier>
type provenanceEntry struct {
	field  string
	source string
	fp     string
	tier   string
}

func flushProvenance(entries []provenanceEntry) {
	for _, e := range entries {
		log.Printf("linkari: secret %s resolved from %s fp=%s tier=%s",
			e.field, e.source, e.fp, e.tier)
	}
}

// recordProvenance appends a provenance entry for a non-empty resolved value.
// Empty values (default tier short-circuit) skip emission per locked decision #4.
func recordProvenance(entries []provenanceEntry, field, value, tier string, src secrets.Source) []provenanceEntry {
	if value == "" {
		return entries
	}
	return append(entries, provenanceEntry{
		field:  field,
		source: src.String(),
		fp:     secrets.Fingerprint(value),
		tier:   tier,
	})
}

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
	rootCmd.AddCommand(configCmd())
	rootCmd.AddCommand(doctorCmd())
	rootCmd.AddCommand(scoreCmd())
	rootCmd.AddCommand(scoreWriteCmd())
	rootCmd.AddCommand(searchCmd())
	rootCmd.AddCommand(backfillCmd())
	rootCmd.AddCommand(tagBackfillCmd())
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
		port            int
		token           string
		jiraToken       string
		jiraAPIUsername  string
		jiraAPIPassword string
		jiraDomain      string
		pagerDutyToken  string
		debug         bool
		firebaseSA    string
		queueDB       string
		tlsEnabled    bool
		certFile      string
		keyFile       string
		tsnetEnabled  bool
		localEnabled  bool
		tsnetHostname  string
		tsnetStateDir  string
		tsnetAuthKey   string
		notifyMinScore int
		shell          string
		shellArgs      string
		configFile     string
		detach         bool
		logFormat      string
		logLevel       string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the webhook HTTP server",
		Long: `Start the linkari HTTP server that accepts POST /share requests
from Android HTTP Shortcuts and routes them to tmux sessions.

Zero-flag production boot (requires ~/.config/linkari/server.yaml):
  linkari serve

Local-only dev mode (skips Tailscale Funnel):
  linkari serve --local

Configuration via flags, environment variables, or server.yaml:
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

Tailscale Funnel (on by default):
  LINKARI_LOCAL            Disable tsnet and use local listener only ("1" or "true")
  LINKARI_TSNET            Override tsnet enable/disable ("1" or "true" to force on)
  LINKARI_TSNET_HOSTNAME   Tailscale node hostname (default "linkari")
  LINKARI_TSNET_STATE_DIR  tsnet state directory (default ~/.config/linkari/tsnet)

linkari serves on BOTH the local port (plain HTTP, localhost debug) and via
Tailscale Funnel (HTTPS, public Android ingress). TLS is handled by Tailscale;
no cert management required.

If no tsnet_authkey is resolvable and tsnet was not explicitly enabled, linkari
falls back to local-only mode with a WARN log (safe for fresh-clone dev laptops).

On first tsnet run an auth URL is printed to complete Tailscale login.
For unattended startup set TS_AUTHKEY or server.yaml tsnet_authkey.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// EPIC-049 M3: --detach re-execs the binary without --detach,
			// setsid, waits for child ready signal, writes PID file, exits parent.
			// maybeDetach calls os.Exit(0) from parent; child returns nil here.
			if err := maybeDetach(detach); err != nil {
				return err
			}

			// EPIC-047 M3: load ~/.config/linkari/server.yaml (new file wins).
			// If absent, fall through to actions.yaml[server:] (deprecated).
			serverFilePath := defaultServerConfigPath()
			serverFileCfg, err := LoadServerFile(serverFilePath)
			if err != nil {
				return fmt.Errorf("load server.yaml: %w", err)
			}

			// Build the resolver early — it lazily wires AWS SDK on first
			// secretsmanager:// URI, so cost is zero when no SM URIs are used.
			resolver := secrets.New(secrets.DefaultAWSFactory())

			var provenance []provenanceEntry

			// Helper closure: resolve a single field through the pipeline and
			// record provenance. Hard-fail on resolver error (locked decision #2).
			resolveField := func(field, flag, env, def string, yamlVal func(*ServerConfig) string) (string, error) {
				var y string
				if serverFileCfg != nil {
					y = yamlVal(serverFileCfg)
				}
				v, tier, src, err := resolveServerField(ctx, resolver, flag, env, y, def)
				if err != nil {
					return "", fmt.Errorf("resolve %s: %w", field, err)
				}
				provenance = recordProvenance(provenance, field, v, tier, src)
				return v, nil
			}

			// token: flag > LINKARI_TOKEN > server.yaml.token > (no default; required)
			token, err = resolveField("token", token, os.Getenv("LINKARI_TOKEN"), "",
				func(s *ServerConfig) string { return s.Token })
			if err != nil {
				return err
			}
			if token == "" {
				return fmt.Errorf("bearer token required: set --token, LINKARI_TOKEN, or server.yaml token")
			}

			// EPIC-057: jira_token — optional scoped bearer for ginit_* actions.
			// flag > LINKARI_JIRA_TOKEN > server.yaml.jira_token > (empty = disabled)
			jiraToken, _ = resolveField("jira_token", jiraToken, os.Getenv("LINKARI_JIRA_TOKEN"), "",
				func(s *ServerConfig) string { return s.JiraToken })

			// Outbound Jira API + PagerDuty credentials (linkari/jira-webhook secret).
			// All optional — empty = integration disabled.
			jiraAPIUsername, _ = resolveField("jira_api_username", "", os.Getenv("LINKARI_JIRA_API_USERNAME"), "",
				func(s *ServerConfig) string { return s.JiraAPIUsername })
			jiraAPIPassword, _ = resolveField("jira_api_password", "", os.Getenv("LINKARI_JIRA_API_PASSWORD"), "",
				func(s *ServerConfig) string { return s.JiraAPIPassword })
			jiraDomain, _ = resolveField("jira_domain", "", os.Getenv("LINKARI_JIRA_DOMAIN"), "",
				func(s *ServerConfig) string { return s.JiraDomain })
			pagerDutyToken, _ = resolveField("pagerduty_token", "", os.Getenv("LINKARI_PAGERDUTY_TOKEN"), "",
				func(s *ServerConfig) string { return s.PagerDutyToken })

			if envPort := os.Getenv("LINKARI_PORT"); envPort != "" && !cmd.Flags().Changed("port") {
				fmt.Sscanf(envPort, "%d", &port)
			}

			// firebase_sa: flag > LINKARI_FIREBASE_SA > server.yaml.firebase_sa
			// Pre-resolve so we can detect whether the resolved value is a path
			// (literal/env/flag) or JSON content (secretsmanager://) and
			// materialize to cache in the latter case (EPIC-047 M4).
			{
				var (
					yamlVal string
					tier    string
					src     secrets.Source
					value   string
				)
				if serverFileCfg != nil {
					yamlVal = serverFileCfg.FirebaseSA
				}
				value, tier, src, err = resolveServerField(ctx, resolver, firebaseSA, os.Getenv("LINKARI_FIREBASE_SA"), yamlVal, "")
				if err != nil {
					return fmt.Errorf("resolve firebase_sa: %w", err)
				}
				if value != "" && tier == "yaml-sm" {
					// Materialize JSON content into cache and treat the cache
					// path as the firebase service account file going forward.
					cacheDir, cacheErr := xdgpath.CacheDir()
					if cacheErr != nil {
						return fmt.Errorf("firebase_sa cache: %w", cacheErr)
					}
					cachePath := filepath.Join(cacheDir, "firebase-sa.json")
					if writeErr := os.WriteFile(cachePath, []byte(value), 0o600); writeErr != nil {
						return fmt.Errorf("firebase_sa write: %w", writeErr)
					}
					firebaseSA = cachePath
				} else {
					firebaseSA = value
				}
				provenance = recordProvenance(provenance, "firebase_sa", value, tier, src)
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
			// Only needed for --local --tls (no tsnet Funnel). When tsnet is
			// enabled (the default since EPIC-048), Tailscale handles TLS and
			// these PEM files are unused. To generate them for local TLS:
			//   mkcert -cert-file ~/.config/linkari/cert.pem \
			//          -key-file  ~/.config/linkari/key.pem  \
			//          localhost 127.0.0.1
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

			// EPIC-048 M2: resolve tsnet/local through the four-tier helper pipeline.
			// Two separate resolveBoolField calls reconciled via NOT.
			// tsnetExplicit is consumed by the M3 fallback rule.
			var yamlTsnet *bool
			if serverFileCfg != nil {
				yamlTsnet = serverFileCfg.Tsnet
			}
			localEnv := os.Getenv("LINKARI_LOCAL")
			tsnetEnv := os.Getenv("LINKARI_TSNET")
			tsnetResolved, _, _ := resolveBoolField(tsnetEnabled, cmd.Flags().Changed("tsnet"), tsnetEnv, yamlTsnet, true)
			localResolved, _, _ := resolveBoolField(localEnabled, cmd.Flags().Changed("local"), localEnv, nil, false)
			tsnetEnabled = tsnetResolved && !localResolved
			// tsnetExplicit: any layer explicitly opted in or out of tsnet.
			// Five inputs: cli-tsnet, cli-local, env-tsnet, env-local, yaml.
			tsnetExplicit := cmd.Flags().Changed("tsnet") || cmd.Flags().Changed("local") ||
				tsnetEnv != "" || localEnv != "" || yamlTsnet != nil

			// Resolve tsnet string tunables.
			var yamlTsnetHostname, yamlTsnetStateDir string
			if serverFileCfg != nil {
				yamlTsnetHostname = serverFileCfg.TsnetHostname
				yamlTsnetStateDir = serverFileCfg.TsnetStateDir
			}
			tsnetHostname, _, _ = resolveStringField(tsnetHostname, os.Getenv("LINKARI_TSNET_HOSTNAME"), yamlTsnetHostname, "linkari")
			// tsnet_authkey routes through the secret resolver pipeline (EPIC-047).
			tsnetAuthKey, err = resolveField("tsnet_authkey", tsnetAuthKey, os.Getenv("TS_AUTHKEY"), "",
				func(s *ServerConfig) string { return s.TSNetAuthKey })
			if err != nil {
				return err
			}
			// EPIC-048 M3: fallback-to-local rule. Fires before state-dir
			// creation so we skip the MkdirAll when falling back.
			// logger has no flags so the WARN string is golden-testable.
			{
				warnLogger := log.New(log.Default().Writer(), "", 0)
				tsnetEnabled = applyTsnetFallback(tsnetEnabled, tsnetExplicit, tsnetAuthKey, warnLogger)
			}
			tsnetStateDir, _, _ = resolveStringField(tsnetStateDir, os.Getenv("LINKARI_TSNET_STATE_DIR"), yamlTsnetStateDir, filepath.Join(configDir, "tsnet"))
			if tsnetEnabled {
				if err := os.MkdirAll(tsnetStateDir, 0700); err != nil {
					return fmt.Errorf("creating tsnet state dir: %w", err)
				}
			}
			if tsnetEnabled && tlsEnabled {
				slog.Warn("--tls and --tsnet both set; --tls applies only to the local listener")
			}

			// EPIC-048 M2: resolve notify_min_score and debug via the helper pipeline.
			var notifyYAML *int
			if serverFileCfg != nil && serverFileCfg.NotifyMinScore != 0 {
				notifyYAML = &serverFileCfg.NotifyMinScore
			}
			notifyMinScore, _, _ = resolveIntField(notifyMinScore, cmd.Flags().Changed("notify-min-score"), os.Getenv("LINKARI_NOTIFY_MIN_SCORE"), notifyYAML, 0)
			var yamlDebug *bool
			if serverFileCfg != nil && serverFileCfg.Debug {
				t := true
				yamlDebug = &t
			}
			debug, _, _ = resolveBoolField(debug, cmd.Flags().Changed("debug"), "", yamlDebug, false)

			queue, err := NewQueue(queueDB, debug)
			if err != nil {
				return fmt.Errorf("opening queue: %w", err)
			}
			defer queue.Close()

			ring := NewRingLog(100)
			logWriter := ring.Writer()

			// Optional file logging — no CLI flag; yaml+env only (EPIC-048).
			// INVARIANT (pinned by EPIC-048 M4): log_file MUST be fully resolved
			// into logWriter before log.SetOutput so flushProvenance lines land
			// in the configured sink. Do not move this block below log.SetOutput.
			// Relative paths resolve against the process cwd at invocation time
			// (same semantics as LINKARI_LOG_FILE env-var; use absolute paths in
			// server.yaml for operator clarity).
			var yamlLogFile string
			if serverFileCfg != nil {
				yamlLogFile = serverFileCfg.LogFile
			}
			logFilePath, _, _ := resolveStringField("", os.Getenv("LINKARI_LOG_FILE"), yamlLogFile, "")
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

			// EPIC-051 M1: install structured slog handler alongside stdlib log.
			// Both write to logWriter, so ring/log-file capture output from
			// either path during the migration. Level is a LevelVar so SIGHUP
			// can re-tune at runtime (EPIC-051 M3).
			lvlVar := new(slog.LevelVar)
			parsedLevel, lvlErr := linklog.ParseLevel(logLevel)
			if lvlErr != nil {
				return lvlErr
			}
			// --debug flag acts as a shorthand for --log-level=debug when
			// --log-level is unset. Preserved for backwards compat; removed
			// in EPIC-051 M3.
			if debug && logLevel == "" {
				parsedLevel = slog.LevelDebug
			}
			lvlVar.Set(parsedLevel)
			parsedFormat, fmtErr := linklog.ParseFormat(logFormat)
			if fmtErr != nil {
				return fmtErr
			}
			slogHandler := linklog.New(logWriter, linklog.Options{
				Level:   lvlVar,
				Format:  parsedFormat,
				Command: "linkari",
			})
			slog.SetDefault(slog.New(slogHandler))

			// EPIC-047 M4: flush buffered provenance lines into the configured
			// log sink so they land in ring/log-file (locked decision #7).
			flushProvenance(provenance)
			if debug {
				log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
				slog.Debug("startup config",
					"event_type", "startup_config",
					"port", port,
					"firebase_sa", firebaseSA,
					"queue_db", queueDB,
				)
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
				slog.Info("no action config found, using built-in defaults", "error", cfgErr)
				router = NewRouterFromConfig(tmux, builtinConfig(), debug)
			} else {
				slog.Info("loaded actions from config", "count", len(cfg.Actions))
				router = NewRouterFromConfig(tmux, cfg, debug)

				// EPIC-047 M3: deprecation warning when [server:] block in
				// actions.yaml is present. server.yaml is the new home; the
				// actions.yaml block remains as a back-compat fallback only.
				if !cfg.Server.IsZero() {
					slog.Warn("actions.yaml [server:] block is deprecated", "migrate_to", serverFilePath)
				}

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
					slog.Info("config server.log_file set (restart to take effect)", "log_file", cfg.Server.LogFile)
				}
				// server_url is consumed by fish callbacks via /actions or env;
				// surface it in the log so operators can verify what shipped.
				if cfg.Server.ServerURL != "" {
					slog.Info("config server.server_url advertised to clients", "server_url", cfg.Server.ServerURL)
				}
			}
			// Validate debug fault-injection env var before binding; fatal on bad value.
			if code := ValidateRegisterFaultEnv(); code != 0 {
				slog.Warn("register fault injection active (debug only)",
					"var", registerFaultEnv, "status_code", code,
				)
			}
			// EPIC-067: wire whisper model path for audio transcription.
			if router != nil && cfg != nil {
				router.SetWhisperModel(cfg.Server.WhisperModel)
			}

			// EPIC-038 M6: probe container runtime at startup; fall back to local
			// exec if the CRI socket is unreachable. SandboxConfig is zero-valued
			// (Enabled: false) when not configured, so LocalRuntime is the default.
			var sandboxCfg SandboxConfig
			if serverFileCfg != nil {
				sandboxCfg = serverFileCfg.Sandbox
			}
			_ = NewExecutionRuntimeWithPing(cmd.Context(), sandboxCfg)
			// EPIC-001: resolve google_client_id for Google Sign-In.
			var googleClientID string
			googleClientID, _ = resolveField("google_client_id", "", os.Getenv("LINKARI_GOOGLE_CLIENT_ID"), "",
				func(s *ServerConfig) string { return s.GoogleClientID })

			// EPIC-073: create shield middleware for Funnel client identity enforcement.
			var shield *Shield
			{
				shieldMode := "log" // default
				if serverFileCfg != nil {
					shieldMode = serverFileCfg.ShieldConfig()
				}
				shield = NewShield(shieldMode)
				slog.Info("shield started", "event_type", "shield_started", "mode", shieldMode)
			}

			srv := NewServer(token, router, queue, ring, debug, fcmTokenSource)
			srv.SetShield(shield)
			srv.jiraToken = jiraToken
			srv.jiraAPIUsername = jiraAPIUsername
			srv.jiraAPIPassword = jiraAPIPassword
			srv.jiraDomain = jiraDomain
			srv.pagerDutyToken = pagerDutyToken
			srv.notifyMinScore = notifyMinScore
			// EPIC-061: heuristic override is always on — auto-profile
			// actions require domain heuristics for profile classification.
			srv.shareHeuristicOverride = true

			// EPIC-001: seed static invite codes from server.yaml.
			if serverFileCfg != nil && len(serverFileCfg.InviteCodes) > 0 && queue != nil {
				n, err := queue.SeedInviteCodes(serverFileCfg.InviteCodes)
				if err != nil {
					slog.Error("seed invite codes", "error", err)
				} else if n > 0 {
					slog.Info("seeded invite codes", "new", n, "total", len(serverFileCfg.InviteCodes))
				}
			}

			// EPIC-001: wire Google Sign-In verifier when client ID is configured.
			if googleClientID != "" {
				srv.googleVerifier = NewGoogleTokenVerifier(googleClientID)
				slog.Info("google sign-in enabled", "client_id_len", len(googleClientID))
			}
			if serverFileCfg != nil && serverFileCfg.SessionTTLDays > 0 {
				srv.sessionTTLDays = serverFileCfg.SessionTTLDays
			}
			if serverFileCfg != nil {
				srv.SetBlocklist(serverFileCfg.Blocklist)
				srv.SetCORSOrigins(serverFileCfg.CORSOrigins)
			}

			// EPIC-051 M3/M4: install the live push config on the queue so
			// EnqueueDigestIfDue honors notify_min_score + per-profile
			// throttle durations for every writer path (HTTP + CLI).
			if queue != nil {
				pcfg := &PushConfig{NotifyMinScore: notifyMinScore}
				if serverFileCfg != nil {
					pcfg.DigestThrottle = serverFileCfg.Push.DigestThrottle.Durations()
					pcfg.DigestThrottleDefault = serverFileCfg.Push.DigestThrottleDefault.Duration()
				}
				queue.SetPushConfig(pcfg)
			}

			// Event logging — append to logs/ next to queue db.
			eventsPath := filepath.Join(filepath.Dir(queueDB), "linkari_events.jsonl")
			events, err := NewEventLogger(eventsPath)
			if err != nil {
				slog.Warn("event logger disabled", "error", err)
			} else {
				srv.events = events
				router.SetEvents(events) // EPIC-076 M1: wire into scoring goroutines
				slog.Info("event logging enabled", "path", eventsPath)
			}
			if fcmTokenSource != nil {
				slog.Info("FCM push notifications enabled", "firebase_sa", firebaseSA)
			} else {
				slog.Info("FCM push notifications disabled (no firebase SA configured)")
			}
			if notifyMinScore > 0 {
				slog.Info("notify min score override", "score", notifyMinScore)
			}

			slog.Info("queue enabled", "db", queueDB)
			StartReplay(queue, router, tmux, 30*time.Second, debug)
			srv.StartPushWorker(cmd.Context())

			// EPIC-054 M3: relayed-state watchdog. Reclassifies rows stuck in
			// `relayed` status past the configured max age as failed with
			// error_reason="scoring_timeout" and emits one event per row.
			// Config is hot-reloadable via SIGHUP.
			var relayedWatchdog *RelayedWatchdog
			if queue != nil {
				// Default through a zero-valued ServerConfig so RelayedWatchdog()
				// fills in UrlWorkDir from env/home and other EPIC-055 defaults.
				defaultSC := &ServerConfig{}
				wdCfg := defaultSC.RelayedWatchdog()
				if serverFileCfg != nil {
					wdCfg = serverFileCfg.RelayedWatchdog()
				}
				relayedWatchdog = NewRelayedWatchdog(queue, srv.events, wdCfg)
				go relayedWatchdog.Run(cmd.Context())
			}

			// Periodic VACUUM INTO snapshot — point-in-time recovery baseline
			// if queue.db becomes corrupt (2026-04-13 incident). Defaults to
			// 1h interval writing <queue_db>.bak; configurable via server.yaml.
			if queue != nil {
				sc := &ServerConfig{}
				if serverFileCfg != nil {
					sc = serverFileCfg
				}
				snapInterval, snapPath := sc.SnapshotConfig(queueDB)
				snapWorker := NewSnapshotWorker(queue, snapInterval, snapPath)
				go snapWorker.Run(cmd.Context())
			}

			// EPIC-080 M6: resolve claude binary path and vision model from config.
			initClaudeConfig(serverFileCfg)

			// EPIC-009 M2: wire yt-dlp path for YouTube transcription.
			if router != nil && serverFileCfg != nil {
				router.SetYtdlpPath(serverFileCfg.YtdlpPath)
			}

			// EPIC-080 M3: log auth key presence at startup for diagnostics.
			logHaikuEnvKeys()

			// When tsnet Funnel is active, bind the local listener to
			// 127.0.0.1 only — LAN exposure is unnecessary since the
			// Funnel provides the public ingress path (GAP-1).
			listenHost := ""
			if tsnetEnabled {
				listenHost = "127.0.0.1"
			}
			httpServer := &http.Server{
				Addr:         fmt.Sprintf("%s:%d", listenHost, port),
				Handler:      srv.Mux(),
				ReadTimeout:  5 * time.Second,
				WriteTimeout: 10 * time.Second,
				IdleTimeout:  60 * time.Second,
			}

			errCh := make(chan error, 2)

			// Start local listener using explicit net.Listen so we can signal
			// detach-ready to the parent process (EPIC-049 M3) after the port
			// is bound, before entering the accept loop.
			if tlsEnabled {
				slog.Info("linkari listening",
					"event_type", "listener_up",
					"port", port, "mode", "local", "tls", true,
				)
				go func() {
					errCh <- httpServer.ListenAndServeTLS(certFile, keyFile)
				}()
				// TLS: signal after starting goroutine (optimistic — port binds inside).
				signalDetachReady()
			} else {
				listenAddr := fmt.Sprintf("%s:%d", listenHost, port)
				ln, lnErr := net.Listen("tcp", listenAddr)
				if lnErr != nil {
					return fmt.Errorf("listen %s: %w", listenAddr, lnErr)
				}
				slog.Info("linkari listening",
					"event_type", "listener_up",
					"port", port, "mode", "local", "tls", false,
				)
				// Signal parent AFTER port is successfully bound.
				signalDetachReady()
				go func() {
					errCh <- httpServer.Serve(ln)
				}()
			}

			// Start tsnet Funnel listener if enabled.
			// tsnetClose is set only on successful tsnet bring-up.
			var tsnetClose func() error
			var tsnetHTTPServer *http.Server

			if tsnetEnabled {
				ln, cleanup, fqdn, err := tsnetStart(cmd.Context(), TsnetConfig{
					Hostname: tsnetHostname,
					StateDir: tsnetStateDir,
					AuthKey:  tsnetAuthKey,
					Debug:    debug,
				})
				if err != nil {
					slog.Warn("tsnet failed to start, continuing with local listener only", "error", err)
				} else {
					tsnetClose = cleanup
					srv.SetTsnetAddr(fqdn)
					tsnetHTTPServer = &http.Server{
						Handler:      srv.FunnelMux(),
						ReadTimeout:  10 * time.Second,
						WriteTimeout: 30 * time.Second,
						IdleTimeout:  120 * time.Second,
						ConnContext:  funnelConnContext,
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
						reloadStart := time.Now()

						// Capture prev shield mode before any reload.
						prevShieldMode := ""
						if shield != nil {
							prevShieldMode = shield.Mode()
						}

						// Hot-reload action config.
						newCfg, reloadErr := LoadConfig(configFile)
						if reloadErr != nil {
							slog.Error("SIGHUP config reload failed", "error", reloadErr)
							continue
						}
						router.Reload(newCfg)
						// EPIC-051 M6: also reload the package-level
						// archiveThreshold cache used by the CLI/server
						// scoring paths and refresh the push config on the
						// queue so throttle knobs can be retuned live.
						if reloadErr := ReloadArchiveThresholdConfig(); reloadErr != nil {
							slog.Error("SIGHUP archive threshold reload failed", "error", reloadErr)
						}

						var newShieldMode string
						var pushCfg *PushConfig
						var wdCfg RelayedWatchdogCfg
						if queue != nil {
							if sf, sfErr := LoadServerFile(serverFilePath); sfErr == nil && sf != nil {
								pushCfg = sf.PushConfig()
								queue.SetPushConfig(pushCfg)
								wdCfg = sf.RelayedWatchdog()
								if relayedWatchdog != nil {
									relayedWatchdog.SetConfig(wdCfg)
								}
								// EPIC-073: hot-reload shield mode.
								if shield != nil {
									newShieldMode = sf.ShieldConfig()
									shield.Reload(newShieldMode)
								}
							}
						}

						dur := time.Since(reloadStart)
						logAttrs := []any{
							"event_type", "config_reloaded",
							"trigger", "SIGHUP",
							"config_path", serverFilePath,
							"actions_count", len(newCfg.Actions),
							"duration_ms", dur.Milliseconds(),
						}
						if shield != nil {
							logAttrs = append(logAttrs,
								"shield_mode", newShieldMode,
								"shield_changed", newShieldMode != prevShieldMode,
								"shield_prev", prevShieldMode,
							)
						}
						if pushCfg != nil {
							logAttrs = append(logAttrs, "push_notify_min_score", pushCfg.NotifyMinScore)
						}
						logAttrs = append(logAttrs,
							"watchdog_interval", wdCfg.Interval,
							"watchdog_max_age", wdCfg.MaxAge,
						)
						slog.Info("config reloaded", logAttrs...)
						continue
					}
					slog.Info("shutting down")
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()

					if tsnetHTTPServer != nil {
						if err := tsnetHTTPServer.Shutdown(ctx); err != nil {
							slog.Error("tsnet HTTP shutdown failed", "error", err)
						}
					}
					if tsnetClose != nil {
						if err := tsnetClose(); err != nil {
							slog.Error("tsnet close failed", "error", err)
						}
					}

					return httpServer.Shutdown(ctx)
				case err := <-errCh:
					if err != nil && err != http.ErrServerClosed {
						return err
					}
					return nil
				case <-cmd.Context().Done():
					// Context cancelled — integration tests use this for clean shutdown.
					slog.Info("shutting down (context cancelled)")
					shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer shutdownCancel()
					if tsnetHTTPServer != nil {
						if err := tsnetHTTPServer.Shutdown(shutdownCtx); err != nil {
							slog.Error("tsnet HTTP shutdown failed", "error", err)
						}
					}
					if tsnetClose != nil {
						if err := tsnetClose(); err != nil {
							slog.Error("tsnet close failed", "error", err)
						}
					}
					return httpServer.Shutdown(shutdownCtx)
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
	cmd.Flags().BoolVar(&tsnetEnabled, "tsnet", true, "enable Tailscale Funnel (default: true; use --local to disable, or LINKARI_TSNET=1)")
	cmd.Flags().BoolVar(&localEnabled, "local", false, "force local-only listener, disables tsnet (or LINKARI_LOCAL=1)")
	cmd.Flags().StringVar(&tsnetHostname, "tsnet-hostname", "", "Tailscale node hostname (default: linkari, or LINKARI_TSNET_HOSTNAME)")
	cmd.Flags().StringVar(&tsnetStateDir, "tsnet-state-dir", "", "tsnet state directory (default ~/.config/linkari/tsnet, or LINKARI_TSNET_STATE_DIR)")
	cmd.Flags().StringVar(&tsnetAuthKey, "tsnet-authkey", "", "Tailscale auth key (or TS_AUTHKEY env)")
	cmd.MarkFlagsMutuallyExclusive("tsnet", "local")
	cmd.Flags().IntVar(&notifyMinScore, "notify-min-score", 0, "minimum score for /notify FCM push (0 = use per-profile default, or LINKARI_NOTIFY_MIN_SCORE)")
	cmd.Flags().StringVar(&shell, "shell", "", "shell binary for tmux windows (default fish, or LINKARI_SHELL)")
	cmd.Flags().StringVar(&shellArgs, "shell-args", "", "shell command flag for tmux windows (default -c, or LINKARI_SHELL_ARGS)")
	cmd.Flags().StringVar(&configFile, "config", "", "path to actions.yaml config (default ~/.config/linkari/actions.yaml, or LINKARI_CONFIG)")
	cmd.Flags().BoolVar(&detach, "detach", false, "fork to background (POSIX only); PID written to ~/.local/state/linkari/linkari.pid")
	cmd.Flags().StringVar(&logFormat, "log-format", "", "log output format: text (default, human-friendly) or json (automation-metrics envelope)")
	cmd.Flags().StringVar(&logLevel, "log-level", "", "log level: debug|info|warn|error (default info; --debug forces debug)")

	return cmd
}
