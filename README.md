# runabout

Go devtools monorepo — three CLI tools for shell optimization workflows.

- **mdq** — query fields and tables across markdown files
- **perfgate** — statistical before/after performance gating
- **shellprof** — fish shell function profiler with call graphs

## Install

```bash
make install   # go install → ~/go/bin
```

Requires Go 1.24+.

## mdq

Query structured data out of markdown files matching a glob.

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

## Build

```bash
make build    # builds bin/mdq bin/perfgate bin/shellprof
make install  # go install → ~/go/bin
make clean    # removes bin/
go test ./... # run all tests
```

Version, commit, and build date are injected at build time via ldflags.

## Layout

```
cmd/mdq/           # mdq entry point
cmd/perfgate/      # perfgate entry point
cmd/shellprof/     # shellprof entry point
internal/mdq/      # markdown parser, query engine, output formatting
internal/perfgate/ # benchmark runner, statistics, gating logic
internal/shellprof/# fish instrumentation, profiling, call graph
internal/version/  # shared version info
```
