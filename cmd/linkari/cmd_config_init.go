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

	"github.com/blo-grindr/runabout/cmd/linkari/internal/xdgpath"
)

// serverYAMLTemplate is the canonical server.yaml starter template.
// Every field from EPIC-048's ServerConfig schema is present with comments.
// Secret fields use secretsmanager:// URIs; non-secrets use safe defaults.
// Break-glass examples are commented out beneath each secret field.
const serverYAMLTemplate = `server:
  # --- Authentication ---

  # Bearer token for the /share and /notify endpoints.
  # secretsmanager:// fetched at startup; operator never has to export LINKARI_TOKEN.
  # Break-glass alternatives (uncomment one):
  #   token: "literal-token-value"
  #   token: "file:///home/user/.config/linkari/token.txt"
  token: secretsmanager://linkari/bearer-token

  # --- Tailscale Funnel ---

  # true  = bind Tailscale Funnel (reachable from Android)
  # false = local-only (use --local flag for one-off overrides)
  # Absent = defaults to true; EPIC-048 fallback rule activates if no tsnet_authkey.
  tsnet: true

  # Tailscale auth key for tsnet bring-up.
  # Break-glass: tsnet_authkey: "tskey-auth-..."
  tsnet_authkey: secretsmanager://linkari/tsnet-authkey

  # Tailscale node hostname (visible in tailnet admin panel).
  tsnet_hostname: linkari

  # Override the tsnet state directory (default: ~/.config/linkari/tsnet/).
  # Leave empty to use the XDG default.
  tsnet_state_dir: ""

  # --- Firebase Cloud Messaging ---

  # Path to the Firebase service-account JSON (materialized to ~/.cache/linkari/).
  # secretsmanager:// value is fetched and written to the cache dir at startup.
  # Break-glass: firebase_sa: "file:///home/user/.config/linkari/firebase-sa.json"
  firebase_sa: secretsmanager://linkari/firebase-sa

  # Minimum score [0-100] for pushing an FCM notification. Honored as a
  # uniform floor across ALL writer paths (HTTP /queue/{id}/score, /notify,
  # and the linkari score CLI) since EPIC-051. Set to 0 to disable.
  notify_min_score: 10

  # --- EPIC-051: Push gating ---
  # Per-profile throttle for digest pushes. The unified EnqueueDigestIfDue
  # helper writes at most one digest row per throttle window per profile.
  # Missing profiles fall back to digest_throttle_default.
  #
  # push:
  #   digest_throttle_default: 1h
  #   digest_throttle:
  #     eng: 1h
  #     dining: 24h
  #     fashion: 6h


  # --- Networking ---

  # HTTP listen port (also: LINKARI_PORT env var).
  port: 8080

  # Public base URL for fish callbacks (e.g. the Funnel URL).
  # Leave empty to auto-detect from tsnet or flag.
  server_url: ""

  # --- Queue ---

  # SQLite database path (default: ~/.config/linkari/queue.db).
  queue_db: ""

  # --- Logging ---

  # Append log output to this file path in addition to stderr.
  # Empty = stderr only. Relative paths resolve against the working directory.
  log_file: ""

  # Verbose debug logging (also: --debug flag).
  debug: false
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
		force   bool
		dryRun  bool
		path    string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate ~/.config/linkari/server.yaml with defaults",
		Long: `Generate a starter server.yaml at ~/.config/linkari/server.yaml.

The file is populated with secretsmanager:// URIs for all secret fields and
sensible defaults for all non-secret fields. Edit the file to adjust to your
environment, then run 'linkari doctor' to validate before 'linkari serve'.

Flags:
  --force     overwrite an existing file (backs up the old one first)
  --dry-run   print the generated config to stdout without touching disk
  --path      write to a custom path instead of the XDG default`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target := path
			if target == "" {
				cfgDir, err := xdgpath.ConfigDir()
				if err != nil {
					return fmt.Errorf("config init: %w", err)
				}
				target = filepath.Join(cfgDir, "server.yaml")
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

			// Idempotency check — no-op unless --force.
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
				"linkari: server.yaml written to %s\n"+
					"  → edit secret URIs if needed\n"+
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
