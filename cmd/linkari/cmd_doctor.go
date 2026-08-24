// EPIC-049 M2: linkari doctor subcommand.
//
// Runs the full EPIC-047 resolver pipeline and all pre-flight checks
// without starting any listener, tsnet engine, or tmux session.
// Symmetric with the Android `make doctor` output pattern (✓/✗/⚠ prefixes).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"

	"github.com/thebrianlopez/runabout/internal/secrets"
)

// doctorCheck is the result of one pre-flight check.
type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "ok", "warn", "fail"
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

// doctorDeps carries the injectable probes of `linkari doctor`, threaded
// through doctorCmdWith instead of package-level seams (EPIC-258 M2). A zero
// doctorDeps is the production configuration: resolve() fills every nil field
// with its real implementation.
type doctorDeps struct {
	// ProbeYouTubeSlot probes one YouTube OAuth slot for credential health.
	ProbeYouTubeSlot func(ctx context.Context, slot string, userID int64, q *Queue, clientID, clientSecret string) error
	// AWSProbe reports AWS credential and Secrets Manager reachability.
	AWSProbe func(ctx context.Context, awsCfg secrets.AWSConfig) awsDoctorResult
	// LatestYtdlpVersion reports the newest published yt-dlp release tag.
	LatestYtdlpVersion func(ctx context.Context) (string, error)
}

// resolve returns a copy with production defaults substituted for nil fields.
func (d doctorDeps) resolve() doctorDeps {
	if d.ProbeYouTubeSlot == nil {
		d.ProbeYouTubeSlot = probeYouTubeSlot
	}
	if d.AWSProbe == nil {
		d.AWSProbe = awsDoctorProbe
	}
	if d.LatestYtdlpVersion == nil {
		d.LatestYtdlpVersion = latestYtdlpVersion
	}
	return d
}

const ytdlpReleasesURL = "https://api.github.com/repos/yt-dlp/yt-dlp/releases/latest"

func latestYtdlpVersion(ctx context.Context) (string, error) {
	return fetchLatestYtdlpVersion(ctx, ytdlpReleasesURL)
}

func fetchLatestYtdlpVersion(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api returned %s", resp.Status)
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.TagName == "" {
		return "", errors.New("github api returned no tag_name")
	}
	return payload.TagName, nil
}

func parseYtdlpVersion(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	fields := strings.Split(s, ".")
	if len(fields) < 3 {
		return time.Time{}, false
	}
	t, err := time.Parse("2006.01.02", strings.Join(fields[:3], "."))
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func ytdlpVersionCheck(installed, latest string, latestErr error) doctorCheck {
	if latestErr != nil || latest == "" {
		return okCheck("ytdlp", fmt.Sprintf("%s  -  upstream version check skipped (%v)", installed, latestErr))
	}

	installedAt, okInstalled := parseYtdlpVersion(installed)
	latestAt, okLatest := parseYtdlpVersion(latest)
	if !okInstalled || !okLatest {
		return okCheck("ytdlp", fmt.Sprintf("%s  -  latest is %s", installed, latest))
	}

	if installedAt.Before(latestAt) {
		days := int(latestAt.Sub(installedAt).Hours() / 24)
		return warnCheck("ytdlp", fmt.Sprintf(
			"%s is %d days behind %s  -  YouTube breaks yt-dlp regularly and stale builds fail with HTTP 403 on audio download (upgrade: brew upgrade yt-dlp / pip install -U yt-dlp)",
			installed, days, latest,
		))
	}

	return okCheck("ytdlp", fmt.Sprintf("%s (latest)", installed))
}

// probeYouTubeSlot probes a single YouTube OAuth slot for credential health.
// Returns nil on success, sql.ErrNoRows if no token is stored for the slot,
// or an error with "invalid_grant" for expired tokens.
func probeYouTubeSlot(ctx context.Context, slot string, userID int64, q *Queue, clientID, clientSecret string) error {
	ts, err := youtubeTokenSourceForSlot(ctx, slot, userID, q, clientID, clientSecret)
	if err != nil {
		return err
	}
	_, err = ts.Token()
	return err
}

// EPIC-223 M3: resolveBackupPath returns the expected backup file path.
// Config struct is the source of truth; XDG default is fallback ONLY.
func resolveBackupPath(cfg *ServerConfig) (string, error) {
	if cfg.DB.BackupPath != "" {
		return cfg.DB.BackupPath, nil
	}

	// Fallback to the shared state-dir default.
	paths, err := resolveEffectivePaths(cfg)
	if err != nil {
		return "", fmt.Errorf("state dir: %w", err)
	}

	return filepath.Join(paths.StateDir, "backups", "latest.db"), nil
}

// EPIC-223 M4: checkBackupFreshness reads <path>.backup-meta.json and returns doctorChecks.
// Bands: ok ≤24h, warn ≤72h, fail >72h / missing.
func checkBackupFreshness(backupPath string, now time.Time) []doctorCheck {
	sidecarPath := backupPath + ".backup-meta.json"

	raw, err := os.ReadFile(sidecarPath)
	if err != nil {
		slog.Warn("doctor_backup_freshness", "status", "warn", "backup_path", backupPath)
		return []doctorCheck{warnCheck("backup_freshness", fmt.Sprintf("no backup found at %s (run: linkari db backup %s to enable durability)", backupPath, backupPath))}
	}

	var sidecar struct {
		CreatedAt   time.Time `json:"created_at"`
		SourceDB    string    `json:"source_db"`
		BackupPath  string    `json:"backup_path"`
		QueueDBSize int64     `json:"queue_db_size_bytes"`
	}

	if err := json.Unmarshal(raw, &sidecar); err != nil {
		slog.Error("doctor_backup_freshness", "status", "fail", "backup_path", backupPath, "error", err.Error())
		return []doctorCheck{failCheck("backup_missing", fmt.Sprintf("malformed backup metadata at %s: %v", sidecarPath, err))}
	}

	if sidecar.CreatedAt.IsZero() {
		slog.Error("doctor_backup_freshness", "status", "fail", "backup_path", backupPath)
		return []doctorCheck{failCheck("backup_missing", fmt.Sprintf("missing created_at in backup metadata at %s", sidecarPath))}
	}

	age := now.Sub(sidecar.CreatedAt)
	ageHours := int(age.Hours())

	if age <= 24*time.Hour {
		slog.Info("doctor_backup_freshness", "status", "ok", "age_hours", ageHours, "backup_path", backupPath)
		return []doctorCheck{okCheck("backup_freshness", fmt.Sprintf("last backup %dh ago", ageHours))}
	}

	if age <= 72*time.Hour {
		slog.Warn("doctor_backup_freshness", "status", "warn", "age_hours", ageHours, "backup_path", backupPath)
		return []doctorCheck{warnCheck("backup_freshness", fmt.Sprintf("last backup %dh ago (>24h); run: linkari db backup %s", ageHours, backupPath))}
	}

	slog.Error("doctor_backup_freshness", "status", "fail", "age_hours", ageHours, "backup_path", backupPath)
	return []doctorCheck{failCheck("backup_freshness", fmt.Sprintf("last backup %dh ago (>72h); backups are stale  -  run: linkari db backup %s", ageHours, backupPath))}
}

func checkProfiles(tiers []ProfileSearchTier) ([]doctorCheck, string) {
	var out []doctorCheck
	status := statusOK
	resolved := map[string]string{}
	for _, tier := range tiers {
		label := tier.Source
		msg := label
		count := 0
		if tier.Path == "" {
			msg = fmt.Sprintf("%s (unset)", label)
		} else if tier.Source == "embedded" {
			entries, err := fs.ReadDir(EmbeddedProfileFS(), ".")
			if err != nil {
				msg = fmt.Sprintf("%s (unreadable: %v)", label, err)
			} else {
				for _, e := range entries {
					if strings.HasSuffix(e.Name(), ".yaml") {
						count++
						name := strings.TrimSuffix(e.Name(), ".yaml")
						if _, ok := resolved[name]; !ok {
							resolved[name] = label
						}
					}
				}
				msg = fmt.Sprintf("%s %d profiles", label, count)
			}
		} else {
			ents, err := os.ReadDir(tier.Path)
			if err != nil {
				msg = fmt.Sprintf("%s (unreadable: %v)", label, err)
			} else {
				for _, e := range ents {
					if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
						count++
						name := strings.TrimSuffix(e.Name(), ".yaml")
						if _, ok := resolved[name]; !ok {
							resolved[name] = label
						}
					}
				}
				msg = fmt.Sprintf("%s %d profiles", label, count)
			}
		}
		if tier.Deprecated && count > 0 {
			status = statusWarn
		}
		out = append(out, okCheck("profile_path", msg))
	}
	var names []string
	for n := range resolved {
		names = append(names, n)
	}
	slices.Sort(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s [%s]", n, resolved[n]))
	}
	if len(parts) == 0 {
		parts = append(parts, "none")
	}
	out = append(out, doctorCheck{Name: "profiles_resolved", Status: statusOK, Message: strings.Join(parts, ", ")})
	if status == statusWarn {
		out = append(out, warnCheck("profiles_deprecated", "profile resolved from deprecated ORG_PATH tier; migrate to XDG or toml profile_path"))
	}
	return out, strings.Join(parts, ", ")
}

func doctorCmd() *cobra.Command { return doctorCmdWith(doctorDeps{}) }

// doctorCmdWith threads doctor probe dependencies explicitly so tests can inject
// stubs without writing package globals (EPIC-258 M2).
func doctorCmdWith(deps doctorDeps) *cobra.Command {
	deps = deps.resolve()
	var (
		serverYAMLPath string
		jsonOutput     bool
		strict         bool
		requireChecks  []string
	)

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate linkari configuration and secrets without starting the server",
		Long: `Run all pre-flight checks against ~/.config/linkari/server.yaml without
binding any listeners, starting the tsnet engine, or opening tmux sessions.

Checks:
  server_yaml       -  file present and parseable
  token             -  bearer token resolvable
  firebase_sa       -  firebase service account resolvable
  tsnet_authkey     -  tsnet auth key resolvable
  jira_token        -  Jira bearer token resolvable (optional)
  atlassian_email  -  Atlassian email resolvable (optional)
  atlassian_api_token  -  Atlassian API token resolvable (optional)
  jira_domain        -  Jira domain resolvable (optional)
  pagerduty_token    -  PagerDuty API token resolvable (optional)
  aws_identity      -  AWS STS caller identity (only when SM URIs present)
  xdg_config_dir    -  ~/.config/linkari/ exists and is writable
  xdg_cache_dir     -  ~/.cache/linkari/ exists and is writable
  xdg_state_dir     -  ~/.local/state/linkari/ exists and is writable
  tsnet_state       -  tsnet state directory status
  firebase_sa_cache  -  firebase-sa.json cache path is writable
  log_file          -  log_file path is writable (if configured)

Exit code: 0 if all checks are ✓ or ⚠; 1 if any check is ✗.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			var checks []doctorCheck
			anyFail := false
			anyWarn := false

			addCheck := func(c doctorCheck) {
				checks = append(checks, c)
				switch c.Status {
				case statusFail:
					anyFail = true
				case statusWarn:
					anyWarn = true
				}
			}

			// --- Check 1: config.toml present and parseable ---
			configResolution := resolveConfigPath(serverYAMLPath)
			configPath := configResolution.Path
			var serverCfg *ServerConfig
			var fullCfg *Config
			awsCredsUnavailable := false
			{
				if raw, readErr := os.ReadFile(configPath); readErr == nil && strings.Contains(string(raw), "${secretsmanager:") && !hasExplicitAWSCredentials() && !hasIMDSCredentials() {
					awsCredsUnavailable = true
					// Prevent the AWS SDK default chain from blocking on IMDS during local doctor runs.
					// Only set when IMDS is genuinely unreachable (not on EC2 or no instance profile).
					_ = os.Setenv("AWS_EC2_METADATA_DISABLED", "true")
				}
				cfg, err := LoadConfig(ctx, configPath)
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						addCheck(warnCheck("config_toml",
							fmt.Sprintf("not found at %s  -  run 'linkari config init' to create", configPath)))
					} else {
						addCheck(failCheck("config_toml", fmt.Sprintf("parse error: %v", err)))
					}
				} else {
					fullCfg = cfg
					sc := cfg.Server
					serverCfg = &sc
					addCheck(okCheck("config_toml", configPath))
					if configResolution.Legacy {
						addCheck(warnCheck("legacy_config_location_detected", legacyConfigLocationMessage(defaultConfigPath(), legacyConfigPath())))
					}
				}
				if awsCredsUnavailable {
					addCheck(failCheck("aws_credentials", "config contains secretsmanager refs but no AWS credentials found - set AWS_PROFILE or run on an EC2 instance with an IAM instance profile"))
				}
			}

			// --- Check 1b: sources configuration (EPIC-097 F1) ---
			if serverCfg != nil {
				src := serverCfg.Sources
				enabled := []string{}
				disabled := []string{}
				if src.YouTubeWatchLaterEnabled {
					enabled = append(enabled, "yt_watch_later")
				} else {
					disabled = append(disabled, "yt_watch_later")
				}
				if src.YouTubeMonitoredEnabled {
					enabled = append(enabled, "yt_monitored")
				} else {
					disabled = append(disabled, "yt_monitored")
				}
				if src.YouTubeLikedEnabled {
					enabled = append(enabled, "yt_liked")
				} else {
					disabled = append(disabled, "yt_liked")
				}
				if src.BlueskyFirehoseEnabled {
					enabled = append(enabled, "bsky_firehose")
				} else {
					disabled = append(disabled, "bsky_firehose")
				}
				msg := fmt.Sprintf("enabled=%v", enabled)
				if len(disabled) > 0 {
					msg = fmt.Sprintf("%s disabled=%v", msg, disabled)
					addCheck(warnCheck("sources_config", msg))
				} else {
					addCheck(okCheck("sources_config", msg))
				}
			}

			// --- Check 1c: YouTube OAuth credential health ---
			if serverCfg != nil {
				if len(serverCfg.YouTube.Accounts) > 0 {
					// EPIC-182 F5: per-slot probes when [server.youtube.accounts] is configured.
					queuePath := resolveQueueDB(serverCfg.QueueDB)
					q, err := NewQueue(queuePath, false)
					if err != nil {
						addCheck(failCheck("youtube_oauth", fmt.Sprintf("open queue db: %v", err)))
					} else {
						if conflictErr := validateSlotConfig(serverCfg); conflictErr != nil {
							addCheck(failCheck("youtube_slot_conflict", conflictErr.Error()))
						}
						for _, account := range serverCfg.YouTube.Accounts {
							slot := account.Slot
							if slot == "" {
								slot = "default"
							}
							checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
							probeErr := deps.ProbeYouTubeSlot(checkCtx, slot, 1, q, serverCfg.GoogleClientID, serverCfg.GoogleClientSecret)
							cancel()
							checkName := fmt.Sprintf("youtube_oauth[slot=%s]", slot)
							if errors.Is(probeErr, sql.ErrNoRows) {
								addCheck(warnCheck("youtube_slot_missing",
									fmt.Sprintf("slot '%s' has no stored token - run: linkari auth youtube --slot %s", slot, slot)))
							} else if probeErr != nil {
								errClass, _ := classifyYouTubeAPIError(probeErr)
								if errClass == "" {
									errClass = "api_error"
								}
								addCheck(failCheck(checkName,
									fmt.Sprintf("slot '%s' token error (%s) - run: linkari auth youtube --slot %s", slot, errClass, slot)))
							} else {
								addCheck(okCheck(checkName, fmt.Sprintf("slot '%s' refreshes successfully", slot)))
								// EPIC-184 F4: report how the slot token was obtained.
								var src string
								q.db.QueryRow(`SELECT COALESCE(source,'cli') FROM youtube_oauth_slots WHERE user_id=1 AND slot_name=?`, slot).Scan(&src)
								if src != "" {
									addCheck(okCheck(
										fmt.Sprintf("youtube_delegation_source[slot=%s]", slot),
										fmt.Sprintf("slot '%s' token source: %s", slot, src),
									))
								}
							}
						}
						_ = q.Close()
					}
				} else if serverCfg.Sources.YouTubeWatchLaterEnabled || serverCfg.Sources.YouTubeLikedEnabled || serverCfg.Sources.YouTubeMonitoredEnabled {
					// Backward compat: single youtube_oauth check when no accounts config.
					queuePath := resolveQueueDB(serverCfg.QueueDB)
					q, err := NewQueue(queuePath, false)
					if err != nil {
						addCheck(failCheck("youtube_oauth", fmt.Sprintf("open queue db: %v", err)))
					} else {
						checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
						ts, err := youtubeTokenSource(checkCtx, "default", q, serverCfg.GoogleClientID, serverCfg.GoogleClientSecret)
						if err != nil {
							addCheck(failCheck("youtube_oauth", fmt.Sprintf("%v  -  run `linkari auth youtube`", err)))
						} else if _, err := ts.Token(); err != nil {
							errClass, remediation := classifyYouTubeAPIError(err)
							if remediation == "" {
								remediation = "check Google OAuth client configuration and network access"
							}
							addCheck(failCheck("youtube_oauth", fmt.Sprintf("%s: %v  -  %s", errClass, err, remediation)))
						} else {
							addCheck(okCheck("youtube_oauth", "stored YouTube credential refreshes successfully"))
						}
						cancel()
						_ = q.Close()
					}
				}
			}

			// --- Checks 2-4: secret fields (token, firebase_sa, tsnet_authkey) ---
			resolutions := resolveAllSecrets(serverCfg)

			var hasSMURI bool
			for _, r := range resolutions {
				if r.Err != nil {
					addCheck(failCheck(r.Field, fmt.Sprintf("resolve error: %v  -  check SM permissions or URI spelling", r.Err)))
					continue
				}
				if r.Value == "" {
					if r.Field == "token" {
						addCheck(failCheck("token", "not configured  -  set token in config.toml, or export LINKARI_TOKEN"))
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

			// --- Check 5: AWS credential source + SM access (only when SM URIs present) ---
			if hasSMURI {
				var awsDocCfg secrets.AWSConfig
				if serverCfg != nil {
					awsDocCfg = secrets.AWSConfig{
						Region:  serverCfg.AWS.Region,
						Profile: serverCfg.AWS.Profile,
						RoleARN: serverCfg.AWS.RoleARN,
					}
				}
				result := deps.AWSProbe(ctx, awsDocCfg)
				addCheck(formatAWSCheck(result))
			}

			// --- Checks 6-8: platform directories ---
			effectivePaths, _ := resolveEffectivePaths(serverCfg)
			for _, dirCheck := range []struct {
				name string
				dir  string
			}{
				{"xdg_config_dir", effectivePaths.ConfigDir},
				{"xdg_cache_dir", effectivePaths.CacheDir},
				{"xdg_state_dir", effectivePaths.StateDir},
			} {
				if err := ensureDir(dirCheck.dir); err != nil {
					addCheck(failCheck(dirCheck.name, fmt.Sprintf("create/access failed: %v", err)))
					continue
				}
				// Writable check: try creating a temp file.
				if wErr := probeWritable(dirCheck.dir); wErr != nil {
					addCheck(failCheck(dirCheck.name, fmt.Sprintf("%s exists but is not writable: %v", dirCheck.dir, wErr)))
				} else {
					addCheck(okCheck(dirCheck.name, dirCheck.dir))
				}
			}

			// --- Check 8b: transcripts_dir (F2 DataRoot data_dir_not_directory taxonomy) ---
			// POMO PERSONAL_20260817T223458Z (linkari-transcript-dir-file-collision):
			// doctor previously never validated TranscriptsDir at all, so a path
			// segment collision (e.g. a stray file where a directory should be)
			// only surfaced as a per-request WARN in the scoring pipeline, never
			// here. This distinguishes "exists as a file" (data_dir_not_directory,
			// per F2 §4 Error Taxonomy) from a generic unwritable/create failure.
			for _, c := range checkTranscriptsDirType(effectivePaths.TranscriptsDir) {
				addCheck(c)
			}

			// --- Check 9: whisper-cli and model (EPIC-067) ---
			{
				if _, err := exec.LookPath("whisper-cli"); err != nil {
					addCheck(warnCheck("whisper_cli", "whisper-cli not found on PATH  -  voice note transcription will fail"))
				} else {
					addCheck(okCheck("whisper_cli", "whisper-cli found on PATH"))
				}

				whisperModel := defaultWhisperModel()
				if serverCfg != nil && serverCfg.WhisperModel != "" {
					whisperModel = serverCfg.WhisperModel
				}
				if _, err := os.Stat(whisperModel); err != nil {
					addCheck(warnCheck("whisper_model",
						fmt.Sprintf("model not found at %s  -  download ggml-large-v3-turbo.bin for voice note transcription", whisperModel)))
				} else {
					addCheck(okCheck("whisper_model", whisperModel))
				}
			}

			// --- Check: yt-dlp binary (EPIC-009 M5; version report EPIC-109 M4) ---
			{
				ytPath := "yt-dlp"
				if serverCfg != nil && serverCfg.YtdlpPath != "" {
					ytPath = serverCfg.YtdlpPath
				}
				if resolved, err := exec.LookPath(ytPath); err != nil {
					addCheck(warnCheck("ytdlp",
						fmt.Sprintf("yt-dlp not found at %q  -  YouTube URL transcription will fail (install yt-dlp or set ytdlp_path in server.yaml)", ytPath)))
				} else {
					ver := resolved
					versionKnown := false
					if out, verErr := exec.Command(resolved, "--version").Output(); verErr == nil {
						ver = strings.TrimSpace(string(out))
						versionKnown = true
					}
					if !versionKnown {
						addCheck(okCheck("ytdlp", ver))
					} else {
						verCtx, verCancel := context.WithTimeout(ctx, 3*time.Second)
						latest, latestErr := deps.LatestYtdlpVersion(verCtx)
						verCancel()
						addCheck(ytdlpVersionCheck(ver, latest, latestErr))
					}
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
						fmt.Sprintf("ffmpeg not found at %q  -  audio conversion for YouTube fallback will fail (install ffmpeg or set ffmpeg_path in server.yaml)", ffPath)))
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
						fmt.Sprintf("not found  -  brew install llamaindex-liteparse")))
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
						"tessdata_prefix not set in config and TESSDATA_PREFIX not in env  -  OCR via lit will be unavailable"))
				} else {
					// EPIC-164: upgrade from presence-only to functional validation.
					entries, err := os.ReadDir(effective)
					switch {
					case err != nil:
						addCheck(warnCheck("tessdata_prefix",
							fmt.Sprintf("tessdata_prefix set to %q but path does not exist or is unreadable: %v", effective, err)))
					case !hasTrainedData(entries):
						addCheck(warnCheck("tessdata_prefix",
							fmt.Sprintf("tessdata_prefix set to %q but no .traineddata files found", effective)))
					default:
						addCheck(okCheck("tessdata_prefix", effective))
					}
				}
			}

			// --- Check: wiki config (EPIC-180 M1) ---
			// Only runs when [wiki] block is present and Enabled=true.
			if serverCfg != nil && serverCfg.Wiki.Enabled {
				switch err := serverCfg.Wiki.Validate(); err.(type) {
				case nil:
					// Count topic directories under TopicRootPath.
					entries, readErr := os.ReadDir(serverCfg.Wiki.TopicRootPath())
					topicCount := 0
					if readErr == nil {
						for _, e := range entries {
							if e.IsDir() {
								topicCount++
							}
						}
					}
					addCheck(okCheck("wiki", fmt.Sprintf("vault=%s topics=%d index=%s", serverCfg.Wiki.RootPath, topicCount, serverCfg.Wiki.IndexFilename)))
				case WikiConfigWarning:
					addCheck(warnCheck("wiki", err.Error()))
				default:
					addCheck(failCheck("wiki", err.Error()))
				}
			}

			// --- Check 10: tsnet state directory ---
			{
				var tsnetStateDir string
				if serverCfg != nil && serverCfg.TsnetStateDir != "" {
					tsnetStateDir = serverCfg.TsnetStateDir
				} else {
					tsnetStateDir = filepath.Join(effectivePaths.ConfigDir, "tsnet")
				}
				if tsnetStateDir != "" {
					fi, err := os.Stat(tsnetStateDir)
					if os.IsNotExist(err) {
						addCheck(warnCheck("tsnet_state",
							fmt.Sprintf("%s absent  -  will be created on first tsnet bring-up (normal for fresh installs)", tsnetStateDir)))
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
				cacheDir := effectivePaths.CacheDir
				if err := ensureDir(cacheDir); err == nil {
					cachePath := filepath.Join(cacheDir, "firebase-sa.json")
					// Permission check: can we create/overwrite the cache file?
					if wErr := probeWritable(cacheDir); wErr != nil {
						addCheck(failCheck("firebase_sa_cache",
							fmt.Sprintf("cache dir %s not writable: %v", cacheDir, wErr)))
					} else {
						addCheck(okCheck("firebase_sa_cache",
							fmt.Sprintf("%s (cache dir writable)", cachePath)))
					}
				} else {
					addCheck(failCheck("firebase_sa_cache", fmt.Sprintf("create/access failed: %v", err)))
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
					Message: "FCM is configured but notify_on_prefilter_skip=false  -  pre-filtered shares will be silently dropped without a push notification. Set notify_on_prefilter_skip: true in server.yaml to enable transparency.",
				})
			}

			// --- Check 11a: routing config validation (EPIC-111 F4 M12) ---
			{
				// Use the already loaded doctor config so --path remains path-isolated.
				// If no routing block is present, defaults are used and always valid.
				var routingCfg RoutingConfig
				if fullCfg != nil {
					routingCfg = fullCfg.Routing
				}
				if routingCfg.DefaultThreshold == 0 {
					routingCfg = defaultRoutingConfig()
				}
				if err := ValidateRoutingConfig(routingCfg); err != nil {
					addCheck(failCheck("routing_config",
						fmt.Sprintf("routing block invalid: %v", err)))
				} else {
					addCheck(okCheck("routing_config",
						fmt.Sprintf("threshold=%d, confidence_gate=%.2f, %d route overrides",
							routingCfg.DefaultThreshold,
							routingCfg.ExtractionConfidenceGate,
							len(routingCfg.RouteThresholds))))
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

			// --- Check 12: backup_freshness (EPIC-223) ---
			// Always run; falls back to XDG default when backup_path not configured.
			if serverCfg != nil {
				backupPath, bpErr := resolveBackupPath(serverCfg)
				if bpErr == nil {
					if serverCfg.DB.BackupPath == "" {
						addCheck(warnCheck("backup_path_configured", fmt.Sprintf("backup_path not set in config; using default %s", backupPath)))
					}
					for _, c := range checkBackupFreshness(backupPath, time.Now().UTC()) {
						addCheck(c)
					}
				}
			}

			// --- Check 13: k8s volume health (EPIC-228) ---
			for _, c := range checkK8sVolume(resolveDataDir(serverCfg)) {
				addCheck(c)
			}

			// --- Check 14: profile search path / doctor integration (EPIC-243) ---
			profiles := ProfileSearchPathAnnotated()
			profilesChecks, profilesResolved := checkProfiles(profiles)
			for _, c := range profilesChecks {
				addCheck(c)
			}

			// --- Output ---
			if !jsonOutput {
				fmt.Fprintln(cmd.OutOrStdout(), "[profiles]")
				for _, tier := range profiles {
					if tier.Path == "" {
						fmt.Fprintf(cmd.OutOrStdout(), "  %s (unset)\n", tier.Source)
					} else {
						fmt.Fprintf(cmd.OutOrStdout(), "  %s %s\n", tier.Source, tier.Path)
					}
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  resolved: %s\n", profilesResolved)
			}
			// Optional-dependency gaps (missing lit, whisper-cli, tessdata)
			// surface as warnings, so a fail-only exit code silently passes a
			// half-provisioned host -- see POMO ec2-server-dependency-gaps
			// RC-6/RS-1.
			//
			// --strict gates on any warning. That is deliberately blunt and is
			// wrong for most deployments, where benign warnings are permanent
			// (unconfigured optional integrations, no backup yet). Prefer
			// --require, which names the checks a given deployment actually
			// depends on. A gate that is always red gets disabled.
			byName := make(map[string]doctorCheck, len(checks))
			for _, c := range checks {
				byName[c.Name] = c
			}
			var missing, notOK []string
			for _, want := range requireChecks {
				want = strings.TrimSpace(want)
				if want == "" {
					continue
				}
				c, ok := byName[want]
				if !ok {
					// An unknown name is a gate failure, not a silent pass:
					// a typo'd or renamed check must never look healthy.
					missing = append(missing, want)
					continue
				}
				if c.Status != statusOK {
					notOK = append(notOK, fmt.Sprintf("%s[%s]: %s", c.Name, c.Status, c.Message))
				}
			}
			requireFailed := len(missing) > 0 || len(notOK) > 0

			// --require is a focused gate: unrelated checks can remain optional or
			// be configured by a different deployment concern. Without it, retain
			// doctor's historical whole-host exit semantics.
			focused := len(requireChecks) > 0
			gated := requireFailed || (!focused && anyFail) || (strict && anyWarn)

			gateErr := func() error {
				switch {
				case len(missing) > 0:
					return fmt.Errorf("doctor: --require names unknown check(s): %s", strings.Join(missing, ", "))
				case len(notOK) > 0:
					return fmt.Errorf("doctor: required check(s) not ok: %s", strings.Join(notOK, "; "))
				case !focused && anyFail:
					return fmt.Errorf("doctor: one or more checks failed")
				case strict && anyWarn:
					return fmt.Errorf("doctor: one or more checks warned (--strict)")
				}
				return nil
			}

			if jsonOutput {
				type output struct {
					Checks   []doctorCheck `json:"checks"`
					ExitCode int           `json:"exit_code"`
				}
				exitCode := 0
				if gated {
					exitCode = 1
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(output{Checks: checks, ExitCode: exitCode}); err != nil {
					return err
				}
				return gateErr()
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

			return gateErr()
		},
	}

	cmd.Flags().StringVar(&serverYAMLPath, "path", "", "path to config.toml (default: ~/.config/linkari/config.toml)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit structured JSON output")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero on warnings as well as failures (blunt; prefer --require for gates)")
	cmd.Flags().StringSliceVar(&requireChecks, "require", nil, "comma-separated check names that must be ok; exit non-zero otherwise (e.g. --require lit,whisper_cli). Unknown names are treated as failures.")
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

func hasExplicitAWSCredentials() bool {
	return os.Getenv("AWS_PROFILE") != "" ||
		os.Getenv("AWS_ACCESS_KEY_ID") != "" ||
		os.Getenv("AWS_SECRET_ACCESS_KEY") != "" ||
		os.Getenv("AWS_SESSION_TOKEN") != "" ||
		os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != "" ||
		os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" ||
		os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != ""
}

// hasIMDSCredentials probes IMDSv2 with a short timeout to detect EC2 instance profiles.
// Returns true if IMDS responds, indicating the AWS SDK can obtain credentials automatically.
// Honors the standard AWS_EC2_METADATA_DISABLED env var (same convention the AWS SDK's own
// IMDS client respects) so callers - including doctor itself when it has already determined
// IMDS is unreachable, and tests - can hermetically force this probe off without depending
// on actual network reachability of 169.254.169.254, which can vary by host/sandbox.
func hasIMDSCredentials() bool {
	if os.Getenv("AWS_EC2_METADATA_DISABLED") == "true" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		"http://169.254.169.254/latest/api/token", nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "21600")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type awsDoctorResult struct {
	Source  string // e.g. "shared-credentials-file", "ec2-instance-metadata"
	ARN     string
	Profile string
	SMOK    bool
	Err     error
}

func awsDoctorProbe(ctx context.Context, awsCfg secrets.AWSConfig) awsDoctorResult {
	var opts []func(*config.LoadOptions) error
	if awsCfg.Region != "" {
		opts = append(opts, config.WithRegion(awsCfg.Region))
	}
	if awsCfg.Profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(awsCfg.Profile))
	}

	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cfg, err := config.LoadDefaultConfig(probeCtx, opts...)
	if err != nil {
		return awsDoctorResult{Err: fmt.Errorf("load config: %w", err)}
	}

	stsClient := sts.NewFromConfig(cfg)
	identity, err := stsClient.GetCallerIdentity(probeCtx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return awsDoctorResult{Err: fmt.Errorf("no credentials: %w", err)}
	}

	source := detectCredentialSource(awsCfg)
	result := awsDoctorResult{
		Source:  source,
		ARN:     aws.ToString(identity.Arn),
		Profile: awsCfg.Profile,
	}

	smClient := secretsmanager.NewFromConfig(cfg)
	_, smErr := smClient.ListSecrets(probeCtx, &secretsmanager.ListSecretsInput{
		MaxResults: aws.Int32(1),
	})
	result.SMOK = smErr == nil
	if smErr != nil {
		result.Err = fmt.Errorf("sm access denied: %w", smErr)
	}
	return result
}

func detectCredentialSource(awsCfg secrets.AWSConfig) string {
	if awsCfg.Profile != "" {
		return "shared-credentials-file"
	}
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" {
		return "environment-variables"
	}
	if os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != "" {
		return "web-identity-token"
	}
	if os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" || os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "" {
		return "ecs-container"
	}
	return "ec2-instance-metadata"
}

func formatAWSCheck(r awsDoctorResult) doctorCheck {
	if r.Err != nil && r.ARN == "" {
		return failCheck("aws_credentials",
			"no credentials found - set [aws] profile, role_arn, or AWS_ACCESS_KEY_ID")
	}

	label := r.Source
	if r.Profile != "" {
		label = fmt.Sprintf("%s (profile: %s)", r.Source, r.Profile)
	}

	if !r.SMOK {
		return failCheck("aws_credentials",
			fmt.Sprintf("resolved via %s (%s), but Secrets Manager access denied - check IAM policy", label, r.ARN))
	}

	return okCheck("aws_credentials",
		fmt.Sprintf("resolved via %s (%s)", label, r.ARN))
}

// hasTrainedData returns true if entries contains at least one .traineddata file.
// Used by the tessdata_prefix doctor check (EPIC-164) to distinguish a configured
// but empty or missing tessdata directory from a functional one.
func hasTrainedData(entries []os.DirEntry) bool {
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".traineddata") {
			return true
		}
	}
	return false
}

// strOrEmpty dereferences a *string safely.
func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func k8sModeEnabled() bool {
	v := strings.TrimSpace(os.Getenv("LINKARI_K8S_MODE"))
	return v == "true" || v == "1" || strings.EqualFold(v, "yes")
}

func resolveDataDir(cfg *ServerConfig) string {
	if cfg != nil && cfg.QueueDB != "" {
		return filepath.Dir(cfg.QueueDB)
	}
	return filepath.Dir(resolveQueueDB(""))
}

// checkTranscriptsDirType implements the data_dir_not_directory check named
// in PlatformDataLayout_F2_DataRoot_TDD.md §4 Error Taxonomy. Unlike the
// xdg_*_dir checks above (which lump "not a directory" and "not writable"
// into one create/access failure message), this distinguishes the specific
// path-segment-is-a-file case so operators get the same actionable signal
// doctor was designed to surface, instead of the silent per-request WARN
// this check exists because of (POMO PERSONAL_20260817T223458Z).
func checkTranscriptsDirType(dir string) []doctorCheck {
	if dir == "" {
		return nil
	}
	if st, err := os.Stat(dir); err == nil {
		if !st.IsDir() {
			return []doctorCheck{failCheck("transcripts_dir", fmt.Sprintf("%s exists but is not a directory (data_dir_not_directory) - remove or rename it, or set transcripts_dir to a different path", dir))}
		}
	} else if errors.Is(err, syscall.ENOTDIR) {
		// A parent path segment (not the leaf) is a regular file - same
		// data_dir_not_directory class, just detected one level up. This is
		// the exact shape of POMO PERSONAL_20260817T223458Z: ~/code existed
		// as a file, so stat(~/code/personal/docs/transcripts) failed here.
		return []doctorCheck{failCheck("transcripts_dir", fmt.Sprintf("%s unavailable: a parent path segment is not a directory (data_dir_not_directory): %v", dir, err))}
	} else if !os.IsNotExist(err) {
		return []doctorCheck{failCheck("transcripts_dir", fmt.Sprintf("%s unavailable: %v", dir, err))}
	}

	if err := ensureDir(dir); err != nil {
		return []doctorCheck{failCheck("transcripts_dir", fmt.Sprintf("create/access failed: %v", err))}
	}
	if wErr := probeWritable(dir); wErr != nil {
		return []doctorCheck{failCheck("transcripts_dir", fmt.Sprintf("%s exists but is not writable: %v", dir, wErr))}
	}
	return []doctorCheck{okCheck("transcripts_dir", dir)}
}

func checkK8sVolume(dir string) []doctorCheck {
	if !k8sModeEnabled() {
		return nil
	}

	st, err := os.Stat(dir)
	if err != nil {
		return []doctorCheck{failCheck("k8s_volume_mount", fmt.Sprintf("data dir %s unavailable: %v", dir, err))}
	}
	if !st.IsDir() {
		return []doctorCheck{failCheck("k8s_volume_mount", fmt.Sprintf("data dir %s is not a directory", dir))}
	}
	if wErr := probeWritable(dir); wErr != nil {
		return []doctorCheck{failCheck("k8s_volume_mount", fmt.Sprintf("data dir %s not writable: %v", dir, wErr))}
	}

	checks := []doctorCheck{okCheck("k8s_volume_mount", fmt.Sprintf("data dir %s writable", dir))}

	var freePct int
	var varfs syscall.Statfs_t
	if syscall.Statfs(dir, &varfs) == nil && varfs.Blocks > 0 {
		freePct = int((varfs.Bavail * 100) / varfs.Blocks)
	}
	// Capacity is reported but never short-circuits the remaining probes. These
	// early-returned, so a volume under 20% free silently dropped the
	// single-writer check - the doctor lost a diagnostic precisely when the
	// system was under stress, and the caller could not distinguish "not run"
	// from "passed".
	switch {
	case freePct > 0 && freePct < 5:
		checks = append(checks, failCheck("k8s_volume_capacity", fmt.Sprintf("%s free space critically low (%d%% free)", dir, freePct)))
	case freePct > 0 && freePct < 20:
		checks = append(checks, warnCheck("k8s_volume_capacity", fmt.Sprintf("%s free space low (%d%% free)", dir, freePct)))
	default:
		checks = append(checks, okCheck("k8s_volume_capacity", fmt.Sprintf("%s capacity ok", dir)))
	}

	lockPath := filepath.Join(dir, ".linkari-single-writer.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return append(checks, failCheck("k8s_single_writer", fmt.Sprintf("open lock file: %v", err)))
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return append(checks, failCheck("k8s_single_writer", fmt.Sprintf("another writer appears active in %s", dir)))
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	checks = append(checks, okCheck("k8s_single_writer", fmt.Sprintf("%s single-writer lock available", dir)))
	return checks
}
