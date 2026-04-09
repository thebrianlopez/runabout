# runabout

[![CI](https://github.com/blo-grindr/runabout/actions/workflows/test.yml/badge.svg)](https://github.com/blo-grindr/runabout/actions/workflows/test.yml)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](https://go.dev)
![Tools](https://img.shields.io/badge/tools-8_CLIs-blue)

Go devtools monorepo — eight CLI tools for shell optimization and personal workflows.

These tools occupy the **Go CLI layer** of an [automation knowledge topology](https://github.com/blo-grindr/infra-knowledge) — they represent patterns that graduated from ad-hoc shell scripts into typed, testable binaries. Each tool emits structured telemetry to a unified JSONL bus, enabling usage-driven decisions about what to build, optimize, or deprecate.

- **mdq** — query fields and tables across markdown files
- **perfgate** — statistical before/after performance gating
- **shellprof** — fish shell function profiler with call graphs
- **hookval** — validate Claude hook signal contract against schema
- **effiscore** — Anthropic API efficiency scoring via Datadog metrics
- **linkari** — Android share → tmux webhook bridge
- **wasend** — send WhatsApp messages from the command line
- **protonexport** — export ProtonMail conversations to markdown

## Status

All 8 tools build and pass tests on Go 1.25. wasend Cloud API support planned, pending permanent access token.

- `linkari` expanded — 7 share profiles, SQLite queue/replay, scoring/archiving pipeline, digest endpoint, Tailscale Funnel (`--tsnet`), TLS support, JSONL observability
- `effiscore` added: Anthropic API efficiency scoring via DD Metrics API — 5 weighted dimensions, composite score with tier classification
- `mdq list` extended with `--group-by dir`, `--exclude`, and glob guard (`--headings` hint)

**Last Updated:** 2026-04-05

## Install

```bash
make install   # go install → ~/go/bin
```

Requires Go 1.25+.

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

Profile fish shell functions to find slow call paths. Shellprof surfaces call-graph data that feeds graduation decisions — identifying which fish functions are hot enough to warrant crystallization into Go.

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

Webhook service that bridges Android share actions to tmux sessions. Receives `POST /share` from Android (HTTP Shortcuts or standalone APK), validates and routes payloads to tmux via a local HTTP server or Tailscale Funnel.

**First run (fresh host):**

```bash
# 1. Scaffold ~/.config/linkari/server.yaml with secretsmanager:// defaults
linkari config init
# 2. Edit secret URIs as needed, then validate
linkari doctor
# 3. Zero-flag production boot
linkari serve
```

**Operations:**

```bash
# Validate secrets + dirs without booting
linkari doctor                          # human-readable  (exit 1 on any fail)
linkari doctor --json                   # structured JSON

# Background daemon (POSIX: macOS, Linux, Termux)
linkari serve --detach
kill $(cat ~/.local/state/linkari/linkari.pid)

# Local dev (skip Tailscale Funnel)
linkari serve --local
LINKARI_LOCAL=1 linkari serve

# Break-glass (all flags; no server.yaml required)
linkari serve --tsnet --tsnet-authkey $TS_AUTHKEY --token $LINKARI_TOKEN \
  --firebase-sa ~/.config/linkari/firebase-sa.json --notify-min-score 10 --debug
```

If `tsnet_authkey` is not configured and `--tsnet` was not set explicitly, `linkari serve` automatically falls back to local-only mode with a WARN log.

**Actions:** `text` (paste into existing pane), `url` (opens new tmux window via `uinit` with profile), `ginit` (parses Jira key, opens `ginit <KEY>`). Seven URL profiles: eng, life, travel, fashion, music, finance, dining. URL windows use `remain-on-exit failed` — auto-close on success, stay open on error.

**Endpoints:**

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/share` | POST | Route share payloads to tmux |
| `/actions` | GET | Action registry for dynamic Android intents |
| `/queue` | GET | List queued items (filter by `?status=`) |
| `/queue/{id}/score` | POST | Score a queued item; auto-archives above profile threshold |
| `/archive` | GET | List archived items (filter by `?profile=`) |
| `/digest` | GET | Recent scored items (last 24h) |
| `/notify` | POST | Score callback → FCM push when above threshold |
| `/register` | POST | Register FCM device token for push notifications |
| `/healthz` | GET | Health check (local only) |
| `/logs` | GET | Last 100 log lines (local only) |
| `/logs/stream` | GET | SSE realtime log stream (local only) |

All endpoints except `/healthz`, `/logs`, and `/logs/stream` require bearer token auth. Rate limited to 30 req/min per IP.

**Queue & replay:** Every share is persisted to SQLite before routing. If tmux is unavailable, the request returns `"queued"` and a background goroutine replays pending items every 30s when tmux comes back.

**Scoring & archiving:** `POST /queue/{id}/score` accepts a score (0-100), tags, and slug. Items auto-archive when score meets the profile threshold (80 default, 70 for finance/dining, disabled for life). Archived high-score items trigger an FCM digest push at most once per hour.

**Prompt iteration (`linkari score`):** Dedicated CLI entrypoint for re-running the scoring pipeline against a single URL without touching the `/share` path, score cache, or tmux. Supports `--prompt-file` to swap in a candidate system prompt, `--no-push` to suppress the FCM digest, and `--dry-run` to skip DB writes entirely. Reuses `Queue.ScoreByURL` + `EnqueueDigestIfDue` verbatim — same dual-writer invariant as `/queue/{id}/score` (EPIC-053). For batch evaluation across a fixture corpus, see EPIC-054 (planned).

**Observability:** JSONL event logging to `~/.config/linkari/linkari_events.jsonl` — emits `linkari_share` and `linkari_digest` events with profile, domain, duration, and status.

## wasend

Send WhatsApp messages from the command line. Supports two transports: **personal** (whatsmeow, QR login) and **cloud** (WhatsApp Business API, token-based — [EPIC-001 M4](docs/epics/PERSONAL_20260319T131921Z_WhatsApp_EPIC-001_whatsapp_business_api_account.md), planned).

```bash
# Personal (default) — authenticate via QR code
wasend login
wasend send -t 15551234567 "Hello from CLI"
echo "Hello" | wasend send -t 15551234567 --stdin
wasend logout

# Cloud API (planned) — headless, token-based
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

## Build

```bash
make build    # builds all 8 binaries to bin/
make install  # go install → ~/go/bin
make core     # builds mdq, perfgate, shellprof, hookval only
make clean    # removes bin/
go test ./... # run root module tests
```

Version, commit, and build date are injected at build time via ldflags.

## Telemetry

Every tool emits schema v2 JSONL events to `~/.automation-metrics/events/YYYY-MM-DD.jsonl` via `emit_jsonl`. Events include command, subcommand, duration, exit code, and flags — correlated by `session_id` across the full [automation topology](https://github.com/blo-grindr/infra-knowledge).

This telemetry feeds topology consumers like `agrad` (graduation signals) and `aregress` (regression detection), which track whether a tool is earning its place at the Go CLI layer or should be simplified back to a shell function.

## Layout

```
cmd/mdq/              # mdq entry point
cmd/perfgate/         # perfgate entry point
cmd/shellprof/        # shellprof entry point
cmd/hookval/          # hookval entry point
cmd/effiscore/        # effiscore entry point
cmd/linkari/        # linkari entry point (separate module)
cmd/wasend/           # wasend entry point (separate module)
cmd/protonexport/     # protonexport entry point (separate module)
internal/mdq/         # markdown parser, query engine, output formatting
internal/perfgate/    # benchmark runner, statistics, gating logic
internal/shellprof/   # fish instrumentation, profiling, call graph
internal/hookval/     # schema parsing, signal validation, doc generation
internal/effiscore/   # DD metrics client, efficiency scoring, topology emission
internal/telemetry/   # CLI telemetry via emit_jsonl
internal/version/     # shared version formatting
```
