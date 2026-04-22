# Runabout

Go devtools monorepo — ten CLI tools for shell optimization and personal workflows.

## Module

- Root module: `github.com/blo-grindr/runabout`
- Go: 1.25+ (go.work: 1.26.1, linkari module: 1.26.1)
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
| ts-go | `cmd/ts-go/go.mod` | go-tree-sitter, tree-sitter-go (CGo) |
| wasend | `cmd/wasend/go.mod` | whatsmeow, sqlite3, protobuf |
| protonexport | `cmd/protonexport/go.mod` | go-proton-api (vendored), gluon, crypto |

## Build

```bash
make build      # builds all 6 binaries to bin/
make install    # go install → ~/go/bin
make clean      # removes bin/
make core       # builds only mdq, perfgate, shellprof, hookval
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
cmd/hookval/          # hookval CLI entry point (3 subcommands: validate, gen-docs, lint-schema)
cmd/ts-go/            # ts-go CLI (separate module — tree-sitter CGo deps isolated)
  parser.go           # shared tree-sitter parser init + defer Close() lifecycle
  queries.go          # embedded tree-sitter query patterns
  funcs.go            # funcs subcommand — function/method signature extraction
  types.go            # types subcommand — type declaration extraction
  extract.go          # extract subcommand — function body extraction
  telemetry.go        # telemetry (copied from internal/telemetry)
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
internal/hookval/     # schema parsing, signal validation, doc generation
internal/telemetry/   # CLI telemetry via emit_jsonl (core tools)
internal/version/     # shared version formatting (core tools)
go.work               # Go workspace: root + cmd/ts-go + cmd/protonexport + cmd/wasend
```

## Patterns

- **Caller-wins share action invariant (EPIC-052):** `resolveShareAction` in `handler.go` is the single resolver for `(action, profile)` on every ingress path that writes a `queue` row. When a share request arrives with a non-empty `action` that is present in the router's cfgIndex, that action is preserved verbatim — server-side heuristics are forbidden from rewriting it unless `share.heuristic_override_enabled: true` is set in `server.yaml` (default false). Bare `uinit` (no profile suffix) and unknown `uinit_<profile>` inputs are deterministically pinned via `pinDefaultUinitAction` (lexicographic sort, not map iteration — the EPIC-050 PoMo "Footgun 3"). Every resolution emits a `share_action_resolved` event via `emitShareActionResolved` *before* the DB write, so failed inserts still produce provenance. Regression coverage: `share_resolution_test.go` — `TestShareActionRoundTrip` exercises 7 profiles × 5 URLs + the bare-`uinit` case unconditionally (no build tag).
- **Workspace Key Invariant (EPIC-054):** Workspace directories created by `uinit` are keyed on `(url, profile)`, not URL alone. Sharing the same URL under a different profile (e.g. `life` then `finance`) MUST produce two distinct workspace dirs — the profile suffix is part of the slug. Violating this invariant causes `uinit --auto-resume` to silently resume into an unrelated workspace, leaving the corresponding `queue` row stranded in `relayed` status forever. The **`RelayedWatchdog`** in `watchdog.go` is the observability safety net: it ticks every `relayed_watchdog_interval` (default 60s), marks any `relayed` row older than `relayed_watchdog_max_age` (default 15m) as `failed` with `error_reason='scoring_timeout'`, and emits one `share_scoring_timeout` event per swept row to `linkari_events.jsonl`. Idempotent by construction — the `WHERE status='relayed'` filter guarantees no row is re-swept. Config is hot-reloadable via SIGHUP. Regression coverage: `watchdog_test.go` — four tests covering the in-window, past-window mark, past-window emit, and no-duplicate-emit cases. The `(url, profile)` slug derivation itself lives in `~/.config/fish/functions/uinit.fish` (EPIC-054 M2, fish-config-agent).
- **tmux exec logging invariant:** `cmd/linkari/tmux.go` uses `logTmuxExec(cmd)` for every `exec.Command("tmux", …)` invocation. Never call `slog.Debug("tmux exec", "args", cmd.Args)` directly — slog renders `[]string` with `%v`, space-joining elements without quoting, so argv boundaries vanish and embedded spaces look like token separators (the 2026-04-09 `linkari-server.log` investigation chased this as a suspected quoting bug). `logTmuxExec` emits both the structured `argv` field and a POSIX-quoted `repro` field that is copy-pasteable into a terminal. Shell quoting uses POSIX single-quotes, not `strconv.Quote` — Go escaping is unsafe for shell paste because `$`, backtick, and `\` re-interpret inside double-quoted strings. Regression coverage: `cmd/linkari/tmux_log_test.go` (unit + `sh -c` round-trip) and `tmux_integration_test.go` (real tmux, `go test -tags=integration`).
- **Jira Ingress Invariant (EPIC-057):** No Jira-controlled byte may reach `tmux send-keys -l` except via `jiraKeyRegex`-validated `req.Text`. The `ginit_*` command template uses only `{{.Text}}` — never `{{.Title}}` or `{{.URL}}`. `checkScopedAuth` in `server.go` enforces that requests bearing `jira_token` can only invoke `ginit_*` action IDs, and mobile `LINKARI_TOKEN` requests cannot invoke `ginit_*`. The `AutoScore bool` field on `ActionConfig` causes ginit rows to be enqueued as `status=scored, verdict="workspace_bootstrapped"` via `Queue.EnqueueScored`, bypassing the RelayedWatchdog entirely. Regression coverage: `jira_ingress_test.go` (regex 10 cases + scoped-auth 4 cases + enqueue seam), `share_resolution_test.go` (7 ginit_* CallerWins cases), `tmux_log_test.go` (hostile-summary round-trip).
- **Dual-writer invariant (EPIC-051):** `Queue.EnqueueDigestIfDue(ctx, profile, score, slug, verdict, url)` is the only sanctioned entry point that may write a `kind='digest'` row to `push_outbox`. All three scoring writers — `handleQueueScore`, `handleNotify`, and `cmd_score.go` — go through it. The helper owns the min-score floor (`notify_min_score`), per-profile throttle (`server.yaml push.digest_throttle`), and the SQL-level cross-process race guard. Never add a fourth path; add a new call site to the helper instead. Regression coverage: `integration_push_test.go` (run with `go test -tags=integration ./cmd/linkari/...`).
- **Classification cascade architecture (EPIC-079):** `classifyShareRequestFast` (stages 1-5, no LLM) runs synchronously pre-enqueue in `handleShare`. `classifyShareRequest` (stages 1-6, includes LLM) runs async in `scoreAsync`. File shares no longer default to "eng" — they fall through to stage-6 LLM classify using synthesized metadata. The pre-enqueue sync cascade emits `classify_stage_win` events with `phase: "pre_enqueue"`. `appCategoryProfileMap` maps CATEGORY_IMAGE (3) to "life". `ForceContentClassify` on `ActionConfig` (EPIC-085 M2) is threaded through `ShareRequest.ForceContentClassify` and forces `contentClassify = true` in `scoreAsync` regardless of cascade result.
- **Vision scoring path (EPIC-079 M3):** `HaikuVisionEvaluator` in `evaluator.go` calls `runClaudeHaikuVision` which invokes `claude --print --allowedTools Read --output-format json --json-schema <schema>` to read and score local image files. `scoreAsync` selects this evaluator when `req.Type == "image"` and `req.AudioPath` points to a readable file. Temp file cleanup is owned by `scoreAsync` via `defer os.Remove(req.AudioPath)` for non-audio file shares. `SetTopicTags` is no longer gated on `isURLShare` — all share types can have topic tags. Vision token back-calculation (EPIC-085 M3): when `InputTokens < 100 && CostUSD > 0.01`, `scoreAsync` back-calculates `ImageTokensEstimated` from the cost delta using Haiku 4.5 pricing ($1.00/MTok input, $5.00/MTok output) and writes the corrected value to `sc.Usage.ImageTokensEstimated` before persisting.
- `linkari serve` defaults to tsnet Funnel on; pass `--local` for local-only. Falls back to local with WARN when no `tsnet_authkey` is resolvable (EPIC-048).
- `linkari config init` scaffolds `~/.config/linkari/server.yaml`; `linkari doctor` validates secrets without booting; `serve --detach` is the portable POSIX fork-detach primitive with PID file at `~/.local/state/linkari/linkari.pid` (EPIC-049).
- Cobra subcommands added to rootCmd in each `cmd/` main.go
- Flags bound via pflag (cobra's default)
- Internal packages export types + functions; `cmd/` wires them to CLI
- Core tools use `internal/version.Format()` for version strings
- Satellite tools use inline `fmt.Sprintf` (not worth cross-module coupling)
- All core tools call `telemetry.Instrument`/`Emit` around `rootCmd.Execute()`
- Satellite tools use local `instrument`/`emit` (same pattern, unexported)

## Observation Emission

When completing work in this repo, emit Type 3 observation dispatches to `~/code/personal/linkari-workspace/.claude-dispatch/` for cross-cutting concerns the workspace coordinator should know about. Write observations when you notice issues during normal work — not as a dedicated investigation step.

**Categories to watch for:**
- `versioning` — Go version skew across modules, missing git tags, inconsistent toolchain directives
- `dependency` — divergent dependency versions across satellite modules, stale transitive deps
- `drift` — CLAUDE.md claims that don't match code reality, stale invariant documentation
- `hygiene` — large uncommitted changesets, dead code accumulation, test coverage gaps

**Emission target:** Always write to `~/code/personal/linkari-workspace/.claude-dispatch/` (the workspace agent's CWD), not this repo's `.claude-dispatch/`.

**File naming:** `obs_runabout_<short_id>.json`

**Schema:** See Type 3 in `~/.claude/rules/dispatch-system.md`.

## ts-go: Tree-sitter Go Context Extraction

For Go files >200 lines, use `ts-go funcs` before a full Read to orient on function signatures, then `ts-go extract` to read only the specific function body you need.

```bash
# Orientation: list all functions/methods with signatures and line ranges
ts-go funcs <file>                      # JSON output (default)
ts-go funcs --format compact <file>     # Compact tabular output (lower token cost)

# Targeted read: extract a single function body + doc comments
ts-go extract <file> <function_name>    # JSON output with body, line range, doc comment

# Type overview: list type declarations with field counts
ts-go types <file>                      # JSON output
ts-go types --format compact <file>     # Compact tabular output
```

**Why tree-sitter over gopls:** Tree-sitter parses files in <5ms without a Go workspace or build context. gopls requires a running LSP server, module graph resolution, and type checking — unnecessary overhead for structural extraction. Phase 2 will integrate gopls for cross-file references.

**Resource lifecycle:** `parseFile()` returns `(tree, src, parser, error)`. Callers must `defer tree.Close()` and `defer parser.Close()` — the tree-sitter C runtime allocates memory that Go's GC doesn't track.

## Tool Selection

Use built-in tools first, bash commands last:

| Tool | Use for | NOT |
|------|---------|-----|
| Glob | File pattern matching | `fd`, `find`, `ls` |
| Grep | Content search | `rg`, `grep` |
| Read | File reading | `cat`, `head`, `tail` |
| Edit | File editing | `sed`, `awk` |
| Write | File creation | `echo`, heredoc |

Only use bash for: `go` commands, `git` operations, `make`, fish functions (`epic-claim-milestone`, `dispatch-complete`, etc.).

**Go file analysis (>200 lines):** Use `ts-go` before a full Read to minimize context burn:
1. `ts-go funcs <file>` — orient on signatures and line ranges first
2. `ts-go extract <file> <name>` — read only the function you need
3. Fall back to `Read` with `offset`/`limit` only if `ts-go` output is insufficient

## Model Selection

Default model: **Sonnet**. Use Haiku for simple file lookups. Escalate to Opus only for multi-constraint architectural tradeoffs across 3+ competing concerns.

## Dispatch Processing

**On every session start and first prompt**, check for pending dispatch triggers using `Glob(pattern: ".claude-dispatch/*.md")`.

Canonical entrypoint prompt: `dispatch` — all natural-language variants ("pickup on our dispatches", "lets pickup dispatches", etc.) mean exactly this.

### Execution Rules

1. Process ALL assigned triggers in a single pass — do not stop between triggers
2. Execute all assigned milestones **autonomously without confirmation** — do not ask "proceed?", "continue?", or wait for "yes"
3. Only surface blockers via AskUserQuestion if a milestone's prerequisites are genuinely missing
4. Update epic milestone status after each milestone completion
5. Update README/docs if milestone deliverables changed public interfaces
6. Call `dispatch-complete` for each completed trigger

### Per-Trigger Steps

1. Read the trigger with the Read tool
2. Read the full epic: `md-tree extract <epic_path> "Milestones"`
3. Identify your assigned milestones from the trigger's `milestones` field
4. Claim your first milestone: `epic-claim-milestone <epic-file> M1 "In Progress"`
5. Execute the milestone deliverables completely
6. Mark the milestone complete: `epic-claim-milestone <epic-file> M1 "Complete (YYYY-MM-DD)"`
7. Move to the next assigned milestone
8. When all your milestones are complete: `dispatch-complete .claude-dispatch/<file>.md`
9. If all agents' milestones are complete, update epic `status: Complete`

**Never execute dispatches assigned to a different agent** (check `agent` field in frontmatter).
**Always read the epic file fresh before each milestone — another agent may have updated it.**
