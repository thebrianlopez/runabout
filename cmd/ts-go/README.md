# ts-go — Tree-sitter Go Context Extraction

Surgical Go file inspection for AI agents and humans. Parse function signatures, type declarations, and function bodies without reading entire files.

## Install

```bash
cd runabout && make install-ts-go
# → ~/go/bin/ts-go
```

Requires a C compiler (CGo). On macOS: Xcode Command Line Tools.

## Subcommands

### `funcs` — list function and method signatures

```bash
ts-go funcs <file>
ts-go funcs --format compact <file>
```

Outputs all function and method declarations with name, signature, receiver type, and line range.

```json
[
  {
    "name": "scoreYouTubeAsync",
    "kind": "function",
    "signature": "func scoreYouTubeAsync(ctx context.Context, item *QueueItem) (*ScoredItem, error)",
    "start_line": 122,
    "end_line": 280,
    "receiver": null
  },
  {
    "name": "Score",
    "kind": "method",
    "signature": "func (e *Evaluator) Score(ctx context.Context) (float64, error)",
    "start_line": 45,
    "end_line": 92,
    "receiver": "*Evaluator"
  }
]
```

Compact format (lower token cost):

```
scoreYouTubeAsync  func scoreYouTubeAsync(ctx context.Context, ...) (*ScoredItem, error)  L122-L280
Score              func (e *Evaluator) Score(ctx context.Context) (float64, error)         L45-L92   *Evaluator
```

---

### `types` — list type declarations

```bash
ts-go types <file>
ts-go types --format compact <file>
```

Outputs all struct, interface, and alias declarations with name, kind, line range, and field/method count.

```json
[
  { "name": "QueueItem", "kind": "struct", "start_line": 14, "end_line": 28, "field_count": 7 },
  { "name": "Scorer",    "kind": "interface", "start_line": 47, "end_line": 51, "field_count": 2 }
]
```

---

### `extract` — extract a function body by name

```bash
ts-go extract <file> <name>
ts-go extract <file> <name>@<Receiver>   # disambiguate methods
```

Returns the full function or method body including doc comments. Exits 1 if not found.

If the name matches multiple receivers:

```
error: ambiguous: 2 matches — use <name>@<Receiver>
  Score@*Evaluator  (L45-L92)
  Score@Scorer      (L99-L103)
```

---

### `search` — structural code search

```bash
ts-go search '<s-expression-pattern>' '<glob>'
ts-go search --format compact '(function_declaration name: (identifier) @name)' 'cmd/linkari/*.go'
```

Runs a tree-sitter S-expression query across all files matched by the glob. Outputs `SearchResult` per match.

```json
[
  {
    "file": "cmd/linkari/server_score.go",
    "start_line": 122, "end_line": 280,
    "start_byte": 4021, "end_byte": 9113,
    "matched_text": "...",
    "captures": { "name": "scoreYouTubeAsync" }
  }
]
```

Supports the `#eq?` predicate for exact-text filtering:

```bash
ts-go search '(function_declaration name: (identifier) @fn (#eq? @fn "scoreYouTubeAsync"))' '*.go'
```

Query timeout: 5 seconds per file.

---

### `rewrite` — structural rewrite

```bash
ts-go rewrite '<pattern>' '<replacement>' '<glob>'          # patched source → stdout
ts-go rewrite '<pattern>' '<replacement>' '<glob>' --diff   # unified diff → stdout
ts-go rewrite '<pattern>' '<replacement>' '<glob>' --write  # in-place edit (atomic)
```

Finds all matches for the pattern and replaces each with the replacement template. Use `@capture_name` in the replacement to interpolate captured text.

```bash
# Rename a function across all files
ts-go rewrite '(identifier) @name (#eq? @name "oldFunc")' 'newFunc' 'cmd/linkari/*.go' --diff
```

Edits are applied in reverse byte order to avoid offset drift. Overlapping matches are silently skipped. Post-rewrite re-parse warns on syntax errors before `--write` applies.

---

## Global Flags

| Flag | Default | Scope |
|------|---------|-------|
| `--format json\|compact` | `json` | all subcommands |
| `--diff` | off | `rewrite` only |
| `--write` | off | `rewrite` only |

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | User error (file not found, function not found, invalid query) |
| 2 | System error (parse failure) |

## Usage Rule for AI Agents

For Go files >200 lines, use `ts-go` before a full `Read`:

| Goal | Command | Token cost |
|------|---------|-----------|
| Orient (what functions exist?) | `ts-go funcs <file>` | ~50 tokens |
| Read one function | `ts-go extract <file> <name>` | ~300 tokens |
| Understand data model | `ts-go types <file>` | ~30 tokens |
| Find usages across files | `ts-go search '<pattern>' '<glob>'` | varies |
| Structural refactor | `ts-go rewrite '<pattern>' '<template>' '<glob>' --diff` | varies |

Full Read of a 2K-line file ≈ 8,500 tokens. `ts-go funcs` on the same file ≈ 50 tokens.
