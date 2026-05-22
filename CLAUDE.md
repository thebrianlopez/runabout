
# runabout — project-runway

## Repo Context

| Field | Value |
|-------|-------|
| **Repo** | `runabout` |
| **Role** | `` |
| **Workspace** | `project-runway` |
| **Workspace manifest** | `../CLAUDE.md` |

Read `../CLAUDE.md` for workspace-level context, sibling repos, and environment facts.

---

## Role — What This Repo Owns

This repo serves the **** role in the `project-runway` workspace.

Scope your changes to this role. If work requires changes in a sibling repo, coordinate via the workspace manifest — do not cross-commit.

---

## Commit Protocol

1. Changes in this repo must be independently valid — do not cross-commit across repos
2. Commit messages should reference the workspace name when relevant

---

## Model Selection

| Task | Model |
|------|-------|
| File reads, grep, exploration | haiku |
| Code changes, config analysis, reviews | **sonnet** (default) |
| Multi-repo architectural trade-offs | opus |

Circuit-breaker: same tool call fails 3x → stop, surface the blocker.

---

## chain-eval (cmd/chain-eval/)

The eval harness for the `/chain` prompt quality gate. Key facts for agents:

### Local development

```bash
cd runabout
go build ./cmd/chain-eval/      # build
go test ./cmd/chain-eval/...    # unit tests (17 tests, no API keys needed)
go run ./cmd/chain-eval/ --dry-run --all   # validate fixtures, no API calls
go run ./cmd/chain-eval/ --all --min-score 0.85  # live eval (needs API keys)
```

### docs-core — the prompt under test

`command_chain.md` lives in `~/code/personal/docs/prompts/` (private, gitignored from this repo via `/docs` in `.gitignore`). Locally, `docs/` is a symlink to that directory. In CI, it is cloned from S3.

**Default `CHAIN_PROMPTS_DIR`:** `docs/core/prompts` (local) — works when the `docs/` symlink is live.
**CI `CHAIN_PROMPTS_DIR`:** `docs-core/prompts` — set in `chain-eval.yml` after S3 clone.

To publish local docs-core changes to S3 so CI picks them up:
```bash
make push-docs-artifact   # requires: pipx install git-remote-s3, AWS_PROFILE=brianonpoint
```

### CI gate (GitHub Actions)

Workflow: `.github/workflows/chain-eval.yml`
- Triggers on push to `cmd/chain-eval/**` or `docs/core/prompts/command_chain.md`
- **Step order (eval job):** OIDC creds → `pip install git-remote-s3` → `git clone s3://www-brianlopez-us/repos/docs-core docs-core` → build → eval
- OIDC role: `github-actions-runabout` (SecretsManager read + S3 read on docs-core prefix)
- Live eval skips gracefully when `ANTHROPIC_API_KEY` secret is absent

To trigger manually:
```bash
gh workflow run chain-eval.yml --ref main
gh run list --limit 3
```

### Key env vars

| Var | Default | Notes |
|-----|---------|-------|
| `ANTHROPIC_API_KEY` | required | Claude (model under test) |
| `HUGGINGFACE_API_KEY` | optional | LLM judge + bucket push |
| `HF_RESULTS_BUCKET` | optional | `namespace/bucket` for result storage via `hf` CLI |
| `HF_DATASET_REPO` | optional | Fallback dataset commit push |
| `HF_JUDGE_MODEL` | `meta-llama/Llama-3.1-8B-Instruct` | Must be ≥7B |
| `CHAIN_PROMPTS_DIR` | `docs/core/prompts` | Dir containing `command_chain.md` |
| `CHAIN_FIXTURES_DIR` | `cmd/chain-eval/fixtures` | Dir of fixture subdirs |

All API keys are seeded in AWS Secrets Manager: `runabout/chain-eval` (us-east-1).
Load them with: `chain-eval --secret runabout/chain-eval`

### Result inspection

```bash
# List runs stored in HF Bucket
hf buckets list thebrianlopez/chain-eval-runs -R

# Inspect latest run (replace {runID})
hf buckets cp hf://buckets/thebrianlopez/chain-eval-runs/{runID}/results.jsonl - \
  | python3 -c "import sys,json; rows=[json.loads(l) for l in sys.stdin if l.strip()]; print(len(rows),'rows')"

# Verify HF auth
hf auth whoami
```

Full operational details: `docs/runbooks/PERSONAL_20260522T180000Z_chain_eval_CI_Docs_Core_S3_Runbook.md`
User guide: `docs/guides/PERSONAL_chain_eval_F10_HuggingFace_User_Guide.md`
