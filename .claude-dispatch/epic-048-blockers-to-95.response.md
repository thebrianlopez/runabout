# EPIC-048 Blockers-to-95 Analysis

**Epic:** PERSONAL_20260408T155615Z_Linkari_EPIC-048_tsnet_default_flip.md
**Reviewer:** runabout-agent
**Date:** 2026-04-08
**Assumption:** Locked Decisions 1–9 from the Pre-Implementation Review hold as-is. Not relitigated.

---

## M1 — Schema + resolve*Field helpers + parser tests

**Current confidence (with locks held): 88/100**

Schema additions are mechanical. `resolveBoolField` is novel but small. `*bool` yaml round-trip is the only non-trivial piece.

### Blockers to reach ≥95

| # | Blocker | Recommended resolution | Rationale |
|---|---------|------------------------|-----------|
| 1 | `resolveBoolField` signature — locked decision 6 says `(ctx, flag, flagSet bool, env string, yaml *bool, def bool)` but ctx is unused (no secret resolution for bools). Keeping it makes the helper lie about its contract. | Drop `ctx` and `*secrets.Resolver` entirely from the bool helper. Signature: `resolveBoolField(flag, flagSet bool, env string, yaml *bool, def bool) (value bool, tier, src string)`. Document that bool/int/string-plain helpers are secret-free siblings of `resolveServerField`. | Keeps the secret-aware helper (`resolveServerField`) distinct from the plain-tier helpers. Prevents future maintainers from threading a resolver into a path that can never produce a secret URI. |
| 2 | `LINKARI_LOCAL` env-value parsing rules are unspecified. `LINKARI_TSNET=1` is already accepted at main.go:277. Do we accept `0`/`false`/`no`/empty-string-means-unset? | Pin env parsing to **exactly** `"1"` or `"true"` → set, anything else (including empty) → unset. Matches the existing `LINKARI_TSNET` handling byte-for-byte. Add a table-test row per accepted/rejected value. | Symmetry with the existing env parsing removes a surprise-vector during M2. Avoids a bikeshed in review. |
| 3 | `*bool` yaml round-trip behavior for the three states `tsnet: true` / `tsnet: false` / `tsnet:` (absent) is untested against `gopkg.in/yaml.v3`. yaml.v3 decodes a missing key as nil-pointer; a present-but-empty key (`tsnet:`) decodes as nil-pointer too — this is the correct behavior but needs a pinned fixture. | Add three parser-test fixtures in `config_test.go`: `tsnet_true.yaml`, `tsnet_false.yaml`, `tsnet_absent.yaml`. Assert `cfg.Server.Tsnet == nil` for absent, `*cfg.Server.Tsnet == false` for false, `*cfg.Server.Tsnet == true` for true. | Pinned fixtures prevent a silent yaml.v3 upgrade from flipping semantics. Three lines of test per case. |
| 4 | M1 deliverable names three helpers (`resolveBoolField`, `resolveIntField`, `resolveStringField`) but only `resolveBoolField` is mentioned in Locked Decisions. Is the `*Int`/`*String` helper required in M1 or deferred? | **Require** all three in M1 as siblings. `resolveIntField` is needed for `notify_min_score` (M2 wires it). `resolveStringField` is needed for `log_file`, `tsnet_hostname`, `tsnet_state_dir` (M2/M4 wire them). Deferring splits the helper contract across milestones and makes M2's diff messy. | Keeps the "helper pack" atomic. One PR introduces the full tier/source plumbing for plain values. |
| 5 | Table test row count — "12+ rows" is a floor, not a contract. What are the mandatory rows? | Enumerate mandatory rows in the test: `{flag-true-wins, flag-false-wins, flagSet-overrides-env, flagSet-overrides-yaml, env-1-wins, env-true-wins, env-invalid-ignored, yaml-true-wins, yaml-false-wins, yaml-nil-falls-through, default-true, default-false}` = 12 rows. Add one row per accepted env sentinel. | Makes "12+ rows" a review-checkable list, not a vibe. |

### Final confidence after blockers resolved: **96/100**

Remaining 4 pts: yaml.v3 behavior on future upgrades (pinned fixtures mitigate, but not eliminate).

---

## M2 — `--tsnet` default flip + `--local` + mutual exclusion + `LINKARI_LOCAL`

**Current confidence (with locks held): 80/100**

The flip itself is one-line. The risk is in the wiring between the new helpers and the existing tsnet bring-up block at `main.go:275-302`, plus cobra's mutual exclusion semantics around a default-true bool.

### Blockers to reach ≥95

| # | Blocker | Recommended resolution | Rationale |
|---|---------|------------------------|-----------|
| 1 | cobra `MarkFlagsMutuallyExclusive("tsnet", "local")` fires **only when both flags are changed** (cobra uses `Flag.Changed` internally). With `--tsnet` now default-true, a bare `linkari serve --local` passes mutual-exclusion (tsnet not explicitly set) — correct. But `linkari serve --tsnet --local` must error. Need a test. | Add a unit test exercising cobra's RunE with `--tsnet --local` and asserting non-zero exit + error message. Pin the error format. | Mutual exclusion is a cobra-behavior gotcha. Without a test this regresses silently on cobra upgrades. |
| 2 | Where does `--local` get consumed? The current main.go:275-302 block gates on `tsnetEnabled` only — `--local` has to produce `tsnetEnabled=false` **before** that block runs. | Move the `tsnetEnabled` resolution **above** the env-fallback block (replace lines 275-278 wholesale). Produce `(tsnetEnabled, tsnetExplicit)` from `resolveBoolField` with flag-tier checking `localFlag || tsnetFlag`, env-tier checking `LINKARI_LOCAL || LINKARI_TSNET`, yaml-tier reading `cfg.Server.Tsnet`, default true. The block from 279-302 stays unchanged. | Keeps the diff localized. The fallback rule (M3) then post-processes `tsnetEnabled` using `tsnetExplicit`. |
| 3 | `--local` and `LINKARI_LOCAL` are *negative* signals; `resolveBoolField` takes a single `flag bool` but now has two competing flag inputs. | Resolve `local` and `tsnet` via two separate `resolveBoolField` calls, then reconcile: `tsnetEnabled = tsnetResolved && !localResolved`. `tsnetExplicit = localFlagSet || tsnetFlagSet || localEnvSet || tsnetEnvSet || cfg.Server.Tsnet != nil`. Document this two-step in a comment. | Avoids bloating `resolveBoolField` with a "negative flag" parameter. Two calls + one boolean expression is clearer than a polymorphic helper. |
| 4 | Help text for `--tsnet` currently reads "enable tsnet"; flipping default to true without rewriting the help string misleads operators ("why does it say enable if it's already on?"). | Rewrite help text: `--tsnet` → `"enable Tailscale Funnel (default: true; use --local to disable)"`, `--local` → `"force local-only listener, disables tsnet"`. Pin in a small snapshot test against `cmd.Flags().Lookup("tsnet").Usage`. | Operator-facing surface. Cheap to pin, expensive to discover drift. |
| 5 | `notify_min_score` wiring (M2's `resolveIntField` consumer) is unspecified — does M2 do it or does M3? | M2 wires `notify_min_score`, `log_file` string field (value only, sink ordering in M4), `tsnet_hostname`, `tsnet_state_dir`, `debug` using the new helpers. M2's scope is "all non-bool tunables now flow through the helper pack." | Without this, M2 only half-solves the zero-flag-boot goal. |

### Final confidence after blockers resolved: **95/100**

Remaining 5 pts: cobra mutual-exclusion upgrade risk; `tsnetExplicit` expression has five inputs and is the classic shape where an off-by-one regression hides.

---

## M3 — Fallback rule + integration tests + golden test

**Current confidence (with locks held): 78/100**

This is the highest-risk milestone. Integration tests that exercise `main.go`'s RunE without actually binding tsnet are non-trivial, and the golden WARN test is tightly coupled to log-package format.

### Blockers to reach ≥95

| # | Blocker | Recommended resolution | Rationale |
|---|---------|------------------------|-----------|
| 1 | The fallback rule runs **after** `tsnetAuthKey` resolution (main.go:285-289) but **before** `tsnet` state-dir creation (main.go:296-300). The exact insertion point must be pinned. | Insert the fallback check immediately after line 289 (`if err != nil { return err }`). Extract to a pure function `applyTsnetFallback(tsnetEnabled, tsnetExplicit bool, tsnetAuthKey string, logger *log.Logger) bool` that returns the adjusted `tsnetEnabled` and emits the WARN. Unit-test the pure function with 5 rows. | Pure-function extraction makes the 5-row unit test trivial and decouples it from integration test scaffolding. |
| 2 | Golden WARN test coupling — `log.Printf` prefixes with `log.Ldate|Ltime` once `debug=true` runs `log.SetFlags` at line 346. Golden test needs deterministic output. | Write the WARN through a dedicated logger that does not inherit `log.SetFlags` — use `log.New(logWriter, "", 0).Printf(...)` inside `applyTsnetFallback`. Golden string is the bare message without timestamp prefix. | Decouples the golden assertion from debug-flag side effects. Without this, the golden test is flaky under `--debug`. |
| 3 | Integration test `bare-serve-boots-from-yaml` requires mocking tsnet bring-up. The current code calls `tsnetServe` (or equivalent) unconditionally when `tsnetEnabled=true`. | Add an internal seam: `type tsnetStarter func(hostname, authkey, stateDir string) (net.Listener, error)` with the production impl as default and a test-override var `var tsnetStart tsnetStarter = realTsnetStart`. Integration tests swap it for a no-op that returns a `net.Listen("tcp", "127.0.0.1:0")`. | Without this seam the integration test either (a) requires a real authkey, defeating "no SM calls" or (b) panics. One package-level var is the minimal surface. |
| 4 | `canonical-command-byte-identical` test semantics — "byte-identical" against what? stdout? log lines? exit code? | Define exactly three assertions: (1) exit code 0 within 2s SIGTERM, (2) the set of provenance log lines emitted matches the EPIC-047 canonical set exactly (modulo timestamp), (3) listener bound on configured port. Drop any notion of "byte-identical stdout." | "Byte-identical" is impossible for a server that logs timestamps. This tightens the acceptance to something checkable. |
| 5 | `bare-serve-no-yaml-falls-back-to-local` needs a clean HOME. `t.Setenv("HOME", t.TempDir())` may not be enough if xdgpath caches. | Audit `xdgpath` for init-time caching. If present, reset via a test-only hook. If not, `t.Setenv("HOME", ...)` is sufficient. Document the finding in the test file header. | Silent HOME caching would make the test pass locally and fail in CI on a second run. Cheap audit, high payoff. |
| 6 | firebase-sa cache materialization assertion — is `firebase-sa.json` required for bare-serve-boots? The test would need a real SM stub. | State explicitly: the bare-serve integration test uses a `file://` URI in the fixture `server.yaml` pointing at a `t.TempDir()` stub JSON. No SM calls. Drop "cache materialized" from the assertion list — it's an EPIC-047 concern, already covered. | Avoids re-testing EPIC-047 M3/M4 surface in M3. |

### Final confidence after blockers resolved: **95/100**

Remaining 5 pts: `tsnetStarter` seam is a new abstraction introduced under test pressure — exactly the kind of thing that grows awkwardly. Live laptop smoke is the only true confidence closer.

---

## M4 — `log_file` sink ordering fix

**Current confidence (with locks held): 90/100**

Smallest milestone. The fix is "move the log-file block above line 341 (`log.SetOutput`)." Risk is ordering against `flushProvenance`.

### Blockers to reach ≥95

| # | Blocker | Recommended resolution | Rationale |
|---|---------|------------------------|-----------|
| 1 | Current code at main.go:344 calls `flushProvenance(provenance)` *after* `log.SetOutput(logWriter)`. If M4 moves `log_file` resolution above `log.SetOutput`, the existing `flushProvenance` call stays correct — but this invariant must be documented in a comment so a future refactor doesn't re-break it. | Add a sentinel comment block: `// EPIC-048 M4 INVARIANT: log_file must be resolved into logWriter before log.SetOutput so flushProvenance lines land in the configured sink.` | Zero-cost insurance against re-regression. |
| 2 | `log_file` resolution now has three sources (flag non-existent → env `LINKARI_LOG_FILE` → yaml `log_file`). But there is no CLI flag for log_file today. Add one? Or yaml+env only? | **Do not add a CLI flag** for `log_file`. Yaml + env only. Use `resolveStringField` with `flag=""`, `flagSet=false`. This matches the "config file is primary; env is break-glass" intent of the epic. | Smaller surface. Operators who need to override per-invocation can still use `LINKARI_LOG_FILE=... linkari serve`. |
| 3 | The log_file path currently uses `filepath.Dir(logFilePath)` for MkdirAll at line 325. With yaml-sourced paths, the path may be relative (`./linkari.log`). Is relative-to-cwd the right semantic? | Document: relative paths resolve against cwd at invocation time (matches current env-var semantics). Add one integration test row covering `log_file: ../linkari-server.log` in yaml. | The existing behavior with `LINKARI_LOG_FILE=../linkari-server.log` is already this — preserve it. |

### Final confidence after blockers resolved: **97/100**

---

## M5 — Docs

**Current confidence (with locks held): 92/100**

Doc updates are mechanical but the scope boundary matters.

### Blockers to reach ≥95

| # | Blocker | Recommended resolution | Rationale |
|---|---------|------------------------|-----------|
| 1 | README "Dual listener" example rewrite — what replaces it? Still show both invocations, or collapse to one? | Keep both invocations in README: the zero-flag `linkari serve` (primary) and the canonical explicit-flag form (break-glass / CI / first-time setup). Show the `--local` form in a separate "Local dev" subsection. Drop the six-flag form from the primary example. | Zero-flag is the lede; explicit flags are still documented but demoted. Matches the goal without erasing the canonical path. |
| 2 | CLAUDE.md tsnet-default-on note scope — one sentence or a full subsection? | One sentence under the existing server architecture note: `"linkari serve defaults to tsnet Funnel; pass --local for local-only."` No new subsection. | CLAUDE.md is architect-abstraction; implementation details belong in README. |
| 3 | EPIC-047 cross-reference — which section? | Add to EPIC-047's "Linked Issues" section: `- Successor: EPIC-048 — tsnet default flip + zero-flag canonical invocation`. Do NOT reopen EPIC-047's Complete status. | Epic is Complete; cross-reference is metadata only. |

### Final confidence after blockers resolved: **97/100**

---

## Cross-Milestone Section

### (F) Ordering / Sequencing DAG

```
M1 (helpers + schema)
 │
 ├──→ M2 (flag flip, --local, LINKARI_LOCAL) ──┐
 │                                              │
 ├──→ M4 (log_file sink ordering)               │
 │     ^                                        │
 │     └── independent of M2/M3                 │
 │                                              │
 └──→ M3 (fallback rule + integration tests) ←──┘
       │
       └── requires M1 (helpers), M2 (wiring), M4 (log sink invariant for golden test stability)

M5 (docs) — requires M1-M4 feature-complete for accurate examples
```

**M4 is independent of M3's logic** but M3's **golden WARN test** is stability-coupled to M4 (see M3 blocker 2 — the WARN logger bypass). Recommended order: M1 → M2 → M4 → M3 → M5. Putting M4 before M3 means the golden test is written against the final sink behavior, not a mid-flight state.

### (G) Shared Blockers Across Milestones

| Shared blocker | Milestones affected | Resolution |
|----------------|---------------------|------------|
| `resolveBoolField` / `resolveIntField` / `resolveStringField` helper pack signatures | M1 (produce), M2 (consume for all fields), M4 (consume for log_file) | Lock all three signatures in M1 before any M2/M4 work starts. M1 blocker 1 + 4. |
| yaml.v3 `*bool` fixture library | M1 (parser tests), M3 (integration test fixtures) | M1 produces the three fixtures (`tsnet_true`, `tsnet_false`, `tsnet_absent`) in `testdata/`; M3 reuses them. |
| `tsnetExplicit` computation expression | M2 (produces), M3 (consumes for fallback rule) | Pin the five-input expression in M2 via an extracted helper `wasTsnetExplicitlySet(flags, env, yaml) bool`. M3 then trivially calls it. |
| `applyTsnetFallback` pure function | M3 | Extract at M3 blocker 1. Enables the 5-row unit test AND the golden WARN test without integration scaffolding. |

### (H) Runtime-Smoke-Only Risks (cannot be resolved pre-implementation)

1. **Real tsnet Funnel bring-up on a clean laptop** — the test seam `tsnetStarter` bypasses real tsnet entirely. Only a manual `make && make install && linkari serve` run against a populated `server.yaml` with a real Android share request will close this. (Already called out in epic.)
2. **First-boot tsnet state directory creation** under `~/.config/linkari/tsnet/` — integration tests use `t.TempDir()`. Real permission semantics on a laptop with restrictive umask are smoke-only.
3. **Operator upgrade path** — an operator with the old `LINKARI_TOKEN=... linkari serve --tsnet --tsnet-authkey ...` pattern runs it post-upgrade. Byte-identical behavior is asserted in `canonical-command-byte-identical` test but the *operator's muscle memory* is not. One-line release-notes mention required (not blocking).
4. **`log_file` path permission errors on yaml-sourced paths** — the MkdirAll failure mode is the same as today, but operators may not have set up the parent directory if they're copying from an example. Smoke-only. Mitigation: README example uses an absolute path under `~/.config/linkari/`.

### (I) Implementation Surface Sanity Check

| Surface | Estimate |
|---------|----------|
| Files touched | 7 (`config.go`, `main.go`, new `server_resolve_plain.go` or extension of `server_resolve.go`, `config_test.go`, `server_resolve_test.go`, new `integration_test.go`, `README.md`, `CLAUDE.md`) — 7 code + 2 docs |
| LOC delta (code, excl. tests) | +180 / −35 net +145 |
| LOC delta (tests) | +400 (12 table rows × ~15 LOC + 3 integration tests × ~80 LOC + 3 parser fixtures + golden file) |
| New test count | +16 unit, +3 integration, +1 golden = **20 new tests** |
| New fixture files | 3 yaml + 1 golden WARN txt = **4 testdata files** |
| New package-level vars | 1 (`tsnetStarter` test seam) |
| New exported symbols | 0 (all helpers are package-private) |

**Sanity check:** 5-milestone split is appropriate. M1 is the largest (helper pack + schema + fixtures ≈ 35% of total LOC). M3 is the second-largest (integration scaffolding ≈ 30%). M2/M4/M5 are each ≈ 10–15%. No milestone is trivially small or dangerously large. **Split is sound.**

### (J) Draft → Accepted Recommendation

**Recommend: flip EPIC-048 from Draft → Accepted WITH ONE EPIC EDIT.**

The single required edit is to **Locked Decision 6** (`resolveBoolField` signature). The current text threads `ctx` through a secret-free helper:

> `resolveBoolField(ctx, flag, flagSet bool, env string, yaml *bool, def bool) (bool, tier, src)`

Should be revised to:

> `resolveBoolField(flag, flagSet bool, env string, yaml *bool, def bool) (value bool, tier string, src string)`
>
> Sibling helpers `resolveIntField` and `resolveStringField` follow the same secret-free shape. These three helpers are a deliberate pack, distinct from `resolveServerField` which is secret-aware via `secrets.Resolver`.

All other 8 locked decisions stand unchanged.

Every other blocker in this analysis is an **implementation-time** concern that should live in milestone notes / PR descriptions, not in the epic itself. The epic is otherwise ready for Accepted status.

### Confidence Summary

| Milestone | Current (locks held) | After blockers | Delta |
|-----------|----------------------|----------------|-------|
| M1 | 88 | 96 | +8 |
| M2 | 80 | 95 | +15 |
| M3 | 78 | 95 | +17 |
| M4 | 90 | 97 | +7 |
| M5 | 92 | 97 | +5 |
| **Epic overall** | **82** | **95** | **+13** |

The epic-overall 95 figure matches the bare-serve-boot target stated in the initial review (88/100 with fallback rule → 95/100 with blockers resolved pre-implementation + manual smoke post-implementation).

---

**End of analysis. No source code modified. No milestones claimed. No epics modified.**
