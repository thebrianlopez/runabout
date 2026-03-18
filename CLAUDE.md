# Runabout

Go devtools monorepo — five CLI tools for shell optimization and personal workflows.

## Module

- Root module: `github.com/blo-grindr/runabout`
- Go: 1.25
- Core dependency: `github.com/spf13/cobra`

## Architecture

**Core tools** (mdq, perfgate, shellprof) live in the root module. Each has a thin `cmd/` entrypoint that wires Cobra subcommands, and all logic lives in `internal/`.

**Satellite tools** (wasend, protonexport) have separate `go.mod` files under `cmd/` to isolate heavy dependencies (whatsmeow/sqlite, ProtonMail crypto) from the root module. They are included in the Go workspace via `go.work`.

### Why satellites copy telemetry

Satellite modules can't import `internal/` from the root module (Go visibility rules). Since the telemetry layer is ~80 lines with no internal dependencies beyond cobra/pflag, each satellite carries its own copy (`telemetry.go`) rather than introducing a shared module for one file.

### Module isolation

| Module | Path | Heavy deps |
|--------|------|-----------|
| Root | `go.mod` | cobra only |
| wasend | `cmd/wasend/go.mod` | whatsmeow, sqlite3, protobuf |
| protonexport | `cmd/protonexport/go.mod` | go-proton-api (vendored), gluon, crypto |

## Build

```bash
make build      # builds all 5 binaries to bin/
make install    # go install → ~/go/bin
make clean      # removes bin/
make core       # builds only mdq, perfgate, shellprof
```

The Makefile has two groups: `CORE` (root module tools built with `go build ./cmd/<tool>`) and `SEPARATE` (satellite tools built with `cd cmd/<tool> && go build`). Both use the same ldflags for version injection.

Version, commit, and build date are injected at build time via `-ldflags "-X main.version=... -X main.commit=... -X main.date=..."`.

## Test

```bash
go test ./...                              # root module
cd cmd/wasend && go test ./...             # wasend
cd cmd/protonexport && go test ./...       # protonexport
```

Use `t.TempDir()` for file isolation in tests. No test fixtures checked in. Pure functions are extracted and tested independently.

## Layout

```
cmd/mdq/              # mdq CLI entry point (4 subcommands: query, table, extract, list)
cmd/perfgate/         # perfgate CLI entry point (3 subcommands: run, compare, gate)
cmd/shellprof/        # shellprof CLI entry point (3 subcommands: profile, trace, list)
cmd/wasend/           # wasend CLI (separate module — whatsmeow/sqlite deps isolated)
  client.go           # WhatsApp client construction
  message.go          # message resolution and recipient parsing
  telemetry.go        # telemetry (copied from internal/telemetry)
cmd/protonexport/     # protonexport CLI (separate module — Proton deps isolated)
  go-proton-api/      # vendored go-proton-api fork (local replace directive)
  telemetry.go        # telemetry (copied from internal/telemetry)
internal/mdq/         # markdown parsing, querying, output formatting
internal/perfgate/    # benchmark runner, statistics, gating logic
internal/shellprof/   # fish instrumentation, profiling, call graph
internal/telemetry/   # CLI telemetry via emit_jsonl (core tools)
internal/version/     # shared version formatting (core tools)
go.work               # Go workspace: root + cmd/protonexport + cmd/wasend
```

## Patterns

- Cobra subcommands added to rootCmd in each `cmd/` main.go
- Flags bound via pflag (cobra's default)
- Internal packages export types + functions; `cmd/` wires them to CLI
- Core tools use `internal/version.Format()` for version strings
- Satellite tools use inline `fmt.Sprintf` (not worth cross-module coupling)
- All core tools call `telemetry.Instrument`/`Emit` around `rootCmd.Execute()`
- Satellite tools use local `instrument`/`emit` (same pattern, unexported)

## Epic

Full epic and milestone details: `~/.claude/docs/epics/DEVTOOLS_20260301T153830Z_Runabouts_EPIC-001_go_devtools_monorepo.md`
