# runabout  -  Linkari Backend

## Repo Context

| Field | Value |
|-------|-------|
| **Repo** | `runabout` |
| **Role** | Backend scoring engine + API server |
| **Workspace** | `linkari` |
| **Workspace manifest** | `../CLAUDE.md` ← read for cross-repo topology and full file index |

Read `../CLAUDE.md` for: workspace topology, full `cmd/linkari/` file index, API surface table, intent routing layer, and common workflows.

---

## Role  -  What This Repo Owns

- `linkari serve`: HTTP API server consumed by Android, Chrome extension, and Bluesky firehose
- Scoring pipeline: `scoreAsync` in `server_score.go`  -  classifies and scores shares via `claude` CLI
- Queue + persistence: `queue.go`  -  SQLite-backed dedup, archive, digest
- Intent-conditioned routing: `handler.go` + `intent*.go`  -  resolves share → intent → profile → action
- Config: `config.go`  -  `ServerConfig` (63 fields), `ActionConfig` per domain/action

**Scope your changes to this repo.** Cross-repo changes (e.g., Android API contract) coordinate via workspace manifest  -  do not cross-commit.

---

## Navigation

**Critical file index and ts-go quick-commands are in `../.pi/SYSTEM.md` and `../CLAUDE.md`.**

```bash
# Always orient before editing large files
ts-go funcs --format compact cmd/linkari/<file>.go
ts-go types --format compact cmd/linkari/<file>.go
ts-go extract cmd/linkari/<file>.go <FunctionName>
```

---

## Build & Test

```bash
# Full test suite
go test ./cmd/linkari/... -shuffle=on

# Full test suite with explicit seed (use 3 distinct seeds when validating order dependence)
go test ./cmd/linkari/... -shuffle=1

# Global-state test convention: any test that mutates package globals (for example
# pkgDomainRouter, jinaBaseURL/jinaHTTPClient, profilePathOverride, registered
# scoring backends, or event-log dirs) must save the previous value and restore it
# with t.Cleanup before returning.

# Async-test convention (EPIC-250): no test may return while a goroutine it launched
# (directly, or indirectly via handleFirehosePost/scoreAsync/etc.) is still touching a
# package global (transcriptDir, a *Queue it is about to Close(), etc.). Synchronizing
# on Evaluate() invocation alone (onceDoneEval's done channel) is not sufficient -
# scoreAsync does real work (transcript persistence, queue status writes) after
# Evaluate returns, in the same goroutine. Tests that need that work to have finished
# should set scoreAsyncDoneHook (save/restore the previous value with t.Cleanup) and
# block on it, instead of a fixed sleep. See
# POMO_firehose-transcript-goroutine-leak-suite-order for the original manifestation
# and scoreAsyncDoneHook's doc comment in cmd/linkari/server_score.go for exactly where
# in scoreAsync it fires (after persistence, before the push/FCM tail that can block on
# real on-disk config in dev environments lacking AWS credentials).
#
# Fixture-path convention (POMO devnull-device-unlink-root-test-flake): never use
# device nodes (/dev/null, /dev/zero) as placeholder paths in fixture fields whose
# lifecycle contract is delete-on-cleanup (e.g. ShareRequest.AudioPath - scoreAsync
# owns and removes it). As root, os.Remove on a device node succeeds and deletes it
# host-wide, breaking every subsequent exec.Command with nil stdio. Use a real file
# under t.TempDir() instead. scoreAsync's cleanup is also Lstat-guarded to regular
# files as defense in depth - do not remove that guard.

# Opt-in goroutine-leak detection: `go test -tags leakcheck ./cmd/linkari/...` runs the
# suite under go.uber.org/goleak (cmd/linkari/main_leakcheck_test.go). Not enabled in CI
# - a first pass surfaced ~37 pre-existing leaks unrelated to any specific bug (DB
# connectionOpener, idle HTTP conns, AWS SDK credential resolution). Use it to audit
# incrementally, not as a merge gate yet.

# Build check
go build ./cmd/linkari/...

# Run server
go run ./cmd/linkari serve

# Install ts-go and other tools
make install

# Format all Go files (gofumpt enforced in CI)
make fmt

# Check formatting without writing (mirrors CI gate)
make fmt-check
```

---

## Model Selection

| Task | Model |
|------|-------|
| File reads, grep, exploration | haiku |
| Code changes, config analysis, reviews | **sonnet** (default) |
| Multi-repo architectural trade-offs | opus |

Circuit-breaker: same tool call fails 3× → stop, surface the blocker.

---

## Commit Protocol

1. Run `go test ./cmd/linkari/... -shuffle=on` before committing; verify order-sensitive changes with 3 distinct seeds
2. Commit messages reference epic IDs when applicable (e.g., `EPIC-161 F2:`)
3. Changes must be independently valid  -  do not cross-commit across repos
</content>
</invoke>