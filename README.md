# runabout

Go devtools monorepo — five CLI tools for shell optimization and personal workflows.

- **mdq** — query fields and tables across markdown files
- **perfgate** — statistical before/after performance gating
- **shellprof** — fish shell function profiler with call graphs
- **wasend** — send WhatsApp messages from the command line
- **protonexport** — export ProtonMail conversations to markdown

## Status

All 5 tools build and pass tests on Go 1.25. Monorepo standardization complete: unified telemetry, version helper, code extraction, and restored protonexport with vendored go-proton-api.

- Standardized all modules on Go 1.25.0
- Added telemetry to wasend and protonexport (copied pattern from `internal/telemetry`)
- Extracted wasend logic into `message.go`, `client.go`; added protonexport helper tests

**Last Updated:** 2026-03-18

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
```

Output formats: `text` (default), `json`, `table`.

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

Stats reported: mean, median, P95, stddev, min, max. Exit code 1 on gate failure.

## shellprof

Profile fish shell functions to find slow call paths.

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

## wasend

Send WhatsApp messages from the command line via whatsmeow.

```bash
# Authenticate (scan QR code)
wasend login

# Send a message
wasend send -t 15551234567 "Hello from CLI"

# Pipe message from stdin
echo "Hello" | wasend send -t 15551234567 --stdin

# Remove session
wasend logout
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
make build    # builds all 5 binaries to bin/
make install  # go install → ~/go/bin
make core     # builds mdq, perfgate, shellprof only
make clean    # removes bin/
go test ./... # run root module tests
```

Version, commit, and build date are injected at build time via ldflags.

## Layout

```
cmd/mdq/              # mdq entry point
cmd/perfgate/         # perfgate entry point
cmd/shellprof/        # shellprof entry point
cmd/wasend/           # wasend entry point (separate module)
cmd/protonexport/     # protonexport entry point (separate module)
internal/mdq/         # markdown parser, query engine, output formatting
internal/perfgate/    # benchmark runner, statistics, gating logic
internal/shellprof/   # fish instrumentation, profiling, call graph
internal/telemetry/   # CLI telemetry via emit_jsonl
internal/version/     # shared version formatting
```
