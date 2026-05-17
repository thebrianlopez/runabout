package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"

	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/auth"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/config"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/dedupe"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/jiraclient"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/metrics"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/poller"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/publisher"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/telemetry"
	"github.com/thebrianlopez/runabout/internal/secrets"
)

// Build-time variables injected via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "jira-poller: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// ── Structured logging ────────────────────────────────────────────────────
	logger := telemetry.NewLogger(cfg.LogFormat)
	slog.SetDefault(logger)
	logger.Info("jira-poller starting", "version", version, "commit", commit, "date", date)

	// ── OTel tracing ──────────────────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	otelShutdown, err := telemetry.InitTracerProvider(ctx, cfg.OTelEndpoint)
	if err != nil && !errors.Is(err, telemetry.ErrTracerInit) {
		return fmt.Errorf("otel: %w", err)
	}
	if errors.Is(err, telemetry.ErrTracerInit) {
		logger.Warn("otel tracer init failed; falling back to no-op", "error", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		otelShutdown(shutCtx) //nolint:errcheck
	}()
	_ = otel.GetTracerProvider() // ensure global is used

	// ── Prometheus metrics ────────────────────────────────────────────────────
	if err := metrics.Register(prometheus.DefaultRegisterer); err != nil {
		return fmt.Errorf("metrics init: %w", err)
	}
	logger.Info("prometheus metrics registered", "metric_count", 7)

	// ── SQLite database ───────────────────────────────────────────────────────
	dbPath := cfg.DBPath
	if dbPath == "" {
		home, _ := os.UserHomeDir()
		dbPath = filepath.Join(home, ".jira-poller", "events.db")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}

	db, err := openSQLite(dbPath)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	defer db.Close()

	if err := dedupe.ApplySchema(db); err != nil {
		return fmt.Errorf("dedupe schema: %w", err)
	}
	if err := publisher.ApplySchema(db); err != nil {
		return fmt.Errorf("publisher schema: %w", err)
	}

	// ── Credentials (F2) ──────────────────────────────────────────────────────
	resolver := secrets.New(secrets.DefaultAWSFactory())
	credCache := auth.NewCredentialCache(resolver, cfg.SecretARN, cfg.CredentialTTL, time.Now)
	if err := credCache.ForceRefresh(ctx); err != nil {
		return fmt.Errorf("credentials: %w", err)
	}

	// ── Jira client (F3) ──────────────────────────────────────────────────────
	creds, err := credCache.Get(ctx)
	if err != nil {
		return fmt.Errorf("get credentials: %w", err)
	}
	jiraClient, err := jiraclient.NewAtlassianClient(cfg.JiraBaseURL, creds.Email, creds.APIToken, "")
	if err != nil {
		return fmt.Errorf("jira client: %w", err)
	}

	// ── Stores and publisher (F5) ─────────────────────────────────────────────
	store := dedupe.NewSQLiteStore(db, time.Now)
	pub := publisher.NewSQLitePublisher(db)

	sinkDir := cfg.SinkDir
	if sinkDir == "" {
		home, _ := os.UserHomeDir()
		sinkDir = filepath.Join(home, ".automation-metrics", "events")
	}
	sink := publisher.NewJSONLSink(sinkDir, time.Now)
	publisher.StartDrainWorker(ctx, db, sink, time.Now)

	// ── Poller (F4) ───────────────────────────────────────────────────────────
	p := poller.New(cfg, jiraClient, store, pub, time.Now, logger)

	// ── HTTP server: /metrics, /healthz, /readyz ──────────────────────────────
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.Handle("/readyz", telemetry.NewReadyzHandler(p.LastPollTime, cfg.PollInterval, time.Now))

	port := os.Getenv("METRICS_PORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}
	go func() {
		logger.Info("HTTP server started", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server error", "error", err)
		}
	}()
	defer server.Shutdown(context.Background()) //nolint:errcheck

	// ── Run poller until SIGTERM/SIGINT ───────────────────────────────────────
	logger.Info("poller started",
		"projects", cfg.JiraProjects,
		"poll_interval", cfg.PollInterval,
		"lookback_window", cfg.LookbackWindow,
	)
	return p.Run(ctx)
}

// openSQLite opens a SQLite database with WAL mode and busy timeout.
func openSQLite(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
