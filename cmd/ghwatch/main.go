package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/thebrianlopez/runabout/cmd/ghwatch/internal/client"
	"github.com/thebrianlopez/runabout/cmd/ghwatch/internal/event"
	"github.com/thebrianlopez/runabout/cmd/ghwatch/internal/formatter"
	"github.com/thebrianlopez/runabout/cmd/ghwatch/internal/poller"
	"github.com/thebrianlopez/runabout/cmd/ghwatch/internal/state"
)

var version = "dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var (
		repo      string
		token     string
		interval  time.Duration
		stateFile string
		jsonMode  bool
		events    string
		debug     bool
		since     time.Duration
	)

	cmd := &cobra.Command{
		Use:     "ghwatch",
		Short:   "Stream GitHub repository activity to the terminal",
		Version: version,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), runConfig{
				repo:      repo,
				token:     token,
				interval:  interval,
				stateFile: stateFile,
				jsonMode:  jsonMode,
				events:    events,
				debug:     debug,
				since:     since,
			})
		},
		SilenceUsage: true,
	}

	f := cmd.Flags()
	f.StringVar(&repo, "repo", "", "Repository in owner/repo format (required)")
	f.StringVar(&token, "token", os.Getenv("GITHUB_TOKEN"), "GitHub API token (default: $GITHUB_TOKEN)")
	f.DurationVar(&interval, "interval", 30*time.Second, "Poll interval")
	f.StringVar(&stateFile, "state-file", ".ghwatch-state.json", "State file path")
	f.BoolVar(&jsonMode, "json", false, "Output JSONL instead of human-readable text")
	f.StringVar(&events, "events", "push,pr,workflow", "Comma-separated event types to watch")
	f.BoolVar(&debug, "debug", false, "Enable debug logging to stderr")
	f.DurationVar(&since, "since", time.Hour, "Lookback window on first run")

	_ = cmd.MarkFlagRequired("repo")

	return cmd
}

type runConfig struct {
	repo      string
	token     string
	interval  time.Duration
	stateFile string
	jsonMode  bool
	events    string
	debug     bool
	since     time.Duration
}

func run(parent context.Context, cfg runConfig) error {
	// Signal handling.
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Debug logger.
	var logger *log.Logger
	if cfg.debug {
		logger = log.New(os.Stderr, "[ghwatch] ", log.LstdFlags)
	}
	logf := func(format string, args ...interface{}) {
		if logger != nil {
			logger.Printf(format, args...)
		}
	}

	// Parse owner/repo.
	parts := strings.SplitN(cfg.repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid --repo format %q, expected owner/repo", cfg.repo)
	}
	owner, repoName := parts[0], parts[1]

	// Resolve token.
	if cfg.token == "" {
		return fmt.Errorf("GitHub token required: set --token or $GITHUB_TOKEN")
	}

	// Create client.
	var clientOpts []client.Option
	if logger != nil {
		clientOpts = append(clientOpts, client.WithDebug(logger))
	}
	ghClient, err := client.New(cfg.token, owner, repoName, clientOpts...)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	defer ghClient.Close()

	// Load state.
	store, err := state.NewStore(cfg.stateFile)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	// Create deduplicator, seed from state.
	dedup := event.NewDeduplicator(2 * time.Hour)
	dedup.Seed(store.SeenEvents())
	logf("loaded %d seen events from state", dedup.Len())

	// Determine since time.
	sinceTime := time.Now().Add(-cfg.since)
	if !store.LastPollTime().IsZero() {
		sinceTime = store.LastPollTime()
		logf("resuming from last poll: %v", sinceTime)
	}

	// Create pollers based on --events filter.
	enabled := parseEventTypes(cfg.events)
	var pollers []poller.EventPoller
	if enabled["push"] {
		pollers = append(pollers, poller.NewPushPoller(ghClient, sinceTime))
	}
	if enabled["pr"] {
		pollers = append(pollers, poller.NewPRPoller(ghClient))
	}
	if enabled["workflow"] {
		pollers = append(pollers, poller.NewWorkflowPoller(ghClient))
	}
	if len(pollers) == 0 {
		return fmt.Errorf("no event types enabled (got --events=%q)", cfg.events)
	}

	// Create formatter.
	var fmtr formatter.Formatter
	if cfg.jsonMode {
		fmtr = formatter.NewJSON(os.Stdout)
	} else {
		fmtr = formatter.NewText(os.Stdout)
	}

	// Print startup banner (text mode only).
	if !cfg.jsonMode {
		pollerNames := make([]string, len(pollers))
		for i, p := range pollers {
			pollerNames[i] = p.Name()
		}
		fmt.Fprintf(os.Stderr, "ghwatch %s\n", version)
		fmt.Fprintf(os.Stderr, "  repo:     %s/%s\n", owner, repoName)
		fmt.Fprintf(os.Stderr, "  interval: %s\n", cfg.interval)
		fmt.Fprintf(os.Stderr, "  events:   %s\n", strings.Join(pollerNames, ", "))
		fmt.Fprintf(os.Stderr, "  state:    %s\n", cfg.stateFile)
		fmt.Fprintf(os.Stderr, "  since:    %s\n", sinceTime.Format(time.RFC3339))
		fmt.Fprintln(os.Stderr)
	}

	// Build and run dispatcher.
	disp := poller.NewDispatcher(poller.DispatcherConfig{
		Pollers:   pollers,
		Interval:  cfg.interval,
		Dedup:     dedup,
		Store:     store,
		Formatter: fmtr,
		Debug:     cfg.debug,
		Logger:    logger,
	})

	err = disp.Run(ctx)

	// Save final state.
	store.SetSeenEvents(dedup.SeenIDs())
	store.SetLastPollTime(time.Now())
	if saveErr := store.Save(); saveErr != nil {
		logf("final state save error: %v", saveErr)
	}

	if !cfg.jsonMode {
		fmt.Fprintln(os.Stderr, "\nghwatch shutdown complete")
	}
	if ctx.Err() == context.Canceled {
		return nil // Clean shutdown via signal.
	}
	return err
}

func parseEventTypes(s string) map[string]bool {
	m := make(map[string]bool)
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(strings.ToLower(t))
		if t != "" {
			m[t] = true
		}
	}
	return m
}
