# Review: Flip `linkari serve` defaults to tsnet-implicit

**Scope:** Review-only follow-on to EPIC-047. No code changes. Goal: collapse the canonical production invocation to bare `linkari serve` with `~/.config/linkari/server.yaml` supplying everything and Tailscale Funnel on by default.

---

## 1. Confidence: default-flip itself — **82/100**

The conceptual move (tsnet-on-by-default) is sound and matches the stated production topology (Funnel is the only externally-reachable surface; local bind is developer-only). EPIC-047 already routed `tsnet_authkey` through the resolver pipeline (`main.go:285`), so the secret side is already in place. The 18-point haircut is almost entirely about back-compat for the "bare `linkari serve` with nothing configured" case (see §4) and the boolean-default ambiguity at the YAML layer (see §6).

## 2. Confidence: collapsed canonical command boots cleanly — **76/100**

`make && make install && linkari serve` will boot end-to-end *only if* **all six** of the following are true simultaneously on the host:

1. `~/.config/linkari/server.yaml` exists and is readable.
2. `server.yaml` has `secrets: { token: secretsmanager://linkari/bearer-token, tsnet_authkey: secretsmanager://linkari/tsnet-authkey, firebase_sa: secretsmanager://linkari/firebase-sa }` populated.
3. AWS creds resolvable (profile, instance role, or SSO cached) with `secretsmanager:GetSecretValue` on all three secrets.
4. `~/.cache/linkari/` writable for firebase-sa materialization.
5. `~/.config/linkari/tsnet/` writable (or `tsnet_state_dir` set).
6. `notify_min_score`, `log_file`, `debug` in server.yaml (else behavior silently differs from the current canonical command — `notify_min_score=10`, debug logging on, file sink).

Items 1-3 are the load-bearing preconditions. Every one of them has a failure mode that terminates startup. The EPIC-047 M3/M4 pipeline is correct for this — the pessimism in the score is about **operator surface area**, not code correctness: six independent things must be right on a fresh host, and any one missing turns "zero-flag boot" into a diagnostic session. I'd bump this to **88** once the fallback-to-local behavior in §4 is locked in — that single decision removes the worst failure cliff.

## 3. Proposed flag surface

**Preferred opt-out: `--local` CLI flag + `LINKARI_LOCAL=1` env + `tsnet: false` in server.yaml.**

Rationale for `--local` over the alternatives:

| Candidate | Verdict | Why |
|---|---|---|
| `--local` | **pick** | Positive noun, reads naturally (`linkari serve --local`), matches the mental model ("I want this for local dev"). Terse. |
| `--no-tsnet` | reject | Negative flag → double-negative when combined with future `tsnet: false` in yaml ("no-tsnet with tsnet: true?"). Cobra negative-bool flags are awkward. |
| `--local-only` | acceptable-but-verbose | Semantically identical to `--local`, 5 extra chars for no gain. |
| `LINKARI_LOCAL=1` | **pick as companion** | Matches existing `LINKARI_*` env convention; systemd units and tmux panes need env, not flags. |

**Precedence (matches existing secret resolver order exactly):**

```
CLI flag (--local / --tsnet)  >  env (LINKARI_LOCAL / LINKARI_TSNET)  >  server.yaml (tsnet: bool)  >  built-in default (tsnet=true)
```

Keep `--tsnet` as an explicit positive override too — it's already in the flag surface and removing it is a back-compat break. After the flip, `--tsnet` becomes a no-op in production (already on by default) but remains valid as an explicit opt-in override when `server.yaml` has `tsnet: false`.

## 4. Back-compat impact enumeration

| # | Invocation | Today | After flip | Verdict |
|---|---|---|---|---|
| 1 | `linkari serve` (no flags, no yaml, no env) | Binds localhost only | **Attempts tsnet bring-up → fails (no authkey)** | **Unacceptable — needs fallback** |
| 2 | `linkari serve` + fully-populated `server.yaml` | N/A (no yaml support today) | Boots into tsnet Funnel | Intended |
| 3 | `linkari serve --tsnet --tsnet-authkey $KEY --token $T --firebase-sa ...` (canonical today) | Boots tsnet Funnel | Byte-identical (all flags still win) | No change |
| 4 | `linkari serve --token $T` (local dev with token, no tsnet flag) | Binds localhost only | **Tries tsnet bring-up → fails** | Breaks local dev unless user adds `--local` |
| 5 | `linkari serve` with only `LINKARI_TOKEN` set | Binds localhost only | Tries tsnet bring-up, fails | Breaks |
| 6 | `linkari serve` with `server.yaml` having only `token`, no `tsnet_authkey` | N/A | Tries tsnet bring-up, fails | Breaks first-time migration |
| 7 | `linkari serve --local` (new) | N/A | Binds localhost only | New explicit path |

**Recommendation: loader detects "no `tsnet_authkey` resolvable AND no explicit `--tsnet` flag AND no `LINKARI_TSNET=1` env" → fall back to local-only with a WARN log.**

Exact rule:

```
if tsnetEnabled == true (default)
  and tsnetAuthKey == ""  (after resolver pipeline)
  and not explicitly set via flag or env:
    log.Printf("WARN: tsnet default enabled but no tsnet_authkey resolvable; falling back to --local. Set tsnet_authkey in server.yaml or pass --tsnet-authkey to force tsnet.")
    tsnetEnabled = false
```

This preserves invocations #1, #4, #5, #6 — the "bare `linkari serve` with no infra" case still works for a first-time user cloning the repo, just with a loud WARN explaining how to upgrade. Only #6 (partial migration) is the risk path and it gets a clear signpost.

**Alternative considered and rejected:** hard-fail with a pointer to docs. Cleaner in principle, but it turns every `linkari serve` on a fresh dev box into a diagnostic loop, and "works-then-tells-you-how-to-go-further" beats "fails-then-tells-you-how-to-start" at the margin where operators live.

## 5. `server.yaml` schema additions

The minimum set for zero-flag boot of the canonical command:

```yaml
server:
  # Existing (EPIC-047 M3)
  token: secretsmanager://linkari/bearer-token
  firebase_sa: secretsmanager://linkari/firebase-sa
  tsnet_authkey: secretsmanager://linkari/tsnet-authkey
  server_url: ...           # non-canonical, but already in v1

  # NEW for default-flip
  tsnet: true               # bool, default true; see §6 for ambiguity
  tsnet_hostname: linkari   # string, default "linkari"
  tsnet_state_dir: ""       # string, default ~/.config/linkari/tsnet
  log_file: ../linkari-server.log    # string, default "" (stderr only)
  debug: true               # bool, default false
  notify_min_score: 10      # int, default 0
```

**Yes, this is the complete set** to reproduce the canonical command's runtime behavior from `server.yaml` alone. I audited `main.go:260-340` and `cmd.Flags()` block at `main.go:533-540` — every non-secret flag referenced by the canonical command maps to one of the fields above. `tls_enabled`, `cert_file`, `key_file` are not canonical and stay CLI/env only.

**One detail worth pinning now:** `log_file` should be resolved *before* the `logWriter` / `log.SetOutput` hook at `main.go:319-334`, so the EPIC-047 M4 provenance lines actually land in the file sink instead of stderr when the operator has `log_file` configured via yaml. This is a one-line reorder, but easy to miss.

## 6. Resolution order for non-secret fields (bool/int/string)

**Matches the existing secret resolver pattern: CLI > env > yaml > default.**

**Boolean-`tsnet` ambiguity — this is the subtle one.** Default is `true`, so the usual "zero-value = unset" Go trick doesn't work — the zero value (`false`) is semantically meaningful ("explicitly local"), not "unset". Three options:

| Option | Pros | Cons |
|---|---|---|
| **`*bool` pointer** | Idiomatic for "tri-state" (nil/true/false); nil = unset → use default | Pointer dereferencing noise throughout the loader |
| **`TSNetEnabled bool` + `tsnetExplicit bool`** sentinel | Clean bool on the struct | Two-field dance for every tri-state field; error-prone |
| **`Tsnet string` with domain `"auto"`/`"on"`/`"off"`** | Explicit in yaml, self-documenting | String-typed bool is a code smell and breaks the "bool is a bool" intuition |

**Recommendation: `*bool` pointer.** Yaml v3 handles `*bool` correctly (nil when field absent, pointer to false when `tsnet: false`, pointer to true when `tsnet: true`). The resolver becomes:

```go
// in resolveTsnet or inline:
if cliFlagSet                  { use flag }
else if envSet                 { use env }
else if cfg.Tsnet != nil       { use *cfg.Tsnet }
else                           { use true (new default) }
```

This is 4-5 lines and reads cleanly. Do NOT reuse `resolveServerField` from EPIC-047 for bool — that helper is string-typed and trying to jam bool through it will produce "true"/"false" string parsing bugs. Introduce `resolveBoolField(ctx, flag, flagSet, env, yaml *bool, def bool) (bool, tier, src)` alongside it.

Same pattern for `debug` (default false → plain `bool` works, no pointer needed). Same for `notify_min_score` (default 0 is semantically "no floor", and 0 is also the zero value — ambiguous, but operationally harmless because "no floor" is already the behavior-without-the-flag). `log_file`, `tsnet_hostname`, `tsnet_state_dir` are strings where `""` unambiguously means "unset/use default".

**Pin in the implementing epic:** only `tsnet` needs `*bool`. Everything else uses the zero-value-as-unset convention.

## 7. Test plan

### Unit

- **`resolveBoolField` table test.** Matrix: `(cli_set, cli_val, env, yaml, default) → (expected_val, expected_tier)`. At minimum 12 rows covering each tier winning + default fallback + empty-env sentinel.
- **`server.yaml` parser.** Fixture with all new fields populated; fixture with only `tsnet: false`; fixture with `tsnet` absent (pointer nil).
- **Fallback-to-local rule** (the §4 recommendation). Pure-function test: given `(tsnetEnabled=true, tsnetAuthKey="", tsnetExplicit=false)` → assert `(tsnetEnabled=false, warning_logged=true)`. Four other rows: authkey present → no fallback; explicit flag → no fallback; explicit env → no fallback; yaml `tsnet: false` → no fallback (different code path, already local).

### Integration

- **`bare-serve-boots-from-yaml`.** t.TempDir + synthetic `server.yaml` with literal values (no SM calls); assert process reaches "listening" log line within 2s; assert provenance log lines present for token, tsnet_authkey, firebase_sa, log_file; assert the firebase-sa cache file materialized. Mock or stub the tsnet bring-up (do not hit a real Tailscale control plane in CI).
- **`bare-serve-no-yaml-no-env-falls-back-to-local`.** Empty `$HOME`, no env vars, no flags. Assert: WARN log line emitted, local listener bound, no tsnet bring-up attempted, exit 0 on SIGTERM.
- **`canonical-command-byte-identical-behavior`.** The existing canonical invocation (`linkari serve --tsnet --tsnet-authkey $KEY --token $TOK ...`) continues to work with no `server.yaml` present and produces the same runtime state modulo the N additive provenance log lines already shipped in EPIC-047 M4.

### Regression

- **Golden log test for the new WARN message.** Pin the exact string so it shows up in log search queries — operators will grep this.

## 8. Blast radius on `main.go`

### `main.go:275-302` (tsnetEnabled resolution block)

**Replace the block with an ordered resolver that produces `(tsnetEnabled bool, tsnetExplicit bool)` up front, then gates the authkey fallback on the explicit bit.** Sketch:

```go
tsnetEnabled, tsnetExplicit = resolveTsnet(cmd, serverCfg)  // flag > env > yaml > default(true)
tsnetHostname              = resolveStringField("tsnet_hostname", tsnetHostname, os.Getenv("LINKARI_TSNET_HOSTNAME"), serverCfg.TSNetHostname, "linkari")
tsnetAuthKey, _           := resolveField("tsnet_authkey", tsnetAuthKey, os.Getenv("TS_AUTHKEY"), "", func(s *ServerConfig) string { return s.TSNetAuthKey })

// §4 fallback rule
if tsnetEnabled && tsnetAuthKey == "" && !tsnetExplicit {
    log.Printf("WARN: tsnet default enabled but no tsnet_authkey resolvable; falling back to local-only. ...")
    tsnetEnabled = false
}

if tsnetEnabled {
    // existing state-dir + MkdirAll block
}
```

### Flag registration (`main.go:533-540`)

| Flag | Action |
|---|---|
| `--tsnet` (bool, default false) | **Change default to `true`**, docstring updated to "enable Tailscale Funnel (default: true, pass --local to disable)" |
| `--tsnet-hostname` | Unchanged |
| `--tsnet-state-dir` | Unchanged |
| `--tsnet-authkey` | Unchanged |
| `--local` (new, bool, default false) | Added; mutually exclusive with `--tsnet` — cobra has `MarkFlagsMutuallyExclusive` for this; use it |

**Do not deprecate `--tsnet`** — it's still a valid explicit override. Just flip the default and rewrite the docstring.

### Other touch points in `main.go`

- `main.go:458` (`if tsnetEnabled { ... tsnet bring-up ... }`) — unchanged logic, just consumes the new resolved bool.
- Help text block `main.go:129-138` — needs rewording: "Tailscale Funnel is enabled by default. Pass `--local` or set `LINKARI_LOCAL=1` to bind only the local listener."
- `logWriter`/`log.SetOutput` ordering — see §5 note about resolving `log_file` *before* the sink is installed.

## 9. Documentation impact

| File | Change |
|---|---|
| `runabout/README.md` "Dual listener" example | Swap the canonical command to bare `linkari serve`; add a "Local dev" subsection with `linkari serve --local` |
| `runabout/CLAUDE.md` | Add a line under `cmd/linkari/` noting tsnet-default-on + the `--local` opt-out |
| `docs/epics/PERSONAL_20260408T143220Z_Linkari_EPIC-047_secrets_xdg_standardization.md` | Cross-reference the new epic/milestone under "Linked Issues" |
| Epic proposed below | New artifact (see §10) |
| Operator runbook (if any) | Update to reference `server.yaml` instead of the long flag list |

No other documentation surfaces reference the serve command invocation directly (grep confirms: README + CLAUDE.md + epics only).

## 10. Structure recommendation: new epic or follow-on milestone?

**Recommendation: new standalone epic, EPIC-048: `linkari serve tsnet-default flip`.**

Reasoning:

- EPIC-047 is already `status: Complete` in the frontmatter (line 11). Reopening a complete epic to bolt on M7 muddies the retrospective and breaks the "accepted → implemented → done" arc.
- The default-flip is a **behavior change** visible to every operator, whereas EPIC-047 was explicitly a **back-compat preserving** refactor ("canonical command is byte-identical" was a stated guarantee at line 272). Mixing them confuses the risk story.
- The new work has its own distinct risk profile (fallback rule in §4, bool tri-state in §6, flag deprecation/addition, documentation rewrites) that deserves its own review + locked-decisions block.
- M-count for the new work is ~4 milestones, which is epic-sized, not milestone-sized.

**Proposed EPIC-048 milestone structure:**

| M | Description | Owner |
|---|---|---|
| M1 | `*bool` schema addition + `resolveBoolField` helper + parser tests for new `server.yaml` fields (`tsnet`, `debug`, `notify_min_score`, `log_file`, `tsnet_hostname`, `tsnet_state_dir`) | runabout-agent |
| M2 | `main.go` flag default flip + `--local` flag + mutual exclusion + help text rewrite | runabout-agent |
| M3 | Fallback-to-local rule (§4) + WARN log golden test + integration tests for bare-serve boot cases | runabout-agent |
| M4 | `log_file` resolver ordering fix (resolve before `log.SetOutput`) + provenance lines land in yaml-configured sink | runabout-agent |
| M5 | Docs: README "Dual listener" rewrite, CLAUDE.md update, EPIC-047 cross-ref | runabout-agent |

Preconditions: EPIC-047 M3/M4 complete (already are). No infra work needed — all secrets already exist from EPIC-047.

Confidence after these decisions are locked: **M1-M5 each ≥92** as written. The two items holding it below 95 would be (a) empirical verification that the fallback-rule WARN message shows up in the operator's log pipeline as intended, and (b) one manual smoke test of `make && make install && linkari serve` on a fresh laptop account with a real `server.yaml`.

---

## Summary table

| Question | Answer |
|---|---|
| Default-flip confidence | 82/100 |
| Canonical bare-serve boot confidence | 76/100 → 88/100 with fallback rule |
| Opt-out flag | `--local` + `LINKARI_LOCAL=1` env + `tsnet: false` yaml |
| Bare `linkari serve` with no infra | Fall back to local with WARN — do NOT hard-fail |
| server.yaml schema additions | `tsnet *bool`, `tsnet_hostname`, `tsnet_state_dir`, `log_file`, `debug`, `notify_min_score` |
| `tsnet` tri-state encoding | `*bool` pointer (nil = use default-true) |
| Non-secret resolver order | CLI > env > yaml > default (matches existing) |
| Structure | **New epic EPIC-048**, not an EPIC-047 M7 |

**No source files modified. No milestones claimed. No epic status touched.** Deleting the trigger next.
