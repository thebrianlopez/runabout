// EPIC-049 M2: linkari doctor subcommand.
//
// Runs the full EPIC-047 resolver pipeline and all pre-flight checks
// without starting any listener, tsnet engine, or tmux session.
// Symmetric with the Android `make doctor` output pattern (✓/✗/⚠ prefixes).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"

	"github.com/blo-grindr/runabout/internal/secrets"
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
  atlassian_email — Atlassian email resolvable (optional)
  atlassian_api_token — Atlassian API token resolvable (optional)
  jira_domain       — Jira domain resolvable (optional)
  pagerduty_token   — PagerDuty API token resolvable (optional)
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

			// --- Check 1: config.toml present and parseable ---
			configPath := serverYAMLPath
			if configPath == "" {
				configPath = defaultConfigPath()
			}
			var serverCfg *ServerConfig
			{
				cfg, err := LoadConfig(ctx, configPath)
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						addCheck(warnCheck("config_toml",
							fmt.Sprintf("not found at %s — run 'linkari config init' to create", configPath)))
					} else {
						addCheck(failCheck("config_toml", fmt.Sprintf("parse error: %v", err)))
					}
				} else {
					sc := cfg.Server
					serverCfg = &sc
					addCheck(okCheck("config_toml", configPath))
				}
			}

			// --- Checks 2-4: secret fields (token, firebase_sa, tsnet_authkey) ---
			resolutions := resolveAllSecrets(serverCfg)

			var hasSMURI bool
			for _, r := range resolutions {
				if r.Err != nil {
					addCheck(failCheck(r.Field, fmt.Sprintf("resolve error: %v — check SM permissions or URI spelling", r.Err)))
					continue
				}
				if r.Value == "" {
					if r.Field == "token" {
						addCheck(failCheck("token", "not configured — set token in config.toml, or export LINKARI_TOKEN"))
					} else {
						addCheck(warnCheck(r.Field, fmt.Sprintf("not configured (optional for %s)", r.Field)))
					}
					continue
				}
				if r.Tier == "toml-sm" {
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

			// --- Check 9: whisper-cli and model (EPIC-067) ---
			{
				if _, err := exec.LookPath("whisper-cli"); err != nil {
					addCheck(warnCheck("whisper_cli", "whisper-cli not found on PATH — voice note transcription will fail"))
				} else {
					addCheck(okCheck("whisper_cli", "whisper-cli found on PATH"))
				}

				whisperModel := defaultWhisperModel()
				if serverCfg != nil && serverCfg.WhisperModel != "" {
					whisperModel = serverCfg.WhisperModel
				}
				if _, err := os.Stat(whisperModel); err != nil {
					addCheck(warnCheck("whisper_model",
						fmt.Sprintf("model not found at %s — download ggml-large-v3-turbo.bin for voice note transcription", whisperModel)))
				} else {
					addCheck(okCheck("whisper_model", whisperModel))
				}
			}

			// --- Check: yt-dlp binary (EPIC-009 M5) ---
			{
				ytPath := "yt-dlp"
				if serverCfg != nil && serverCfg.YtdlpPath != "" {
					ytPath = serverCfg.YtdlpPath
				}
				if resolved, err := exec.LookPath(ytPath); err != nil {
					addCheck(warnCheck("ytdlp",
						fmt.Sprintf("yt-dlp not found at %q — YouTube URL transcription will fail (install yt-dlp or set ytdlp_path in server.yaml)", ytPath)))
				} else {
					addCheck(okCheck("ytdlp", resolved))
				}
			}

			// --- Check: ffmpeg binary (EPIC-003 M2) ---
			{
				ffPath := "ffmpeg"
				if serverCfg != nil && serverCfg.FfmpegPath != "" {
					ffPath = serverCfg.FfmpegPath
				}
				if resolved, err := exec.LookPath(ffPath); err != nil {
					addCheck(warnCheck("ffmpeg",
						fmt.Sprintf("ffmpeg not found at %q — audio conversion for YouTube fallback will fail (install ffmpeg or set ffmpeg_path in server.yaml)", ffPath)))
				} else {
					addCheck(okCheck("ffmpeg", resolved))
				}
			}

			// --- Check: lit (LiteParse) binary (EPIC-007 M3) ---
			{
				litPath := liteparseBinaryPath
				if serverCfg != nil && serverCfg.LiteParseePath != "" {
					litPath = serverCfg.LiteParseePath
				}
				if _, err := exec.LookPath(litPath); err != nil {
					addCheck(warnCheck("lit",
						fmt.Sprintf("not found — brew install llamaindex-liteparse")))
				} else {
					ver := ""
					if out, err := exec.Command(litPath, "--version").Output(); err == nil {
						ver = strings.TrimSpace(string(out))
					}
					if ver == "" {
						ver = litPath
					}
					addCheck(okCheck("lit", ver))
				}
			}

			// --- Check: tessdata_prefix (EPIC-109 M1) ---
			// PRIMARY: cfg.LiteParse.TessDataPrefix (config struct)
			// FALLBACK: TESSDATA_PREFIX env var
			// REASON: TESSDATA_PREFIX is only set in the process env by cmd_triage.go
			// during `linkari serve` startup; `doctor` does not run that init path.
			{
				cfgVal := ""
				if serverCfg != nil {
					cfgVal = serverCfg.LiteParse.TessDataPrefix
				}
				envVal := os.Getenv("TESSDATA_PREFIX") // FALLBACK: set by serve init path
				effective := cfgVal
				if effective == "" {
					effective = envVal
				}
				if effective == "" {
					addCheck(warnCheck("tessdata_prefix",
						"tessdata_prefix not set in config and TESSDATA_PREFIX not in env — OCR via lit will be unavailable"))
				} else {
					addCheck(okCheck("tessdata_prefix", effective))
				}
			}

			// --- Check 10: tsnet state directory ---
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

			// --- Check 11a: notify_on_prefilter_skip consistency (EPIC-001 M4) ---
			// When FCM is configured but notify_on_prefilter_skip is false, users
			// won't receive push notifications for pre-filtered shares (login walls,
			// unsupported platforms). This is almost always a misconfiguration.
			if serverCfg != nil && serverCfg.FirebaseSA != "" && !serverCfg.NotifyOnPrefilterSkip {
				addCheck(doctorCheck{
					Name:    "notify_on_prefilter_skip",
					Status:  statusWarn,
					Message: "FCM is configured but notify_on_prefilter_skip=false — pre-filtered shares will be silently dropped without a push notification. Set notify_on_prefilter_skip: true in server.yaml to enable transparency.",
				})
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

	cmd.Flags().StringVar(&serverYAMLPath, "path", "", "path to config.toml (default: ~/.config/linkari/config.toml)")
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
