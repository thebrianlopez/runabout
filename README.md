# runabout

[![CI](https://github.com/blo-grindr/runabout/actions/workflows/test.yml/badge.svg)](https://github.com/blo-grindr/runabout/actions/workflows/test.yml)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](https://go.dev)
![Tools](https://img.shields.io/badge/tools-7_CLIs-blue)

Go devtools monorepo — seven CLI tools for shell optimization and personal workflows.

These tools occupy the **Go CLI layer** of an [automation knowledge topology](https://github.com/blo-grindr/infra-knowledge) — they represent patterns that graduated from ad-hoc shell scripts into typed, testable binaries. Each tool emits structured telemetry to a unified JSONL bus, enabling usage-driven decisions about what to build, optimize, or deprecate.

- **mdq** — query fields and tables across markdown files
- **perfgate** — statistical before/after performance gating
- **shellprof** — fish shell function profiler with call graphs
- **hookval** — validate Claude hook signal contract against schema
- **linkari** — Android share → tmux webhook bridge
- **wasend** — send WhatsApp messages from the command line
- **protonexport** — export ProtonMail conversations to markdown

## Status

All 7 tools build and pass tests on Go 1.25. wasend Cloud API support planned, pending permanent access token.

- `linkari` expanded: FCM push notifications for high-scoring uinit evaluations (score >= 80), `POST /notify` + `/register` endpoints, `ginit` action handler, `GET /actions` registry, `/logs` ring buffer + `/logs/stream` SSE, `--firebase-sa` flag for Firebase credentials
- `mdq list` extended with `--group-by dir` and `--exclude` flags (EPIC-004)
- `hookval` delivered: `validate`, `gen-docs`, `lint-schema`; schema-driven, 15 unit tests

**Last Updated:** 2026-03-22

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

## linkari

Webhook service that bridges Android share actions to tmux sessions over Tailscale. Receives `POST /share` from Android (HTTP Shortcuts or standalone APK), validates and routes payloads to tmux.

```bash
# Start with debug logging + FCM push notifications
linkari serve --debug --token $LINKARI_TOKEN --firebase-sa ~/.config/linkari/firebase-sa.json

# Or via environment variables
LINKARI_TOKEN=secret LINKARI_FIREBASE_SA=~/.config/linkari/firebase-sa.json linkari serve
```

Actions: `text` (paste into existing pane), `url` (opens new tmux window via `uinit`), `ginit` (parses Jira key from URL or text, opens `ginit <KEY>` in new window). URL windows use `remain-on-exit failed` — auto-close on success, stay open on error.

Endpoints: `POST /share`, `GET /healthz`, `GET /actions` (action registry for dynamic Android intents), `GET /logs` (last 100 lines), `GET /logs/stream` (SSE realtime), `POST /notify` (score callback → FCM push), `POST /register` (FCM device token). Bearer token auth, rate limiting, session auto-create.

URL shares include a score callback — after uinit completes, the tmux window curls `POST /notify` with `$UINIT_SCORE`. If score >= 80 and a device FCM token is registered, linkari sends a push notification to the Android device via Firebase Cloud Messaging.

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
make build    # builds all 7 binaries to bin/
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
cmd/linkari/        # linkari entry point (separate module)
cmd/wasend/           # wasend entry point (separate module)
cmd/protonexport/     # protonexport entry point (separate module)
internal/mdq/         # markdown parser, query engine, output formatting
internal/perfgate/    # benchmark runner, statistics, gating logic
internal/shellprof/   # fish instrumentation, profiling, call graph
internal/hookval/     # schema parsing, signal validation, doc generation
internal/telemetry/   # CLI telemetry via emit_jsonl
internal/version/     # shared version formatting
```
