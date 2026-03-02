# Runabout

Go devtools monorepo — three CLI tools for shell optimization workflows.

## Module

- Module: `github.com/blo-grindr/runabout`
- Go: 1.24
- Dependencies: `github.com/spf13/cobra`

## Build

```bash
make build      # builds all three binaries to bin/
make install    # copies to /opt/homebrew/bin/
make clean      # removes bin/
```

## Test

```bash
go test ./...
```

Use `t.TempDir()` for file isolation in tests. No test fixtures checked in.

## Layout

```
cmd/mdq/          # mdq CLI entry point (4 subcommands: query, table, extract, list)
cmd/perfgate/     # perfgate CLI entry point (3 subcommands: run, compare, gate)
cmd/shellprof/    # shellprof CLI entry point (3 subcommands: profile, trace, list)
internal/mdq/     # markdown parsing, querying, output formatting
internal/perfgate/ # benchmark runner, statistics, gating logic
internal/shellprof/ # fish instrumentation, profiling, call graph
internal/version/ # shared version info (injected via ldflags)
```

## Patterns

- Cobra subcommands added to rootCmd in each cmd/ main.go
- Flags bound via pflag (cobra's default)
- Internal packages export types + functions; cmd/ wires them to CLI
- Version injected at build time via Makefile ldflags
- All functions return `fmt.Errorf("not yet implemented")` until M2+

## Epic

Full epic and milestone details: `~/.claude/docs/epics/DEVTOOLS_20260301T153830Z_Runabouts_EPIC-001_go_devtools_monorepo.md`
