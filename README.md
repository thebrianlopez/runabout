# runabout

[![CI](https://github.com/thebrianlopez/runabout/actions/workflows/test.yml/badge.svg)](https://github.com/thebrianlopez/runabout/actions/workflows/test.yml)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev)
![Tools](https://img.shields.io/badge/tools-13_CLIs-blue)

Go devtools monorepo - thirteen CLI tools for shell optimization and personal workflows.

These tools represent patterns that graduated from ad-hoc shell scripts into typed, testable binaries. Each tool emits structured telemetry to a unified JSONL bus, enabling usage-driven decisions about what to build, optimize, or deprecate.

- **mdq** - query fields and tables across markdown files
- **perfgate** - statistical before/after performance gating
- **shellprof** - fish shell function profiler with call graphs
- **hookval** - validate Claude hook signal contract against schema
- **effiscore** - Anthropic API efficiency scoring via Datadog metrics
- **linkari** - Android share → tmux webhook bridge with AI-powered triage scoring
- **wasend** - send WhatsApp messages from the command line
- **fetchpage** - headless webpage fetcher via Playwright
- **protonexport** - export ProtonMail conversations to markdown
- **workctl** - fetch and export Atlassian & GitHub work data (weekly/quarterly summaries, career reports)
- **ghwatch** - stream GitHub repository activity to the terminal (push, PRs, workflow runs)
- **ts-go** - tree-sitter-based structural Go source analysis (signatures, types, body extraction, search, rewrite)



## Install

```bash
make install   # go install → ~/go/bin
```

Requires Go 1.26+.

## mdq

Query structured data out of markdown files matching a glob.

Heading extraction correctly ignores `#` comments inside fenced code blocks (` ``` ` and `~~~`).

```bash
# Extract a field value across all epics
mdq query "docs/epics/*.md" --field Status

# Extract a field scoped to a specific table section
mdq query "docs/epics/*.md" --field Owner --table "Milestones"

# Render a table from a named section
mdq table epic.md "Milestones"

# Extract a section's full content
mdq extract epic.md "Background"

# List all headings across files
mdq list "docs/**/*.md" --headings

# Filter headings by level
mdq list "docs/**/*.md" --headings --level 2

# Group by parent directory (KB manifest)
mdq list "docs/*/*.md" --headings --level 1 --group-by dir

# Exclude noisy directories
mdq list "docs/*/*.md" --headings --group-by dir --exclude standups,temp,.output

# JSON output for scripting/agents
mdq list "docs/*/*.md" --headings --level 1 --group-by dir --format json
```

Output formats: `text` (default), `json`, `table`. JSON output enables direct consumption by AI agents and automation consumers without shell parsing. `--group-by dir` produces `[{folder, count, titles}]` in JSON mode.

## perfgate

Benchmark commands and enforce statistical performance thresholds.

```bash
# Benchmark a command (10 runs, 2 warmup)
perfgate run --cmd "fish -c 'nowdate'" --runs 10 --save baseline.json

# Compare two results
perfgate compare --before baseline.json --after current.json

# CI gate: fail if regression exceeds 5%
perfgate gate --cmd "fish -c 'nowdate'" --baseline baseline.json --max-regression 5
```

Stats reported: mean, median, P95, stddev, min, max. Exit code 1 on gate failure. Perfgate provides the measurement layer for detecting when shell functions are fast enough to stay at the fish layer vs. when they should graduate to a compiled binary.

## shellprof

Profile fish shell functions to find slow call paths. Shellprof surfaces call-graph data that feeds graduation decisions - identifying which fish functions are hot enough to warrant crystallization into Go.

```bash
# Profile a function (default: 3-level call depth)
shellprof profile nowdate

# Deep profile up to 5 levels
shellprof profile nowdate --depth 5

# Single-run trace (full depth)
shellprof trace nowdate

# Output formats: text (default), json, flame
shellprof profile nowdate --format flame
```

## hookval

Validate the Claude `UserPromptSubmit` hook signal contract against a machine-readable schema.

```bash
# Run hook, validate all 8 emitted signals against schema
hookval validate

# Generate Hook Context Signals markdown table (for CLAUDE.md insertion)
hookval gen-docs

# Validate schema file is well-formed
hookval lint-schema

# Override defaults
hookval validate --schema ~/.claude/hook-signal-schema.yaml --hook ~/.claude/hooks/prompt-context.fish
```

Exit 0 = all signals pass. Exit 1 = per-signal violation report. Prevents silent doc/impl drift in hook context injection.

## effiscore

Compute per-user Anthropic API efficiency signals from Datadog metrics. Data adapter for the ClaudeConfig AI usage scoring rubric.

```bash
# Plain text report (default 7d window)
effiscore score --user brian_lopez

# JSON output (ClaudeConfig M2 contract)
effiscore score --user brian_lopez --json

# Custom window
effiscore score --user brian_lopez --window 14d
```

Five dimensions: Cache Hit Rate, Cache Reuse Factor, I/O Ratio, Token Savings, Model Mix (Haiku %). Composite score weighted 30/25/20/15/10 with tier classification (poor/fair/good/excellent). Requires `DD_API_KEY` and `DD_APP_KEY` environment variables. Every run emits `cloud_llm_efficiency` and `dd_api_health` events to the topology bus.

## linkari

Webhook service that bridges Android share actions to tmux sessions with AI-powered content triage. Receives `POST /share` from Android (HTTP Shortcuts or standalone APK), validates and routes payloads to tmux via a local HTTP server or Tailscale Funnel. Shared URLs are scored by an LLM triage pipeline (Haiku) using per-profile prompt manifests, with auto-archiving and FCM push notifications for high-value content.

**First run (fresh host):**

```bash
# 1. Scaffold ~/.config/linkari/config.toml with ${secretsmanager:...} defaults
linkari config init
# 2. Edit secret references as needed, then validate
linkari doctor
# 3. Zero-flag production boot
linkari serve
```

**Operations:**

```bash
# Validate secrets + dirs without booting
linkari doctor                          # human-readable  (exit 1 on any fail)
linkari doctor --json                   # structured JSON
# In K3s pods, set LINKARI_K8S_MODE=true to include volume health checks

# SQLite durability tools
linkari db backup --queue-db ~/.config/linkari/queue.db --dest /tmp/queue.db
linkari db restore --queue-db ~/.config/linkari/queue.db --src /tmp/queue.db

# Background daemon (POSIX: macOS, Linux, Termux)
linkari serve --detach
kill $(cat ~/.local/state/linkari/linkari.pid)

# Local dev (skip Tailscale Funnel)
linkari serve --local
LINKARI_LOCAL=1 linkari serve

# Break-glass (all flags; no config.toml required)
linkari serve --tsnet --tsnet-authkey $TS_AUTHKEY --token $LINKARI_TOKEN \
  --firebase-sa ~/.config/linkari/firebase-sa.json --notify-min-score 10 --debug
```

If `tsnet_authkey` is not configured and `--tsnet` was not set explicitly, `linkari serve` automatically falls back to local-only mode with a WARN log. Config is hot-reloadable via SIGHUP (actions, archive thresholds, push config, watchdog tuning).

**Structured capture config (`~/.config/linkari/config.toml`):**

| Field | Type | Description |
|-------|------|-------------|
| `artifact_dir` | string | Directory for capture artifacts (e.g. `~/docs/captures`) |
| `artifact_filename_template` | string | Go `text/template` for artifact filenames (e.g. `{{.Date}}_{{.Key}}.md`) |
| `domain_routes` | map[string]string | Per-domain action override - maps domain patterns to action names (e.g. `"jira.atlassian.net" = "capture_jira_auto"`) |
| `post_capture_command` | string | Optional shell command executed after the artifact is written. Template vars: `{{.Key}}`, `{{.ArtifactPath}}`, `{{.URL}}`, `{{.Date}}`. Execution is best-effort - non-zero exit logs `capture_command_error` but the queue row stays `status=captured`. Only `{{.Key}}` is validated (regex-gated) before substitution; no other content from the fetched payload reaches the command. |

**Actions:** `text` (paste into existing pane), `url` (opens new tmux window via `uinit` with profile), `ginit` (parses Jira key, opens `ginit <KEY>`), `capture_jira_auto` (action kind `KindCapture` - intercepts Jira browse URLs and writes a structured markdown artifact to `docs/captures/` with YAML frontmatter instead of routing to the LLM scorer), `capture_confluence_auto` (same `KindCapture` path for Confluence page URLs - fetches ADF content and converts to markdown; unknown ADF node types are skipped with an `adf_unsupported_block` warning and the rest of the page still renders), `capture_github_pr_auto` (same `KindCapture` path for GitHub PR URLs matching `github.com/.*/pull/` - fetches PR metadata via GitHub REST API; works unauthenticated at 60 req/hr or authenticated when a GitHub token is configured; `ArtifactKey` format: `{owner}-{repo}-{prNumber}`; non-PR GitHub URLs fall through to the existing fetch path). Seven URL profiles: eng, life, travel, fashion, music, finance, dining. URL windows use `remain-on-exit failed` - auto-close on success, stay open on error. Domain heuristics auto-classify URLs into profiles (e.g. github.com → eng, booking.com → travel). Login-wall domains (Instagram, X/Twitter, Facebook) are pre-filtered to avoid wasting Haiku calls on inaccessible content; LinkedIn `/pulse/` articles are exempted as publicly accessible.

**Server-side scoring:** URL actions with `ServerScore: true` bypass the tmux → fish pipeline entirely - a goroutine fetches page content via Jina Reader, evaluates with Haiku, scores, archives, and sends FCM push. Unsupported domains (YouTube, Spotify, TikTok, etc.) return early without burning a Haiku call.

**Auth model (permanent):** All LLM scoring shells out to the `claude` CLI binary, which authenticates via the user's own Claude Code subscription (OAuth2 device flow). Linkari does not support Anthropic API keys, API client libraries, or direct Anthropic HTTP calls. Each user installs and runs Linkari on their own laptop - there is no shared API key deployment model.

**Execution runtime:** Three runtime implementations - `LocalRuntime` (default, subprocess), `ContainerRuntime` (gVisor-sandboxed via containerd for ffmpeg/whisper), and `HybridRuntime` (routes ffmpeg/whisper through containers but keeps claude CLI local for OAuth2 reasons). Controlled by `sandbox.enabled` in `config.toml`.

**Shield:** `X-Linkari-Client` header validation on the Funnel mux. Two modes: `log` (default, debug logging) and `enforce` (403 on invalid/missing headers). CORS preflight (OPTIONS) is always exempt.

**Integration source control (`[server.sources]`):** Each ingestion source can be independently enabled or disabled via `config.toml`. All flags default to `true` - omitting the block is identical to all-enabled. SIGHUP reloads the flags; a toggled-off source stops on its next poll tick. Disabled sources emit a `source_disabled` event.

| Flag | Default | Controls |
|------|---------|---------|
| `youtube_watch_later_enabled` | `true` | `yt_watch_later` Watch Later playlist sync |
| `youtube_monitored_enabled` | `true` | `yt_monitored` subscription channel poller |
| `youtube_liked_enabled` | `true` | `yt_liked` Liked Videos sync |
| `bluesky_firehose_enabled` | `true` | `bsky_firehose` Bluesky relay scoring |

**YouTube sub-behavior toggles (`[server.youtube]`):** Two pipeline stages - Enqueue and Transcribe (Whisper audio fallback) - can be independently gated per source. Dedup (`seen_content`) always runs regardless of flags. All flags default to `true`.

| Flag | Default | Controls |
|------|---------|---------|
| `auto_enqueue_subscriptions` | `true` | Write `yt_monitored` items to score queue |
| `auto_enqueue_watch_later` | `true` | Write `yt_watch_later` items to score queue |
| `transcribe_subscriptions` | `true` | Whisper audio fallback for `yt_monitored` items with no subtitles |
| `transcribe_watch_later` | `true` | Whisper audio fallback for `yt_watch_later` items with no subtitles |

Note: `transcribe=false` only skips the Whisper stage - yt-dlp subtitle extraction still runs, and scoring proceeds normally if subtitles are found.

**Database durability config (`[server.db]`):** EPIC-223 F3.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `backup_path` | string | `~/.local/state/linkari/backups/latest.db` | Absolute path to the backup database file. `linkari doctor` checks backup freshness (ok ≤24h, warn ≤72h, fail >72h). When configured, the `linkari db backup` command writes a `.backup-meta.json` sidecar that tracks `completed_at`, `bytes`, and `duration_ms`. The check is optional - omitting this field skips the backup freshness check in doctor. |

**Endpoints:**

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/share` | POST | Route share payloads to tmux |
| `/actions` | GET | Action registry for dynamic Android intents |
| `/queue` | GET | List queued items (filter by `?status=`) |
| `/queue/{id}/score` | POST | Score a queued item; auto-archives above profile threshold |
| `/queue/{id}/outcome` | POST | Record user outcome (opened, saved, dismissed) for a scored item |
| `/queue/{id}/feedback` | POST | Submit feedback (thumbs up/down, correction) on a scored item |
| `/queue/slug/{slug}/feedback` | POST | Feedback by workspace slug (convenience alias) |
| `/queue/slug/{slug}/outcome` | POST | Outcome by workspace slug (convenience alias) |
| `/profiles/stats` | GET | Per-profile scoring statistics and feedback summary |
| `/archive` | GET | List archived items (filter by `?profile=`, `?status=captured` for capture-only rows) |
| `/digest` | GET | Recent scored items (last 24h) |
| `/notify` | POST | Score callback → FCM push when above threshold |
| `/register` | POST | Register FCM device token for push notifications |
| `/search` | POST | Full-text search over scored queue items |
| `/push/test` | POST | Test FCM push delivery |
| `/auth/google` | POST | Google ID token verification for mobile clients |
| `/auth/invite` | POST | Redeem invite code to create a user account |
| `/admin/invite` | POST | Generate invite codes (operator bearer token required) |
| `/health` | GET | Minimal health probe (Funnel-safe, returns `{"status":"ok"}`) |
| `/healthz` | GET | Health check with DB status (local only) |
| `/logs` | GET | Last 100 log lines (local only) |
| `/logs/stream` | GET | SSE realtime log stream (local only) |

All endpoints except `/healthz`, `/logs`, `/logs/stream`, and `/auth/*` require bearer token auth (or Google ID token for authenticated users). Rate limited to 30 req/min per IP.

**Adaptive assistant (EPIC-072):** Feedback and outcome signals flow back from the client via `/queue/{id}/feedback` and `/queue/{id}/outcome` (or by slug). A tag-based clustering engine (Jaccard similarity on `topic_tags`) detects content themes across scored items. When a scored item meets the profile threshold, action routing dispatches it to either Jira ticket drafting (`draft_jira_ticket`) or research digest append (`append_research_digest`). Per-profile statistics are available via `GET /profiles/stats`.

**Classification cascade (EPIC-079):** `classifyShareRequestFast` (stages 1-5, no LLM) runs synchronously pre-enqueue. `classifyShareRequest` (stages 1-6, includes LLM) runs async in `scoreAsync`. File shares fall through to stage-6 LLM classify using synthesized metadata. Stage-6 has two distinct invocation paths: (a) URL shares post-Jina fetch, where the fetched content is reclassified when stage 5 returned `url_domain_fallback`; (b) non-URL shares with metadata hints (ExtraSubject/ExtraText/Filename), which are classified from synthesized metadata before content fetch. Both call `classifyContentProfile` but at different pipeline stages.

**Vision scoring (EPIC-079 M3):** `HaikuVisionEvaluator` scores local image files via `claude --print --allowedTools Read --output-format json --json-schema <schema>`. Selected when `req.Type == "image"` and the file is readable. The score-first short-circuit instruction tells the model to respond immediately with `{"score": 0, "verdict": "personal photo"}` for camera-roll images with no engineered content, bypassing full rubric evaluation.

**Vision prefilter gates:** Three gates run before vision eval in `scoreAsync`, checked in order (first match wins):

| Gate | Config Key | Default | Trigger |
|------|-----------|---------|---------|
| `oversize_file` | `image_noise_gate_max_bytes` | 15 MB | File exceeds max bytes |
| `camera_photo_gate` | *(heuristic, no config)* | - | Gallery app + camera filename pattern (`IMG_/DSC_/PXL_` prefix) + no text metadata. Bypassed when `image_text_extraction_enabled=true` and extracted text exceeds `image_short_circuit_bypass_min_chars`. |
| `noise_gate_min_size` | `image_noise_gate_min_bytes` | 1 KB | File below min bytes with no text metadata |

When any gate fires, vision eval is skipped and the share falls through to metadata-only scoring. Each gate emits a `score_prefilter_skip` event.

**Image text extraction pre-pass (EPIC-122):** When `image_text_extraction_enabled = true` (default: `false`), a Claude Haiku vision call runs before scoring for all `type=image` shares that pass the noise gate. The call transcribes all visible text verbatim (`model: claude-haiku-4-5`, 30s timeout, `max_concurrency=1`). When extracted text exceeds `image_short_circuit_bypass_min_chars` (default: `20`), the camera-photo short-circuit is suppressed and the transcript is included in the scoring prompt - 9 fields instead of 5 (`calling_package`, `relative_path`, `is_screenshot`, `url`, `transcribed_text` appended when present). Transcript files are persisted to `transcripts_dir` at `YYYYMMDD_<rowID>_IMG_<slug>.md`. CLI failure or timeout falls through to current scoring behavior; the share is never blocked. Feature is disabled by default for safe rollout.

| Config Field | Type | Default | Description |
|-------------|------|---------|-------------|
| `image_text_extraction_enabled` | bool | `false` | Enable Haiku vision pre-pass before image scoring. Set to `true` in `[server]` to activate. |
| `image_short_circuit_bypass_min_chars` | int | `20` | Minimum extracted-text length (chars) required to suppress the camera-photo short-circuit and activate full rubric. |

**Rejection taxonomy:** Shares can be rejected (prefiltered) before or during scoring. Each reason code produces a specific queue row effect and optionally triggers an FCM push notification to the user.

*User-notified prefilters (FCM push via `enqueuePrefilterPush`):*

| Reason Code | Trigger | Queue Effect | User-Facing Verdict |
|-------------|---------|-------------|---------------------|
| `unsupported_pipeline` | URL matches streaming domains (YouTube, Spotify, TikTok, Twitch, SoundCloud, Netflix, Vimeo, Rumble, Dailymotion). Domain list configurable via `unsupported_pipeline_domains` in `config.toml`. | **Pre-enqueue (primary):** no queue row created, `score_prefilter_skip` event emitted (`phase: "pre_enqueue"`). **Safety-net in scoreAsync:** `MarkFailedWithReason` (fires only when `scoreAsync` is called directly, bypassing the HTTP handler). | "Video platform - not yet supported" |
| `login_wall_domain` | URL matches login-walled domains (Instagram, X/Twitter, Facebook, LinkedIn non-`/pulse/`) | No queue row created (pre-enqueue) | "Login-walled site - can't access content" |
| `screenshot_no_text` | Screenshot with empty ExtraSubject + ExtraText | `MarkFailedWithReason` | "Screenshot had no extractable text" |
| `fetch_failed` | Jina Reader HTTP error | `MarkFailedWithReason` | "Could not fetch page content" |
| `empty_content` | Jina response body empty after truncation | `MarkFailedWithReason` | "Page had no readable content" |
| `no_metadata` | File share with no ExtraSubject, ExtraText, Filename, or MimeType | `MarkFailedWithReason` | "File had no scorable metadata" |
| `template_error` | Profile template load failure | `MarkFailedWithReason` | "Scoring configuration error" |

*Internal-only prefilters (no FCM push):*

| Reason Code | Trigger | Queue Effect |
|-------------|---------|-------------|
| `eval_failed` | `Evaluate()` returns error | `MarkFailedWithReason` |
| `score_persist_failed` | Score DB write error | `MarkFailedWithReason` |
| `scoring_timeout` | Relayed row exceeds `relayed_watchdog_max_age` (default 15m) | `MarkFailedWithReason` (watchdog) |

*Audio pipeline prefilters (no FCM push):*

| Reason Code | Trigger |
|-------------|---------|
| `ffmpeg_failed` | ffmpeg audio conversion error |
| `wav_stat_failed` | WAV file stat failure |
| `segment_failed` | ffmpeg segmentation error |
| `transcription_failed` | Whisper single-pass failure |
| `transcription_failed_chunk_N` | Whisper chunk N failure |
| `empty_transcript` | Whisper produces empty output |
| `template_load_failed` | Voice note synopsis template missing |

FCM push for prefiltered shares is controlled by `notify_on_prefilter_skip` in `config.toml` (default `false`; Go runtime default is `true` but the TOML zero-value override wins when the key is absent). Reasons not in the `prefilterVerdicts` map fall back to "Share could not be scored".

**Vision telemetry:** The `claude` CLI's `--output-format json` envelope reports `input_tokens` reflecting only text tokens from the CLI wrapper, not image tokens. `ImageTokensEstimated` is back-calculated when `InputTokens < 100 && CostUSD > 0.01` using Haiku 4.5 pricing ($1.00/MTok input, $5.00/MTok output): `imageCost = totalCost - (inputTokens × $1/MTok + outputTokens × $5/MTok)`, then `imageTokensEstimated = imageCost / $1/MTok`. This value is emitted in `vision_token_correction` events and slog but is **not persisted to SQLite** (known limitation).

**Event types:** Key pipeline events emitted to `linkari_events.jsonl`:

| Event Type | Description |
|------------|-------------|
| `classify_stage_win` | Classification cascade resolved a profile (fields: `profile`, `classify_source`, `phase`) |
| `score_prefilter_summary` | Per-share pipeline summary (fields: `prefilter_stage`, `eval_skipped`, `latency_ms`, `cost_usd`, `type`) - always emitted via defer |
| `score_prefilter_skip` | Vision gate or login-wall rejection (fields: `stage`, `file_size` or `filename`) |
| `score_repair_turn` | Scoring required a repair turn (fields: `cost_usd`, `profile`, `type`) |
| `vision_token_correction` | Image token back-calculation applied (fields: `input_tokens`, `image_tokens_estimated`, `cost_usd`) |
| `score_cost_exceeded` | Scoring cost exceeded threshold (fields: `cost_usd`, `threshold_usd`, `profile`) |
| `score_cluster_semaphore_full` | Scoring semaphore saturated (slog only, not in JSONL event log) |

**Auth:** Google Sign-In (ID token verification against Google JWKS) for mobile clients. Invite-code flow for onboarding new users; operator bearer token generates codes via `POST /admin/invite`.

**Queue & replay:** Every share is persisted to SQLite before routing. If tmux is unavailable, the request returns `"queued"` and a background goroutine replays pending items every 30s when tmux comes back. A `RelayedWatchdog` marks rows stuck in `relayed` status past a configurable max age as `failed` with `scoring_timeout`. A `SnapshotWorker` periodically writes a clean copy of `queue.db` via `VACUUM INTO` for crash recovery.

**Scoring & archiving:** `POST /queue/{id}/score` accepts a score (0-100), tags, and slug. Items auto-archive when score meets the profile threshold (80 default, 70 for finance/dining, disabled for life). Archived high-score items trigger an FCM digest push at most once per hour (configurable per-profile throttle via `config.toml`). Push delivery uses a durable outbox pattern (`push_outbox` table) with exponential backoff and a 24h TTL.

**CLI subcommands:**

| Subcommand | Description |
|------------|-------------|
| `serve` | Start the webhook HTTP server (local + optional Tailscale Funnel) |
| `config init` | Scaffold `~/.config/linkari/config.toml` |
| `doctor` | Validate secrets and directories without booting |
| `score <url>` | Run the triage scoring pipeline against a single URL |
| `score-write` | Write a pre-computed score directly (legacy/scripting) |
| `triage <url>` | Run profile-aware LLM triage (Haiku) and persist the verdict |
| `eval capture` | Snapshot triage inputs+outputs into a fixture JSON |
| `eval run` | Replay fixtures against the scorer; exit non-zero on regression |
| `eval refresh-goldens` | Re-score fixture goldens against current prompt manifests |
| `profile lint` | Validate profile YAML manifests against the v1 schema |
| `search <query>` | Full-text search over scored queue items |
| `backfill [dir]` | Ingest existing `_score.json` files into the queue database |
| `tag-backfill` | Re-process scored items missing topic tags through tag extraction |
| `digest` | List recent scored items (CLI equivalent of `GET /digest`) |
| `completion` | Generate shell completions (fish/bash/zsh/powershell) |

**Triage pipeline:** `linkari triage` loads a per-profile YAML manifest from `docs/prompts/profiles/`, renders a system prompt via `text/template`, shells out to the `claude` CLI for a single-turn Haiku call, parses the returned markdown verdict, and persists the score through `Queue.ScoreByURL`. The eval harness (`linkari eval`) supports capture/replay of fixture corpora for regression testing across prompt iterations.

**Observability:** JSONL event logging to `~/.config/linkari/linkari_events.jsonl`. Key event types: `linkari_share`, `linkari_digest`, `share_scoring_timeout`, `score_async_*` (server-side pipeline stages), `push_attempt`, `snapshot_written`, `shield_blocked`, `config_reloaded`. Structured logging via slog with configurable format (`text`/`json`) and level. SIGHUP hot-reloads config and emits `config_reloaded`.

**K3s deployment (Timoni CUE):** Linkari can be deployed to a self-hosted K3s cluster with durable storage and automated backups. The Timoni module (`infra/timoni/templates/`) provisions two PersistentVolumeClaims (10Gi each, default `local-path` storage class):

- `linkari-data`: Live database (`queue.db`, Tailscale state). `replicas: 1`, `Recreate` strategy, single-writer invariant enforced by `PodDisruptionBudget` (`maxUnavailable: 0`).
- `linkari-backup`: Backup snapshots written by the sidecar (`linkari db backup --interval 6h`). Mounted read-only by the main pod's health checks, read-write by the optional backup sidecar.

Operator overrides via `values.cue`:
```cue
values: {
  backupStorage:      "50Gi"      // Custom backup PVC size
  backupStorageClass: "nvme"      // Alternative storage class (e.g. different disk)
  backupInterval:     "12h"       // Snapshot frequency
  backupPath:         "/backup/linkari/queue.db"  // Snapshot dest (on backup PVC)
}
```

Deploy:
```bash
timoni build linkari ./infra/timoni -n linkari -o yaml | kubectl apply -f -   # dry-run first
make k8s-diff    # Preview (dry-run)
make k8s-deploy  # Apply to k3d
```

**Backup sidecar:** An optional init container runs `linkari db backup --interval <dur> --overwrite --dest <path>` in a loop, producing snapshots to the `linkari-backup` PVC. The sidecar opens the database read-only (separate WAL reader) so it does not contend with the main process's single pooled connection or violate the single-writer invariant. Failed cycles are non-fatal - the prior good snapshot is retained and the loop continues. Off-node replication (e.g. rsync to S3) remains an infrastructure concern (CronJob outside the pod). SIGTERM gracefully exits after the in-flight snapshot completes, so pod termination does not interrupt backups mid-write.

**Migration runbook:** For one-time Mac→K3s cutover, keep the Mac authoritative until the in-pod verification passes: (1) `linkari db backup`, (2) `PRAGMA integrity_check`, (3) rsync the backup into the `linkari-backup` PVC and verify `sha256sum`, (4) `make k8s-deploy`, (5) confirm `db-restore` logs show `seed complete`, (6) verify in-pod integrity plus per-table row-count parity before cutover.

## wasend

Send WhatsApp messages from the command line. Supports two transports: **personal** (whatsmeow, QR login) and **cloud** (WhatsApp Business API, token-based - [EPIC-001 M4](docs/epics/PERSONAL_20260319T131921Z_WhatsApp_EPIC-001_whatsapp_business_api_account.md), planned).

```bash
# Personal (default) - authenticate via QR code
wasend login
wasend send -t 15551234567 "Hello from CLI"
echo "Hello" | wasend send -t 15551234567 --stdin
wasend logout

# Cloud API (planned) - headless, token-based
wasend send --api cloud -t 15551234567 "Hello from CLI"
wasend send --api cloud -t 15551234567 --template hello_world
```

## protonexport

Export ProtonMail conversations to markdown files with YAML front-matter.

```bash
# Export emails matching a contact
protonexport export -u user@proton.me -p password -c contact@example.com

# Specify output directory and worker count
protonexport export -u user@proton.me -p password -c contact@example.com -o ./export -w 20
```

Credentials can also be set via `PROTON_USERNAME`, `PROTON_PASSWORD`, `PROTON_SENDER` environment variables.

## fetchpage

Fetch fully rendered HTML from a URL using headless Chromium via Playwright. Waits for JS rendering and network idle before outputting to stdout.

```bash
# Basic fetch (outputs rendered HTML to stdout)
fetchpage https://example.com

# With stealth mode (anti-detection fingerprint)
fetchpage https://example.com --stealth

# Custom timeout and post-load wait
fetchpage https://example.com --timeout 60 --wait 5

# Use chromium instead of chrome
fetchpage https://example.com --channel chromium
```

Requires one-time Playwright setup: `make setup-fetchpage`.

## workctl

Fetch and export Atlassian & GitHub work data. Reads Jira, Confluence, and GitHub to produce weekly/quarterly summaries, performance reviews, career progression reports, and trend analysis. Config lives in `~/.config/workctl/config.yaml`; cache is SQLite (optionally encrypted via `WORKCTL_CACHE_PASSPHRASE`).

```bash
workctl init                        # scaffold config
workctl weekly                      # weekly work summary
workctl quarterly                   # quarterly summary
workctl review                      # performance review generation
workctl insights                    # work insights across sources
workctl career                      # career progression analysis
workctl trends                      # trend analysis
workctl cache --refresh             # force cache refresh
```

## ghwatch

Stream GitHub repository activity to the terminal. Polls push events, pull requests, and workflow runs at a configurable interval; deduplicates across restarts via a local state file. Outputs human-readable text or JSONL.

```bash
ghwatch --repo owner/repo                          # watch all event types (30s interval)
ghwatch --repo owner/repo --events push,pr         # filter event types
ghwatch --repo owner/repo --interval 60s --json    # JSONL output
ghwatch --repo owner/repo --since 4h               # lookback window on first run
```

Requires `$GITHUB_TOKEN` or `--token`.

## ts-go

Structural Go source analysis using tree-sitter. Parses files in <5ms without a Go workspace or build context - use it to orient on large files before reading.

```bash
# List all function/method signatures with line ranges
ts-go funcs cmd/linkari/handler.go
ts-go funcs --format compact cmd/linkari/handler.go   # lower token cost

# Extract a single function body + doc comments by name
ts-go extract cmd/linkari/handler.go handleShare

# List type declarations (structs, interfaces, aliases) with field counts
ts-go types cmd/linkari/queue.go
ts-go types --format compact cmd/linkari/queue.go

# Search files for a tree-sitter S-expression pattern
ts-go search '(function_declaration name: (identifier) @name)' 'cmd/linkari/*.go'
ts-go search '(call_expression function: (identifier) @fn (#eq? @fn "slog.Info"))' 'cmd/**/*.go'

# Rewrite source by matching a pattern and applying a template
ts-go rewrite '(function_declaration name: (identifier) @name)' 'func renamed_@name()' '*.go' --diff
ts-go rewrite '(comment) @c' '' 'main.go' --write
```

Output formats: `json` (default), `compact`. `--diff` and `--write` flags available for `rewrite`. Separate module (`cmd/ts-go/go.mod`) to isolate CGo tree-sitter dependencies from the root module.

## bmux

Remote tmux session manager over SSH. Manages persistent tmux sessions on remote hosts via a local daemon, with auto-reconnect on disconnect.

```bash
bmux start            # start the local daemon
bmux serve            # serve a remote tmux session over SSH
bmux attach           # attach to a running session
bmux status           # show daemon and session status
bmux stop             # stop the daemon
bmux doctor           # validate config and SSH connectivity
bmux config           # manage config file (~/.config/bmux/config.yaml)
bmux completion fish  # generate fish completions
```

Separate module (`cmd/bmux/go.mod`) to isolate `golang.org/x/crypto` SSH dependencies from the root module. Internal packages (`config`, `ssh`, `daemon`, `pane`, `bridge`, `reconnect`) live under `cmd/bmux/internal/`.

```bash
make install-bmux-completions   # install + register fish completions
```

## Build

```bash
make build    # builds all 12 binaries to bin/
make install  # go install → ~/go/bin
make core     # builds mdq, perfgate, shellprof, hookval, effiscore
make clean    # removes bin/
go test ./... # run root module tests
cd cmd/linkari && go test ./...    # linkari (separate module)
cd cmd/wasend && go test ./...     # wasend (separate module)
cd cmd/protonexport && go test ./... # protonexport (separate module)
cd cmd/workctl && go test ./...    # workctl + ghwatch (separate module)
```

Version, commit, and build date are injected at build time via ldflags. The Go workspace (`go.work`) ties the root module to six separate-module satellites: linkari, fetchpage, ts-go, wasend, protonexport, and workctl.

**Container images** (linkari sandbox runtime):

```bash
make container-build                        # native platform (dev iteration)
make container-push IMAGE_REGISTRY=ghcr.io/blo-grindr/linkari  # multi-arch push
make lima-start                             # start Lima gVisor VM
make lima-test                              # gVisor smoke test
make integration-test                       # container integration tests (requires Lima)
```

Three images: `ffmpeg` (audio conversion), `whisper` (speech-to-text), `claude-sandbox` (LLM invocation). All support linux/amd64 + linux/arm64 via `docker buildx`.

## Telemetry

Every tool emits schema v2 JSONL events to `~/.automation-metrics/events/YYYY-MM-DD.jsonl` via `emit_jsonl`. Events include command, subcommand, duration, exit code, and flags - correlated by `session_id` across all tools in the suite.

This telemetry feeds topology consumers like `agrad` (graduation signals) and `aregress` (regression detection), which track whether a tool is earning its place at the Go CLI layer or should be simplified back to a shell function.

## Layout

```
cmd/bmux/             # bmux entry point (separate module - golang.org/x/crypto SSH deps isolated)
  internal/           # config, ssh, daemon, pane, bridge, reconnect, io packages
cmd/mdq/              # mdq entry point (root module)
cmd/perfgate/         # perfgate entry point (root module)
cmd/shellprof/        # shellprof entry point (root module)
cmd/hookval/          # hookval entry point (root module)
cmd/effiscore/        # effiscore entry point (root module)
cmd/fetchpage/        # fetchpage entry point (separate module)
cmd/ts-go/            # ts-go entry point (separate module - tree-sitter CGo deps isolated)
cmd/linkari/          # linkari entry point (separate module, ~80 files)
cmd/wasend/           # wasend entry point (separate module)
cmd/protonexport/     # protonexport entry point (separate module)
cmd/workctl/          # workctl module root (separate module)
  cmd/workctl/        # workctl binary entry point
  cmd/ghwatch/        # ghwatch binary entry point (shares workctl go.mod)
  internal/           # workctl + ghwatch shared internals
internal/mdq/         # markdown parser, query engine, output formatting
internal/perfgate/    # benchmark runner, statistics, gating logic
internal/shellprof/   # fish instrumentation, profiling, call graph
internal/hookval/     # schema parsing, signal validation, doc generation
internal/effiscore/   # DD metrics client, efficiency scoring, topology emission
internal/telemetry/   # CLI telemetry via emit_jsonl
internal/version/     # shared version formatting
container/            # Dockerfiles for gVisor sandbox runtime (ffmpeg, whisper, claude-sandbox)
```

## Status

Active development. 15+ tools building and passing tests. Goreleaser + Homebrew distribution live as of v0.2.14.

- `castex` CLI shipped: `init`, `report`, `directive`, `sync`, `doctor` subcommands (agent observability + registry health)
- `chain-eval v0.2.14`: `index` subcommand live, epic-to-chain linkage fixed (310 → 124 orphans), CI all green
- `plaid-service` stable on personal Tailnet; hourly sync, 390+ transactions in SQLite

**Last Updated:** 2026-06-09
