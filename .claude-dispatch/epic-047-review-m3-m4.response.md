# EPIC-047 M3/M4 Pre-Implementation Review

**Reviewer:** runabout-agent
**Date:** 2026-04-08
**Scope:** Review-only. No source modifications. M1/M2 already landed under `cmd/linkari/internal/{xdgpath,secrets}`.

## 1. Confidence Scores

| Milestone | Confidence | Rationale |
|-----------|-----------:|-----------|
| **M3** — `ServerConfig` loader + resolution order | **85** | Loader shape is well-scoped. Existing `ServerConfig` struct already exists in `config.go` embedded in `Config.Server`, so the work is largely (a) add a *separate* `server.yaml` loader path, (b) thread `Resolver.Resolve` per field, (c) enforce flag > env > yaml-literal > yaml-sm > default ordering in `serveCmd.RunE`. The 15pt deduction covers two real ambiguities (see §2). |
| **M4** — firebase-sa materialization + tsnet-authkey SM + provenance log | **78** | Mechanics are clear but three subtleties drag confidence: (a) materialization must happen *before* the existing `os.ReadFile(firebaseSA)` at main.go:121, which means the resolver runs earlier in `RunE` than where those fields are consumed today; (b) provenance log must go to `log.Printf` *after* `log.SetOutput(logWriter)` at main.go:235, otherwise lines land on stderr instead of the ring/logfile; (c) fingerprint is currently 8 hex chars in `secrets.Fingerprint` but the epic says "first-4-hex" — spec/code mismatch flagged below. |

## 2. Spec Concerns / Ambiguities

1. **Fingerprint width mismatch.** Epic text says "first-4-hex SHA-256 fingerprint" but `secrets.Fingerprint()` returns 8 hex chars (`hex.EncodeToString(sum[:])[:8]`). 4 hex = 16 bits = ~65k space which collides regularly across the secret namespace; 8 hex (32 bits) is the right call and is what M2 already shipped. **Proposal:** update the epic wording to "first-8-hex" to match code, rather than weaken the fingerprint.
2. **`server.yaml` vs existing `actions.yaml` `server:` block.** `Config.Server` already exists inside `actions.yaml` (see `cmd_shared.go` / `config.go:50`). The epic introduces a *new* `~/.config/linkari/server.yaml`. Two possible interpretations:
   - (a) Separate file, strictly tunables+secrets, loaded independently of `actions.yaml`.
   - (b) Same `ServerConfig` struct, but hoisted to a standalone file while preserving the `actions.yaml` embedded block as deprecated fallback.
   The epic's resolution-order line (`server.yaml literal > server.yaml secretsmanager://`) implies (a). **I recommend (a) with an explicit migration note:** if `server.yaml` exists, it wins; otherwise fall back to `actions.yaml`'s `server:` block with a deprecation log line. Back-compat preserved, no operator forced to migrate on day 1.
3. **Resolution order precedence for SM errors.** If `server.yaml` has `token: secretsmanager://linkari/bearer-token` and the SM call fails (e.g., no AWS creds in local dev), should the loader: (i) fail hard, (ii) fall back to the literal URI, or (iii) fall back to default/env? **Proposal:** hard-fail at startup with a clear error. Silent fallback to env would mask rotation failures. Document this as "SM URI is a commitment; break-glass = unset the URI and re-export LINKARI_TOKEN".
4. **`file://` URI scope.** `secrets.Resolve` supports `file://` but the epic's resolution-order line doesn't mention it. Is `file://` an intentional hidden feature, or should it be removed from the public surface for v1? **Proposal:** keep it, document it as the escape hatch for pre-materialized secrets (useful for CI).
5. **Empty string handling.** `Resolve("")` today returns `("", Source{literal}, nil)`. In the ServerConfig path, an empty field should mean "skip, fall through to the next tier" not "return empty literal as a valid value". The loader must check `len(raw) == 0` *before* calling `Resolve`, otherwise every empty yaml field consumes a resolver call and produces a provenance log line for `<literal>` with fingerprint `e3b0c442` (SHA-256 of empty string).
6. **Provenance log ordering vs log sink.** The ring/log-file sink is wired at main.go:235. Provenance lines must be emitted *after* that to land in the operator log pipeline. The natural implementation is: construct `Resolver` early, defer provenance emission until after `log.SetOutput`, then loop over collected `(field, source, fp)` tuples.
7. **tsnet-authkey already has env precedence at main.go:182.** The new SM resolution must slot in *between* the flag check (main.go:181 `if tsnetAuthKey == ""`) and the current `os.Getenv("TS_AUTHKEY")` fallback. Order becomes: flag → env → `server.yaml` literal → `server.yaml` SM URI → empty.

## 3. Proposed `ServerConfig` Shape Changes

Minimal. The current struct is fine; add:

```go
type ServerConfig struct {
    Port           int    `yaml:"port"`
    Token          string `yaml:"token"`             // may be secretsmanager:// or literal
    TSNetAuthKey   string `yaml:"tsnet_authkey"`     // NEW — M4; may be SM URI
    QueueDB        string `yaml:"queue_db"`
    FirebaseSA     string `yaml:"firebase_sa"`       // may be SM URI; resolver materializes to cache path
    LogFile        string `yaml:"log_file"`
    Shell          string `yaml:"shell"`
    ShellArgs      string `yaml:"shell_args"`
    NotifyMinScore int    `yaml:"notify_min_score"`
    ServerURL      string `yaml:"server_url"`        // may be SM URI per epic requirement
}
```

Plus a new *top-level* wrapper type for `server.yaml` (since that file shouldn't embed `Actions`):

```go
// ServerFile is the on-disk shape of ~/.config/linkari/server.yaml.
type ServerFile struct {
    Server ServerConfig `yaml:"server"`
}
```

Rationale: keeps `Config` (actions.yaml) shape unchanged for back-compat, while `server.yaml` gets its own loader `LoadServerFile(path string) (*ServerConfig, error)`.

**Resolution-order helper** — recommend extracting into a small pure function so it's table-testable:

```go
// resolveServerField picks the highest-precedence non-empty value.
// Returns the chosen value, the tier it came from (for logging), and an
// error (only from SM resolution).
func resolveServerField(
    ctx context.Context, r *secrets.Resolver,
    flag, env, yamlVal, def string,
) (value string, tier string, src secrets.Source, err error) { ... }
```

Tiers: `"flag"`, `"env"`, `"yaml-literal"`, `"yaml-sm"`, `"default"`. Provenance log emits `tier` alongside `src` and fingerprint.

## 4. Test Plan

### M3 — Resolution Order Table Test

File: `cmd/linkari/config_server_test.go` (new)

Table-driven, rows cover every tier winning:

| # | Flag | Env | yaml literal | yaml SM URI | Default | Expected | Expected Tier |
|---|------|-----|-------------|------------|---------|----------|--------------|
| 1 | `f`  | `e` | `y`  | `sm://x`   | `d`     | `f`  | `flag` |
| 2 | ``   | `e` | `y`  | `sm://x`   | `d`     | `e`  | `env` |
| 3 | ``   | ``  | `y`  | `sm://x`   | `d`     | `y`  | `yaml-literal` |
| 4 | ``   | ``  | ``   | `sm://x`   | `d`     | `V`  | `yaml-sm` |
| 5 | ``   | ``  | ``   | ``         | `d`     | `d`  | `default` |
| 6 | ``   | ``  | ``   | ``         | ``      | ``   | `default` (empty) |
| 7 | ``   | ``  | ``   | `sm://missing` | `d` | err  | `yaml-sm` (hard fail) |
| 8 | ``   | ``  | ``   | `sm://x#k` | `d`     | `V.k`| `yaml-sm` (json key) |

Each row uses `newTestResolver(&fakeSM{data: ...})` with appropriate payloads.

Additional tests:
- `LoadServerFile_BackCompat` — absent `server.yaml` → returns empty config without error (so serve still works with zero server.yaml present).
- `LoadServerFile_MalformedYAML` — returns wrapped parse error.
- `LoadServerFile_EmptyFieldsSkipResolver` — assert `fakeSM.calls == 0` when all fields blank (see concern §2.5).

### M4 — Materialization + Provenance Golden Test

File: `cmd/linkari/config_server_materialize_test.go` (new)

1. **Firebase SA materialization.**
   - Seed `fakeSM` with `linkari/firebase-sa` → valid JSON blob.
   - Point `XDG_CACHE_HOME` at `t.TempDir()`.
   - Call the materialization function; assert:
     - `~/.cache/linkari/firebase-sa.json` exists
     - File mode is `0600` (`Stat().Mode().Perm() == 0o600`)
     - Contents byte-equal the SM payload
     - Field value is rewritten to the cache file path (downstream `os.ReadFile` works unchanged)
     - Running twice re-materializes (not check-then-skip) — assert mtime advances on second call
2. **Provenance log golden.**
   - Capture `log.Output` via a `bytes.Buffer`.
   - Resolve three fields: literal, file://, secretsmanager://.
   - Assert log contains exactly three lines matching regex:
     ```
     ^linkari: secret [a-z_]+ resolved from <source> fp=[0-9a-f]{8}$
     ```
   - Assert *no* line contains the actual secret value (scan buffer for known seeded strings — must be absent).
3. **tsnet-authkey resolution.**
   - Env var wins over yaml SM URI (table row).
   - yaml SM URI wins when env empty (table row).
   - Missing secret → startup error, not silent empty string.

## 5. Blast Radius — Existing Files

### `cmd/linkari/config.go` (211 lines today)

**Additions only, no deletions.** Functions touched:

| Function | Change | Back-compat risk |
|----------|--------|------------------|
| `ServerConfig` struct | Add `TSNetAuthKey string \`yaml:"tsnet_authkey"\`` field | None — zero value preserves current behavior |
| *(new)* `ServerFile` type | Add new wrapper struct for `server.yaml` | None — new surface |
| *(new)* `LoadServerFile(path string)` | Add new function | None — new surface |
| *(new)* `resolveServerField(...)` | Add helper | None — new surface |
| `LoadConfig` | **Unchanged** | ✅ `actions.yaml` loader untouched |
| `defaultConfigPath` | **Unchanged** | ✅ |
| `validate` / `ActiveActions` / `RenderCommand` / `ExtractMatch` / `ToAction` / `builtinConfig` | **Unchanged** | ✅ |

### `cmd/linkari/main.go` (427 lines today, `serveCmd` at line 53)

**Modifications inside `RunE` only.** Line-by-line back-compat check for the canonical command:

```
linkari serve --token $LINKARI_TOKEN --tsnet --tsnet-authkey $TS_AUTHKEY \
  --firebase-sa ~/.config/linkari/firebase-sa.json --notify-min-score 10 --debug
```

| main.go line | Current behavior | New behavior | Canonical command impact |
|-------------:|------------------|--------------|--------------------------|
| 103–108 | Token: flag → env LINKARI_TOKEN → error | Token: flag → env → yaml-literal → yaml-sm → error | ✅ flag set → yaml never consulted, identical path |
| 115–117 | FirebaseSA: flag → env LINKARI_FIREBASE_SA | FirebaseSA: flag → env → yaml-literal → yaml-sm (with materialization) | ✅ flag set → materialization skipped, file path used directly |
| 120–132 | `os.ReadFile(firebaseSA)` + credentials parse | **Unchanged** — receives the now-resolved path (either flag value or materialized cache path) | ✅ identical code path |
| 181–183 | tsnetAuthKey: flag → env TS_AUTHKEY | tsnetAuthKey: flag → env → yaml-literal → yaml-sm | ✅ flag set → yaml never consulted |
| 235 | `log.SetOutput(logWriter)` | **Unchanged** | ✅ |
| (new) ~236 | — | Emit provenance log lines for all resolved server fields | ✅ additive only; canonical command emits three lines (token=env, tsnet-authkey=env, firebase-sa=flag) |
| 267–287 | `cfg.Server.*` fallback from actions.yaml | **Unchanged** — lowest priority after server.yaml introduced | ✅ preserved |

**Back-compat verdict:** canonical invocation is byte-identical in behavior. The only observable difference is N new log lines at startup (the provenance log). Zero runtime behavior change for operators who have not migrated to `server.yaml`.

**One gotcha:** the provenance log must be emitted *after* `log.SetOutput(logWriter)` at line 235, which means the resolver must be constructed and fields resolved earlier, then provenance held in a slice and flushed after the sink is wired. Implementation sketch:

```go
// early in RunE, right after flag defaults
resolver := secrets.New(awsFactory)
serverFile, _ := LoadServerFile("")    // tolerant of missing file
resolutions := []resolution{}           // collect (field, tier, src, fp)
token, _ = resolveField(ctx, resolver, "token", token, os.Getenv("LINKARI_TOKEN"), serverFile.Server.Token, "", &resolutions)
// ...
// later, after log.SetOutput(logWriter):
for _, r := range resolutions {
    log.Printf("linkari: secret %s resolved from %s fp=%s", r.field, r.src, r.fp)
}
```

## 6. Open Questions for Implementation Session

1. Confirm fingerprint width decision (8 hex, update epic wording). [recommend: yes]
2. Confirm SM URI failure policy (hard-fail vs fallback). [recommend: hard-fail]
3. Confirm `actions.yaml` `server:` block survives as deprecated fallback vs immediate removal. [recommend: keep with deprecation log line]
4. Confirm `file://` URI stays on the public surface. [recommend: yes, document as escape hatch]
5. Need the AWS SDK SM factory file (`sm_aws.go`) — M2 stubbed `New(smFactory)` but production factory is not yet present. Implied M3/M4 dependency or should be split into its own micro-milestone?

## 7. Comfort with Satellite-Module Decision

**Confirmed comfortable.** Placing `xdgpath` and `secrets` under `cmd/linkari/internal/` is consistent with the `wasend`/`protonexport`/`fetchpage` pattern already documented in `runabout/CLAUDE.md` ("Why satellites copy telemetry"). The AWS SDK stays scoped to the linkari binary, and the CLAUDE.md satellite rationale applies unchanged. No retro relocation risk given `aws-sdk-go-v2/{config,credentials,sts,sso}` is already transitively present via `tailscale.com`.

---

**Ready to implement M3/M4 once open questions §6.1–§6.5 are resolved.** Recommend tackling in order: (a) finalize fingerprint spec wording, (b) add `sm_aws.go` production factory, (c) implement `LoadServerFile` + `resolveServerField` + table tests (M3), (d) implement firebase-sa materialization + tsnet-authkey resolution + provenance log + golden tests (M4).
