# ghwatch

Stream GitHub repository activity to the terminal in real-time.

## Quick Start

```bash
# Build
go build -o ghwatch ./cmd/ghwatch

# Run (requires GITHUB_TOKEN)
export GITHUB_TOKEN=ghp_...
./ghwatch --repo owner/repo
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--repo` | *(required)* | Repository in `owner/repo` format |
| `--token` | `$GITHUB_TOKEN` | GitHub API token |
| `--interval` | `30s` | Poll interval |
| `--state-file` | `.ghwatch-state.json` | State file path for resumable polling |
| `--json` | `false` | Output JSONL instead of human-readable text |
| `--events` | `push,pr,workflow` | Comma-separated event types to watch |
| `--debug` | `false` | Enable debug logging to stderr |
| `--since` | `1h` | Lookback window on first run |

## JSONL Output Schema

When `--json` is enabled, each line is a JSON object:

```json
{
  "id": "push-abc123-1708300000",
  "kind": "push",
  "repo": "owner/repo",
  "timestamp": "2026-02-19T12:00:00Z",
  "push": {
    "branch": "main",
    "head_sha": "abc123",
    "size": 2,
    "commits": [
      {
        "sha": "abc123",
        "author": "user",
        "message": "fix: resolve edge case",
        "added": ["new.go"],
        "removed": [],
        "modified": ["main.go"]
      }
    ]
  }
}
```

## Architecture

```
┌─────────────┐
│  ghwatch    │
│  (CLI)      │
└──────┬──────┘
       │
       ▼
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│  PushPoller  │     │  PRPoller    │     │ WorkflowPoller│
└──────┬───────┘     └──────┬───────┘     └──────┬────────┘
       │                    │                    │
       └────────────┬───────┘────────────────────┘
                    ▼
           ┌────────────────┐
           │  Dispatcher    │
           │  (dedup +      │
           │   event chan)  │
           └────────┬───────┘
                    ▼
           ┌────────────────┐
           │  Formatter     │
           │  (JSON / Text) │
           └────────┬───────┘
                    ▼
                  stdout
```

**Polling** → Each poller runs on its own goroutine, calling the GitHub API at the configured interval.

**Dedup** → Events are deduplicated by ID using a TTL-based cache (2h). State is persisted to disk for resumable polling across restarts.

**Formatter** → Events are formatted as either JSONL (machine-readable) or colored text (human-readable) and written to stdout.

## Examples

```bash
# Watch push events only, poll every 10 seconds
./ghwatch --repo org/backend --events push --interval 10s

# JSONL output for piping to jq
./ghwatch --repo org/api --json | jq '.kind'

# Debug mode with custom state file
./ghwatch --repo org/infra --debug --state-file /tmp/ghwatch.json

# Watch PRs and workflows, lookback 24 hours on first run
./ghwatch --repo org/frontend --events pr,workflow --since 24h
```
