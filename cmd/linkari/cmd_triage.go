package main

// EPIC-043 M2: `linkari triage` — Go port of _uinit_profile_prompt.fish.
//
// This subcommand owns the link-triage scoring loop. It loads a profile
// prompt template, assembles the system+user prompt the same way fish does,
// shells out to the `claude` CLI for a single-turn Haiku call, parses the
// returned markdown into a TriageResult, and persists the score via the
// same Queue.ScoreByURL path that the /notify HTTP handler uses.
//
// Why shell out to `claude` instead of using the Anthropic SDK directly:
// fish currently calls `claude --print --model claude-haiku-4-5-20251001
// --max-turns 1 --tools "" --system-prompt ...` so output bytes flow through
// Claude Code, not the raw API. Matching transport keeps M2 byte-for-byte
// equivalent to what M1's 42-fixture corpus captured, and avoids splitting
// billing between Claude Code subscription and API spend.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// TriageResult is the parsed output of one Haiku triage call.
type TriageResult struct {
	Score       int      `json:"score"`
	Verdict     string   `json:"verdict"`
	ActionItems []string `json:"action_items,omitempty"`
	Tags        string   `json:"tags,omitempty"`
	RawMarkdown string   `json:"raw_markdown"`
}

// claudeModel is the Haiku model fish currently calls. Pinned for parity with
// the M1 fixture corpus until M3+ revisits the model selection.
const claudeModel = "claude-haiku-4-5-20251001"

// contentTruncationRunes matches the fish 2000-char truncation in
// _uinit_profile_prompt.fish line 39 (`string sub -l 2000`). Fish counts
// runes, not bytes — match that semantic.
const contentTruncationRunes = 2000

// execHaiku is the indirection point that tests stub. Production path is
// runClaudeHaiku; tests can swap in a deterministic fake.
var execHaiku = runClaudeHaiku

// triageCmd wires `linkari triage` into the root command.
func triageCmd() *cobra.Command {
	var (
		queueDB     string
		url         string
		profile     string
		workspace   string
		contentFile string
		dryRun      bool
		noPersist   bool
	)

	cmd := &cobra.Command{
		Use:   "triage",
		Short: "Score link content via Haiku and persist to the queue (EPIC-043 M2)",
		Long: `Run profile-specific triage on workspace content via Haiku and
persist the score to the linkari queue.

Mirrors the fish function _uinit_profile_prompt: loads
docs/prompts/profiles/<profile>.md as the system prompt, pipes the workspace
content (truncated to 2000 chars) through Haiku via the claude CLI, parses
the resulting markdown, and writes the score to the queue + a _score.json
sidecar in the workspace.

Use --dry-run to inspect the assembled prompt without calling Haiku.
Use --no-persist to call Haiku and parse but skip the queue write (used by
the eval harness path).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if profile == "" {
				return fmt.Errorf("--profile required")
			}
			if workspace == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("resolve workspace: %w", err)
				}
				workspace = cwd
			}
			if contentFile == "" {
				contentFile = filepath.Join(workspace, "README.md")
			}

			// 1. Load profile template.
			tmplPath, sysPrompt, err := loadProfileTemplate(profile)
			if err != nil {
				return err
			}

			// 2. Read content (or stdin if "-").
			var content string
			if contentFile == "-" {
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				content = string(b)
			} else {
				b, err := os.ReadFile(contentFile)
				if err != nil {
					return fmt.Errorf("read content: %w", err)
				}
				content = string(b)
			}
			content = truncateRunes(content, contentTruncationRunes)
			if strings.TrimSpace(content) == "" {
				return fmt.Errorf("empty content from %s", contentFile)
			}

			// 3. Dry run? Dump exact bytes Haiku would see and exit.
			if dryRun {
				fmt.Fprintf(os.Stderr, "triage: dry-run profile=%s template=%s content=%dch\n", profile, tmplPath, len(content))
				fmt.Printf("===SYSTEM PROMPT===\n%s\n===USER INPUT===\n%s", sysPrompt, content)
				return nil
			}

			// 4. Call Haiku.
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			raw, err := execHaiku(ctx, sysPrompt, content)
			if err != nil {
				return fmt.Errorf("haiku: %w", err)
			}

			// 5. Parse triage markdown.
			res, err := parseTriageMarkdown(raw)
			if err != nil {
				return fmt.Errorf("parse triage: %w", err)
			}
			res.RawMarkdown = raw

			if noPersist {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}

			// 6. Persist via queue + sidecar + README append.
			slug := filepath.Base(workspace)
			queueDB = resolveQueueDB(queueDB)
			q, err := NewQueue(queueDB, false)
			if err != nil {
				return fmt.Errorf("opening queue: %w", err)
			}
			defer q.Close()

			item, _, err := q.ScoreByURL(url, res.Score, res.Verdict, res.Tags, profile, slug)
			if err != nil {
				return fmt.Errorf("score: %w", err)
			}
			// Auto-archive if score meets profile threshold (mirrors cmd_score.go).
			threshold := archiveThreshold(item.Profile)
			if threshold >= 0 && item.Score != nil && *item.Score >= threshold {
				if archErr := q.Archive(item.ID); archErr == nil {
					item.Status = "archived"
				}
			}

			// 7. Write _score.json sidecar (fish line 138-145 shape).
			if err := writeScoreSidecar(workspace, res.Score, res.Verdict, slug, profile, url); err != nil {
				fmt.Fprintf(os.Stderr, "WARN: write _score.json: %v\n", err)
			}

			// 8. Append triage block to README.md (fish line 122 format).
			if err := appendTriageToReadme(workspace, raw); err != nil {
				fmt.Fprintf(os.Stderr, "WARN: append README: %v\n", err)
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(item)
		},
	}

	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().StringVar(&url, "url", "", "URL being triaged (required for persist)")
	cmd.Flags().StringVar(&profile, "profile", "eng", "scoring profile (eng/life/finance/...)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "workspace directory (default: cwd)")
	cmd.Flags().StringVar(&contentFile, "content-file", "", "content file to score (default: <workspace>/README.md, '-' for stdin)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "assemble + dump prompt, skip Haiku call and persist")
	cmd.Flags().BoolVar(&noPersist, "no-persist", false, "call Haiku + parse but skip queue write and sidecar (used by eval)")

	return cmd
}

// loadProfileTemplate finds the profile prompt template using the same
// precedence as _uinit_profile_prompt.fish lines 19-29:
//
//  1. $ORG_PATH/docs/prompts/profiles/<profile>.md
//  2. ~/code/personal/docs/prompts/profiles/<profile>.md
func loadProfileTemplate(profile string) (path, content string, err error) {
	var candidates []string
	if orgPath := os.Getenv("ORG_PATH"); orgPath != "" {
		candidates = append(candidates, filepath.Join(orgPath, "docs", "prompts", "profiles", profile+".md"))
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		candidates = append(candidates, filepath.Join(home, "code", "personal", "docs", "prompts", "profiles", profile+".md"))
	}
	for _, p := range candidates {
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			continue
		}
		if len(bytes.TrimSpace(b)) == 0 {
			continue
		}
		return p, string(b), nil
	}
	return "", "", fmt.Errorf("no profile prompt template for %q (checked %v)", profile, candidates)
}

// runClaudeHaiku shells out to the claude CLI for a single-turn Haiku call,
// matching _uinit_profile_prompt.fish line 50-53:
//
//	printf '%s' "$content" | env -u CLAUDECODE claude --print \
//	    --model claude-haiku-4-5-20251001 --max-turns 1 --tools "" \
//	    --system-prompt "$system_prompt"
//
// CLAUDECODE is unset to mirror fish (`env -u CLAUDECODE`) — claude CLI
// behaves differently when invoked from inside Claude Code itself.
func runClaudeHaiku(ctx context.Context, systemPrompt, content string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude",
		"--print",
		"--model", claudeModel,
		"--max-turns", "1",
		"--tools", "",
		"--system-prompt", systemPrompt,
	)
	cmd.Stdin = strings.NewReader(content)

	// Mirror fish `env -u CLAUDECODE`: copy current env minus CLAUDECODE.
	env := os.Environ()
	filtered := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDECODE=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	cmd.Env = filtered

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude exec: %w (stderr=%s)", err, strings.TrimSpace(stderr.String()))
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return "", fmt.Errorf("claude returned empty output")
	}
	return out, nil
}

// parseTriageMarkdown extracts a TriageResult from Haiku's markdown output.
// Score is mandatory; Verdict / ActionItems / Tags are best-effort.
func parseTriageMarkdown(md string) (TriageResult, error) {
	// Try parse as-is first; if score regex misses, run normalizer.
	score, err := parseScoreFromMarkdown(md)
	normalized := md
	if err != nil {
		normalized = normalizeTriageMarkdown(md)
		score, err = parseScoreFromMarkdown(normalized)
		if err != nil {
			return TriageResult{}, err
		}
	}

	verdict := extractVerdict(normalized)
	actionItems := extractActionItems(normalized)
	tags := extractTagsLine(normalized)

	return TriageResult{
		Score:       score,
		Verdict:     verdict,
		ActionItems: actionItems,
		Tags:        tags,
	}, nil
}

// verdictRE captures the text after `## Verdict` up to the next `##` or EOF.
// Handles both `## Verdict\nbody` and the flat-line `## Verdict body` form
// the existing fixtures use (see 20260404_103238_gh_teamchong-turboquant-wasm).
var verdictRE = regexp.MustCompile(`(?s)##\s*Verdict\s*\n?\s*(.+?)(?:\n##|$)`)

func extractVerdict(md string) string {
	m := verdictRE.FindStringSubmatch(md)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// actionItemsRE locates the `## Action Items` block and grabs everything
// up to the next `##` heading.
var actionItemsRE = regexp.MustCompile(`(?s)##\s*Action Items\s*\n?(.+?)(?:\n##|$)`)
var actionItemLineRE = regexp.MustCompile(`(?m)^(?:\s*[-*]\s+|\s*\d+\.\s+)(.+)$`)

func extractActionItems(md string) []string {
	m := actionItemsRE.FindStringSubmatch(md)
	if len(m) < 2 {
		return nil
	}
	block := m[1]
	var items []string
	for _, lm := range actionItemLineRE.FindAllStringSubmatch(block, -1) {
		if len(lm) < 2 {
			continue
		}
		items = append(items, strings.TrimSpace(lm[1]))
	}
	return items
}

// tagsLineRE matches `Tags: a, b, c` on its own line.
var tagsLineRE = regexp.MustCompile(`(?mi)^\s*Tags:\s*(.+)$`)

func extractTagsLine(md string) string {
	m := tagsLineRE.FindStringSubmatch(md)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// normalizeTriageMarkdown is a Go port of the python normalizer in
// _uinit_profile_prompt.fish lines 62-114. It only kicks in if the raw
// parser failed — Haiku usually emits clean markdown, but occasionally
// returns everything on one line.
func normalizeTriageMarkdown(text string) string {
	// 1. Newlines before ## headings.
	reHeading := regexp.MustCompile(`  ?(?:## )`)
	text = reHeading.ReplaceAllStringFunc(text, func(s string) string {
		return "\n\n## "
	})
	// 2. Newlines before list items.
	reBullet := regexp.MustCompile(` (?:- \*?\*?[A-Z])`)
	text = reBullet.ReplaceAllStringFunc(text, func(s string) string {
		return "\n" + strings.TrimPrefix(s, " ")
	})
	reNum := regexp.MustCompile(` (?:\d+\.\s)`)
	text = reNum.ReplaceAllStringFunc(text, func(s string) string {
		return "\n" + strings.TrimPrefix(s, " ")
	})
	// 3. Collapse triple+ newlines.
	reNL := regexp.MustCompile(`\n{3,}`)
	text = reNL.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// truncateRunes returns at most n runes from s, matching fish `string sub -l n`.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// writeScoreSidecar writes _score.json with byte-equivalent shape to fish
// _uinit_profile_prompt.fish lines 138-145.
func writeScoreSidecar(workspace string, score int, verdict, slug, profile, url string) error {
	payload := struct {
		Score    int    `json:"score"`
		Verdict  string `json:"verdict"`
		Slug     string `json:"slug"`
		Profile  string `json:"profile"`
		URL      string `json:"url"`
		ScoredAt string `json:"scored_at"`
	}{
		Score:    score,
		Verdict:  verdict,
		Slug:     slug,
		Profile:  profile,
		URL:      url,
		ScoredAt: nowRFC3339UTC(),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(filepath.Join(workspace, "_score.json"), b, 0644)
}

// appendTriageToReadme appends `\n---\n\n<raw>\n` to README.md, matching
// fish line 122: `printf '\n---\n\n%s\n' "$triage_normalized" >> "$readme_file"`.
func appendTriageToReadme(workspace, raw string) error {
	path := filepath.Join(workspace, "README.md")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n---\n\n%s\n", raw)
	return err
}

// nowRFC3339UTC is split out so tests can stub the timestamp.
var nowRFC3339UTC = func() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// --- Eval harness wiring ---

// triageScorer implements Scorer (cmd_eval.go) by running the real Go
// triage path against each fixture's content + profile. Used by `linkari
// eval run` once registeredScorer() returns it.
type triageScorer struct{}

func (triageScorer) Name() string { return "triage-haiku" }

func (triageScorer) Score(fix Fixture) (Golden, error) {
	_, sysPrompt, err := loadProfileTemplate(fix.Profile)
	if err != nil {
		return Golden{}, fmt.Errorf("load template: %w", err)
	}
	// Fixture content was already truncated at capture time, but apply the
	// same truncation here so eval is robust against future fixture sources.
	content := truncateRunes(fix.Content, contentTruncationRunes)
	raw, err := execHaiku(context.Background(), sysPrompt, content)
	if err != nil {
		return Golden{}, err
	}
	res, err := parseTriageMarkdown(raw)
	if err != nil {
		return Golden{}, err
	}
	return Golden{
		Score:       res.Score,
		Verdict:     res.Verdict,
		RawMarkdown: raw,
	}, nil
}
