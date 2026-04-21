# ts-go Measurement Protocol

## Baseline Snapshot (Pre-ts-go, Apr 18-21 2026)

Sessions in linkari workspace, all Go-touching work:

| Day | Sessions | Avg Total Tokens | Avg Turns | Avg Cache Hit% |
|-----|----------|-----------------|-----------|----------------|
| 2026-04-18 | 94 | 3,540,545 | 54 | 90.0% |
| 2026-04-19 | 76 | 3,634,847 | 47 | 89.3% |
| 2026-04-20 | 36 | 4,541,182 | 56 | 88.9% |
| 2026-04-21 | 15 | 4,920,907 | 63 | 91.1% |

**Aggregate baseline:** ~3.8M tokens/session, ~53 turns/session, ~89.8% cache hit rate.

## Post-Rollout jq Filter

Compute tokens-per-session for Go-touching sessions after ts-go adoption:

```bash
# Run against daily JSONL files
jq -c 'select(.event_type == "session_summary" and (.cwd | test("linkari")))' \
  ~/.automation-metrics/events/YYYY-MM-DD.jsonl | \
  jq -s '{
    sessions: length,
    avg_tokens: ([.[].metadata.total_tokens] | add / length | round),
    avg_turns: ([.[].metadata.turns] | add / length | round),
    avg_cache_hit: ([.[].metadata.cache_hit_pct] | add / length * 10 | round / 10),
    ts_go_usage: [.[].metadata.tool_distribution["ts-go"] // 0] | add
  }'
```

## Re-Measurement Date

**2026-05-05** (2 weeks post-M5 completion).

Compare against baseline to measure:
- Token reduction per orientation read (target: >80%)
- Turn count reduction for find-and-modify workflows (target: 3-5 → 2)
- ts-go CLI invocation count via telemetry events

## C Compiler Requirement

ts-go requires CGo (C compiler) for the tree-sitter runtime. On macOS, ensure Xcode Command Line Tools are installed:

```bash
xcode-select --install
```

The tree-sitter C core is compiled from vendored sources via `go-tree-sitter` and `tree-sitter-go` modules. No external tree-sitter installation is needed.
