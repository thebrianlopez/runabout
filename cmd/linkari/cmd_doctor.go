// EPIC-049 M2: linkari doctor subcommand.
//
// Runs the full EPIC-047 resolver pipeline and all pre-flight checks
// without starting any listener, tsnet engine, or tmux session.
// Symmetric with the Android `make doctor` output pattern (✓/✗/⚠ prefixes).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"

	"github.com/blo-grindr/runabout/cmd/linkari/internal/secrets"
	"github.com/blo-grindr/runabout/cmd/linkari/internal/xdgpath"
)

// doctorCheck is the result of one pre-flight check.
type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`  // "ok", "warn", "fail"
	Message string `json:"message"`
}

const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

func okCheck(name, msg string) doctorCheck   { return doctorCheck{name, statusOK, msg} }
func warnCheck(name, msg string) doctorCheck { return doctorCheck{name, statusWarn, msg} }
func failCheck(name, msg string) doctorCheck { return doctorCheck{name, statusFail, msg} }

func doctorCmd() *cobra.Command {
	var (
		serverYAMLPath string
		jsonOutput     bool
	)

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate linkari configuration and secrets without starting the server",
		Long: `Run all pre-flight checks against ~/.config/linkari/server.yaml without
binding any listeners, starting the tsnet engine, or opening tmux sessions.

Checks:
  server_yaml      — file present and parseable
  token            — bearer token resolvable
  firebase_sa      — firebase service account resolvable
  tsnet_authkey    — tsnet auth key resolvable
  jira_token       — Jira bearer token resolvable (optional)
  aws_identity     — AWS STS caller identity (only when SM URIs present)
  xdg_config_dir   — ~/.config/linkari/ exists and is writable
  xdg_cache_dir    — ~/.cache/linkari/ exists and is writable
  xdg_state_dir    — ~/.local/state/linkari/ exists and is writable
  tsnet_state      — tsnet state directory status
  firebase_sa_cache — firebase-sa.json cache path is writable
  log_file         — log_file path is writable (if configured)

Exit code: 0 if all checks are ✓ or ⚠; 1 if any check is ✗.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			var checks []doctorCheck
			anyFail := false

			addCheck := func(c doctorCheck) {
				checks = append(checks, c)
				if c.Status == statusFail {
					anyFail = true
				}
			}

			// --- Check 1: server.yaml present and parseable ---
			if serverYAMLPath == "" {
				serverYAMLPath = defaultServerConfigPath()
			}
			var serverCfg *ServerConfig
			{
				cfg, err := LoadServerFile(serverYAMLPath)
				if err != nil {
					addCheck(failCheck("server_yaml", fmt.Sprintf("parse error: %v", err)))
				} else if cfg == nil {
					addCheck(warnCheck("server_yaml",
						fmt.Sprintf("not found at %s — run 'linkari config init' to create", serverYAMLPath)))
				} else {
					serverCfg = cfg
					addCheck(okCheck("server_yaml", serverYAMLPath))
				}
			}

			// --- Checks 2-4: secret fields (token, firebase_sa, tsnet_authkey) ---
			resolver := secrets.New(secrets.DefaultAWSFactory())
			resolutions := resolveAllSecrets(ctx, resolver, serverCfg)

			var hasSMURI bool
			for _, r := range resolutions {
				if r.Err != nil {
					addCheck(failCheck(r.Field, fmt.Sprintf("resolve error: %v — check SM permissions or URI spelling", r.Err)))
					continue
				}
				if r.Value == "" {
					if r.Field == "token" {
						addCheck(failCheck("token", "not configured — set token in server.yaml, or export LINKARI_TOKEN"))
					} else {
						addCheck(warnCheck(r.Field, fmt.Sprintf("not configured (optional for %s)", r.Field)))
					}
					continue
				}
				if r.Tier == "yaml-sm" {
					hasSMURI = true
				}
				fp := secrets.Fingerprint(r.Value)
				addCheck(okCheck(r.Field,
					fmt.Sprintf("resolved from %s fp=%s tier=%s", r.Src.String(), fp, r.Tier)))
			}

			// --- Check 5: AWS identity (only when SM URIs present) ---
			if hasSMURI {
				awsCfg, err := config.LoadDefaultConfig(ctx)
				if err != nil {
					addCheck(failCheck("aws_identity", fmt.Sprintf("load AWS config: %v — set AWS_PROFILE or configure ~/.aws/credentials", err)))
				} else {
					stsClient := sts.NewFromConfig(awsCfg)
					identity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
					if err != nil {
						addCheck(failCheck("aws_identity", fmt.Sprintf("sts:GetCallerIdentity failed: %v — check credentials and region", err)))
					} else {
						addCheck(okCheck("aws_identity",
							fmt.Sprintf("Account=%s ARN=%s", strOrEmpty(identity.Account), strOrEmpty(identity.Arn))))
					}
				}
			}

			// --- Checks 6-8: XDG directories ---
			for _, dirCheck := range []struct {
				name string
				fn   func() (string, error)
			}{
				{"xdg_config_dir", xdgpath.ConfigDir},
				{"xdg_cache_dir", xdgpath.CacheDir},
				{"xdg_state_dir", xdgpath.StateDir},
			} {
				dir, err := dirCheck.fn()
				if err != nil {
					addCheck(failCheck(dirCheck.name, fmt.Sprintf("create/access failed: %v", err)))
					continue
				}
				// Writable check: try creating a temp file.
				if wErr := probeWritable(dir); wErr != nil {
					addCheck(failCheck(dirCheck.name, fmt.Sprintf("%s exists but is not writable: %v", dir, wErr)))
				} else {
					addCheck(okCheck(dirCheck.name, dir))
				}
			}

			// --- Check 9: tsnet state directory ---
			{
				var tsnetStateDir string
				if serverCfg != nil && serverCfg.TsnetStateDir != "" {
					tsnetStateDir = serverCfg.TsnetStateDir
				} else {
					cfgDir, err := xdgpath.ConfigDir()
					if err == nil {
						tsnetStateDir = filepath.Join(cfgDir, "tsnet")
					}
				}
				if tsnetStateDir != "" {
					fi, err := os.Stat(tsnetStateDir)
					if os.IsNotExist(err) {
						addCheck(warnCheck("tsnet_state",
							fmt.Sprintf("%s absent — will be created on first tsnet bring-up (normal for fresh installs)", tsnetStateDir)))
					} else if err != nil {
						addCheck(failCheck("tsnet_state", fmt.Sprintf("stat %s: %v", tsnetStateDir, err)))
					} else if fi.IsDir() {
						addCheck(okCheck("tsnet_state",
							fmt.Sprintf("%s exists (authenticated or initialized)", tsnetStateDir)))
					} else {
						addCheck(failCheck("tsnet_state",
							fmt.Sprintf("%s exists but is not a directory", tsnetStateDir)))
					}
				}
			}

			// --- Check 10: firebase-sa cache path writable ---
			{
				cacheDir, err := xdgpath.CacheDir()
				if err == nil {
					cachePath := filepath.Join(cacheDir, "firebase-sa.json")
					// Permission check: can we create/overwrite the cache file?
					if wErr := probeWritable(cacheDir); wErr != nil {
						addCheck(failCheck("firebase_sa_cache",
							fmt.Sprintf("cache dir %s not writable: %v", cacheDir, wErr)))
					} else {
						addCheck(okCheck("firebase_sa_cache",
							fmt.Sprintf("%s (cache dir writable)", cachePath)))
					}
				}
			}

			// --- Check 11: log_file writable (if configured) ---
			if serverCfg != nil && serverCfg.LogFile != "" {
				logDir := filepath.Dir(serverCfg.LogFile)
				if mkErr := os.MkdirAll(logDir, 0o755); mkErr != nil {
					addCheck(failCheck("log_file",
						fmt.Sprintf("cannot create log dir %s: %v", logDir, mkErr)))
				} else if wErr := probeWritable(logDir); wErr != nil {
					addCheck(failCheck("log_file",
						fmt.Sprintf("log dir %s not writable: %v", logDir, wErr)))
				} else {
					addCheck(okCheck("log_file",
						fmt.Sprintf("%s (parent dir writable)", serverCfg.LogFile)))
				}
			}

			// --- Output ---
			if jsonOutput {
				type output struct {
					Checks   []doctorCheck `json:"checks"`
					ExitCode int           `json:"exit_code"`
				}
				exitCode := 0
				if anyFail {
					exitCode = 1
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(output{Checks: checks, ExitCode: exitCode})
			}

			for _, c := range checks {
				icon := "✓"
				if c.Status == statusWarn {
					icon = "⚠"
				} else if c.Status == statusFail {
					icon = "✗"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", icon, c.Name, c.Message)
			}

			if anyFail {
				return fmt.Errorf("doctor: one or more checks failed")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&serverYAMLPath, "path", "", "path to server.yaml (default: ~/.config/linkari/server.yaml)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit structured JSON output")
	return cmd
}

// probeWritable checks if a directory is writable by creating and immediately
// removing a temp file. Returns nil if writable.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".doctor-probe-*")
	if err != nil {
		return err
	}
	f.Close()
	os.Remove(f.Name())
	return nil
}

// strOrEmpty dereferences a *string safely.
func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
