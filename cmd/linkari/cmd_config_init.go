// EPIC-049 M1: linkari config init subcommand.
//
// Generates ~/.config/linkari/server.yaml with sensible defaults and
// secretsmanager:// URIs pre-populated. Idempotent, supports --force,
// --dry-run, and --path overrides.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// serverYAMLTemplate is the canonical config.toml starter template.
// Every field from EPIC-048's ServerConfig schema is present with comments.
// Secret fields use ${secretsmanager:name#field} refs expanded at load time;
// non-secrets use safe defaults.
const serverYAMLTemplate = `[server]
# --- Authentication ---

# Bearer token for the /share and /notify endpoints.
# secretsmanager:// URIs are resolved at startup via the post-load resolver,
# which uses [server.aws] for credential/region config.
# Break-glass alternatives:
#   token = "literal-token-value"
#   token = "file:///home/user/.config/linkari/token.txt"
token = "secretsmanager://linkari/bearer-token"

# Google OAuth client ID/secret for Android Google Sign-In and YouTube API.
google_client_id     = "secretsmanager://linkari/google-client-id"
google_client_secret = "secretsmanager://linkari/google-client-secret"

# Static invite codes seeded into the DB at startup (INSERT OR IGNORE).
invite_codes = []

# --- Tailscale Funnel ---

# true  = bind Tailscale Funnel (reachable from Android)
# false = local-only (use --local flag for one-off overrides)
tsnet = true

# Tailscale OAuth client secret for ephemeral key generation.
# The JSON secret at linkari/tsnet-oauth must contain {"client_id":"...","client_secret":"..."}.
# Break-glass (static auth key): tsnet_authkey = "tskey-auth-..."
tsnet_client_secret = "secretsmanager://linkari/tsnet-oauth#client_secret"
tsnet_authkey = ""

# Tailscale node hostname (visible in tailnet admin panel).
tsnet_hostname = "linkari"

# Override the tsnet state directory (default: ~/.config/linkari/tsnet/).
# Leave empty to use the XDG default.
tsnet_state_dir = ""

# --- Firebase Cloud Messaging ---

# Path to the Firebase service-account JSON (materialized to ~/.cache/linkari/).
# secretsmanager:// value is fetched and written to the cache dir at startup.
# Break-glass: firebase_sa = "file:///home/user/.config/linkari/firebase-sa.json"
firebase_sa = "secretsmanager://linkari/firebase-sa"

# Minimum score [0-100] for pushing an FCM notification.
notify_min_score = 10

# Push when a share is prefiltered (rejected before scoring).
notify_on_prefilter_skip = true

# --- Push gating ---
# Per-profile throttle for digest pushes. Missing profiles fall back to default.
#
# [server.push]
# digest_throttle_default = "1h"
#
# [server.push.digest_throttle]
# eng = "1h"
# dining = "24h"
# fashion = "6h"


# --- Jira API (outbound) ---

# Credentials for outbound Jira REST API calls.
# All fields below are sourced from the same SM secret with JSON key selectors (#field).
# Break-glass: use literal values or LINKARI_ATLASSIAN_EMAIL / LINKARI_ATLASSIAN_API_TOKEN /
#              LINKARI_JIRA_DOMAIN / LINKARI_PAGERDUTY_TOKEN env vars.
atlassian_email     = "secretsmanager://linkari/jira-webhook#ATLASSIAN_EMAIL"
atlassian_api_token = "secretsmanager://linkari/jira-webhook#ATLASSIAN_API_TOKEN"
jira_domain         = "secretsmanager://linkari/jira-webhook#JIRA_DOMAIN"

# --- PagerDuty ---

pagerduty_token = "secretsmanager://linkari/jira-webhook#PAGERDUTY_API_TOKEN"

# --- Domain API clients ---
# These fields are read directly from the config struct (not through the
# post-load resolver), so they use the ${secretsmanager:...} expansion format.
# The expansion relies on the default AWS SDK credential chain.
github_token               = "${secretsmanager:linkari/github-pat}"
google_service_account_path = ""
atlassian_confluence_token = "${secretsmanager:linkari/confluence-token}"
google_oauth_token         = "${secretsmanager:linkari/google-oauth-token}"

# --- TLS (local-only mode) ---

# When tsnet is enabled (default), Tailscale handles TLS automatically.
# For local-only TLS (--local --tls), generate cert/key with mkcert:
#   mkcert -cert-file ~/.config/linkari/cert.pem \
#          -key-file  ~/.config/linkari/key.pem  \
#          localhost 127.0.0.1

# --- Networking ---

port = 8080

# Public base URL for fish callbacks (e.g. the Funnel URL).
# Leave empty to auto-detect from tsnet or flag.
server_url = ""

# --- Queue ---

# SQLite database path (default: ~/.config/linkari/queue.db).
queue_db = ""

# --- Logging ---

log_file = ""
debug = false

# --- Image text extraction ---

# Vision pre-pass before image scoring (requires claude CLI on PATH).
image_text_extraction_enabled = false

# Minimum extracted-text length to suppress personal-photo short-circuit.
# image_short_circuit_bypass_min_chars = 20

# --- YouTube transcription and audio fallback ---

# Path to the yt-dlp binary. Defaults to "yt-dlp" on PATH.
# ytdlp_path = ""

# Directory for transcript markdown files. Defaults to "$WS_ORG_DOCS/transcripts" or "~/docs/transcripts".
# transcripts_dir = ""

# Path to the ffmpeg binary. Defaults to "ffmpeg" on PATH.
# ffmpeg_path = ""

[server.youtube]
fallback_to_audio = true

# Per-source sub-behavior toggles.
transcribe_subscriptions   = true
transcribe_watch_later     = true
auto_enqueue_subscriptions = true
auto_enqueue_watch_later   = true

# OAuth account slot routing. Maps named slots to YouTube sources.
# Re-auth with: linkari auth youtube --slot <name>
# On a browserless host (SSH/SSM, no port forwarding), run this from an
# interactive TTY: the command still prints the auth URL, but also accepts
# a pasted redirect URL (or just the code) from stdin once you approve in a
# browser on any other machine (EPIC-253).
# [server.youtube.accounts.default]
# slot    = "default"
# sources = ["watch_later", "liked"]

# --- Sources ---

[server.sources]
youtube_watch_later_enabled = true
youtube_liked_enabled       = true
youtube_monitored_enabled   = false
bluesky_firehose_enabled    = false

# --- AWS credential resolution ---

# Controls SDK credential resolution for secretsmanager:// URIs.
# On EC2 with an instance profile, region auto-detects from IMDS.
# On non-EC2, set region explicitly.
[server.aws]
region = "us-east-2"
# profile = ""
# role_arn = ""

# --- Database ---

[server.db]
# Absolute path to backup .db; sidecar metadata at <path>.backup-meta.json.
# backup_path = ""

# --- Shield middleware ---

[server.shield]
mode = "enforce"

# --- Scoring backend ---

[server.scoring]
# "claude_cli" (default) or "pi" for pluggable scoring.
# backend = "claude_cli"

# --- Telemetry ---

# [server.telemetry]
# enabled = false
# automation_metrics = false

# --- LiteParse (PDF extraction) ---

# [server.liteparse]
# tessdata_prefix = ""
# confidence_threshold = 0.5
`

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage linkari configuration",
	}
	cmd.AddCommand(configInitCmd())
	return cmd
}

func configInitCmd() *cobra.Command {
	var (
		force  bool
		dryRun bool
		path   string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate ~/.config/linkari/config.toml with defaults",
		Long: `Generate a starter config.toml at ~/.config/linkari/config.toml.

The file is populated with ${secretsmanager:...} refs for all secret fields and
sensible defaults for all non-secret fields. Edit the file to adjust to your
environment, then run 'linkari doctor' to validate before 'linkari serve'.

Flags:
  --force     overwrite an existing file (backs up the old one first)
  --dry-run   print the generated config to stdout without touching disk
  --path      write to a custom path instead of the XDG default`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target := path
			if target == "" {
				target = defaultConfigPath()
			}

			if dryRun {
				fmt.Fprint(cmd.OutOrStdout(), serverYAMLTemplate)
				return nil
			}

			// Ensure parent directory exists with restricted permissions.
			parent := filepath.Dir(target)
			if err := os.MkdirAll(parent, 0o700); err != nil {
				return fmt.Errorf("config init: create directory %s: %w", parent, err)
			}

			// Idempotency check  -  no-op unless --force.
			if _, err := os.Stat(target); err == nil {
				if !force {
					fmt.Fprintf(cmd.OutOrStdout(),
						"linkari: config already exists at %s (use --force to overwrite)\n", target)
					return nil
				}
				// Back up the existing file.
				backup := target + ".backup-" + time.Now().UTC().Format("20060102T150405Z")
				if err := os.Rename(target, backup); err != nil {
					return fmt.Errorf("config init: backup existing file: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(),
					"linkari: backed up existing config to %s\n", backup)
			}

			if err := os.WriteFile(target, []byte(serverYAMLTemplate), 0o600); err != nil {
				return fmt.Errorf("config init: write %s: %w", target, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"linkari: config.toml written to %s\n"+
					"  → edit secret refs if needed\n"+
					"  → run 'linkari doctor' to validate\n"+
					"  → run 'linkari serve' to start\n", target)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing file (backs up old)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print to stdout, do not write to disk")
	cmd.Flags().StringVar(&path, "path", "", "write to this path instead of the XDG default")
	return cmd
}
