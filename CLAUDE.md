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