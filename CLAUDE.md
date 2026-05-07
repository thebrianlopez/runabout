
# runabout — bluesky-support-linkari

## Repo Context

| Field | Value |
|-------|-------|
| **Repo** | `runabout` |
| **Role** | `` |
| **Workspace** | `bluesky-support-linkari` |
| **Workspace manifest** | `../CLAUDE.md` |

Read `../CLAUDE.md` for workspace-level context, sibling repos, and environment facts.

---

## Role — What This Repo Owns

This repo serves the **** role in the `bluesky-support-linkari` workspace.

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
