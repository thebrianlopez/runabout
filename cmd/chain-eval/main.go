package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(2)
	}
}

func rootCmd() *cobra.Command {
	var (
		flagAll      bool
		flagFixture  string
		flagMinScore float64
		flagSecrets  []string
		flagDryRun   bool
		flagFromHub  bool
	)

	cmd := &cobra.Command{
		Use:   "chain-eval",
		Short: "Evaluate chain prompt quality against fixture scenarios",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := runConfig{
				all:      flagAll,
				fixture:  flagFixture,
				minScore: flagMinScore,
				secrets:  flagSecrets,
				dryRun:   flagDryRun,
				fromHub:  flagFromHub,
			}
			os.Exit(run(cmd.Context(), cfg))
			return nil
		},
	}

	cmd.Flags().BoolVar(&flagAll, "all", false, "Run all fixtures")
	cmd.Flags().StringVar(&flagFixture, "fixture", "", "Run a single named fixture")
	cmd.Flags().Float64Var(&flagMinScore, "min-score", 0.85, "Minimum passing score per dimension")
	cmd.Flags().StringArrayVar(&flagSecrets, "secret", nil, "AWS Secrets Manager secret ARN (repeatable); each secret must be a JSON object whose keys are set as env vars")
	cmd.Flags().BoolVar(&flagDryRun, "dry-run", false, "Validate fixtures and prompt without calling any APIs")
	cmd.Flags().BoolVar(&flagFromHub, "from-hub", false, "Load fixtures from HF Hub (HF_FIXTURES_REPO) with local fallback")

	cmd.AddCommand(dumpFixturesCmd())
	cmd.AddCommand(pushFixturesCmd())

	return cmd
}

type runConfig struct {
	all      bool
	fixture  string
	minScore float64
	secrets  []string
	dryRun   bool
	fromHub  bool
}

func run(ctx context.Context, cfg runConfig) int {
	if err := loadSecrets(ctx, cfg.secrets); err != nil {
		fmt.Fprintf(os.Stderr, "chain-eval: secrets_fetch_failed: %v\n", err)
		return 2
	}

	if cfg.dryRun {
		return dryRun(cfg)
	}

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "chain-eval: ANTHROPIC_API_KEY required")
		return 2
	}
	if os.Getenv("HUGGINGFACE_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "chain-eval: WARN: HUGGINGFACE_API_KEY not set - judge disabled, deterministic scoring only")
	}

	promptsDir := os.Getenv("CHAIN_PROMPTS_DIR")
	if promptsDir == "" {
		promptsDir = "docs/core/prompts"
	}
	promptPath := filepath.Join(promptsDir, "command_chain.md")
	promptBytes, err := os.ReadFile(promptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "chain-eval: prompt file not found: %s\n", promptPath)
		return 2
	}
	prompt := string(promptBytes)

	fixturesDir := os.Getenv("CHAIN_FIXTURES_DIR")
	if fixturesDir == "" {
		fixturesDir = "cmd/chain-eval/fixtures"
	}

	cases := buildDataset(fixturesDir, cfg.fixture)
	if cfg.fromHub {
		if hub := loadHubFixtures(ctx); len(hub) > 0 {
			cases = mergeFixtures(cases, hub)
		}
	}
	if len(cases) == 0 {
		fmt.Fprintln(os.Stderr, "chain-eval: no fixtures to run")
		return 2
	}

	runID := fmt.Sprintf("%s-%s", promptSHA(prompt), time.Now().UTC().Format("20060102T150405Z"))
	coll := &scoreCollector{byDim: make(map[string][]float64)}
	client := anthropic.NewClient()

	// Run fixtures with bounded concurrency, collect ResultRows for hub push.
	// Default to 1 to stay under low Anthropic TPM limits; callers can raise with
	// CHAIN_EVAL_CONCURRENCY once quota is available.
	concurrency := 1
	if raw := os.Getenv("CHAIN_EVAL_CONCURRENCY"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			concurrency = parsed
		}
	}
	rows := make([]ResultRow, len(cases))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, c := range cases {
		wg.Add(1)
		go func(idx int, cas EvalCase) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			output, taskErr := runTask(ctx, &client, prompt, fixturesDir, cas.Input)
			if taskErr != nil {
				coll.record("next_action", 0)
				coll.record("validate_recall", 0)
				coll.record("icon_accuracy", 0)
				rows[idx] = ResultRow{
					RunID: runID, Fixture: cas.Input.Fixture, Command: cas.Input.Command,
					ScoredAt: time.Now().UTC().Format(time.RFC3339),
				}
				return
			}

			tr := TaskResult{Input: cas.Input, Output: output}
			na, naJ := scoreNextAction(ctx, tr)
			vr, vrJ := scoreValidateRecall(ctx, tr)
			ia, _ := scoreIconAccuracy(ctx, tr)

			coll.record("next_action", na)
			coll.record("validate_recall", vr)
			coll.record("icon_accuracy", ia)

			rows[idx] = ResultRow{
				RunID:          runID,
				Fixture:        cas.Input.Fixture,
				Command:        cas.Input.Command,
				NextAction:     na,
				ValidateRecall: vr,
				IconAccuracy:   ia,
				JudgeInvoked:   naJ || vrJ,
				ScoredAt:       time.Now().UTC().Format(time.RFC3339),
			}
		}(i, c)
	}
	wg.Wait()

	// Set PassThreshold after all scores collected.
	pass := allPass(coll, cfg.minScore)
	for i := range rows {
		rows[i].PassThreshold = pass
	}

	printScoreTable(coll, cfg.minScore)

	// Push results: prefer HF Bucket (no commit rate limit, accumulates history)
	// over dataset commit. Falls back to hubPushBatch when bucket not configured.
	if bucket := os.Getenv("HF_RESULTS_BUCKET"); bucket != "" {
		runID := ""
		if len(rows) > 0 {
			runID = rows[0].RunID
		}
		if err := hubPushBucket(ctx, rows, bucket); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ bucket push failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "HF Bucket: %s\n", bucketRunURL(bucket, runID))
		}
	} else {
		// Legacy: dataset commit (1 commit/run, subject to 128/hr free-tier limit).
		if err := hubPushBatch(ctx, rows); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ hub push failed: %v\n", err)
		} else if repo := os.Getenv("HF_DATASET_REPO"); repo != "" {
			fmt.Fprintf(os.Stderr, "HF Hub: https://huggingface.co/datasets/%s\n", repo)
		}
	}

	if !pass {
		return 1
	}
	return 0
}

// EvalCase holds one fixture to evaluate.
type EvalCase struct {
	Input ChainInput
}

func runTask(ctx context.Context, client *anthropic.Client, prompt, fixturesDir string, input ChainInput) (string, error) {
	fixtureDir := filepath.Join(fixturesDir, input.Fixture)
	fixtureContent, err := loadFixture(fixtureDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "chain-eval: fixture '%s' not found - skipped\n", input.Fixture)
		return "", fmt.Errorf("fixture '%s': %w", input.Fixture, err)
	}

	userMsg := fmt.Sprintf("Command: %s\n\nDocs state:\n\n%s", input.Command, fixtureContent)
	msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeHaiku4_5,
		MaxTokens: 1024,
		System:    []anthropic.TextBlockParam{{Text: prompt}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(userMsg))},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "chain-eval: fixture '%s': API error: %v - scored 0\n", input.Fixture, err)
		return "", err
	}
	if len(msg.Content) == 0 {
		return "", fmt.Errorf("fixture '%s': empty response", input.Fixture)
	}
	return msg.Content[0].Text, nil
}

func loadFixture(dir string) (string, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", fmt.Errorf("directory not found: %s", dir)
	}
	var sb strings.Builder
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(dir, path)
		fmt.Fprintf(&sb, "## File: %s\n\n%s\n\n", rel, string(content))
		return nil
	})
	return sb.String(), err
}

// loadHubFixtures downloads fixture metadata from HF Hub (HF_FIXTURES_REPO env var).
// Returns empty slice on any error  -  caller falls back to local fixtures (non-fatal).
func loadHubFixtures(ctx context.Context) []EvalCase {
	repo := os.Getenv("HF_FIXTURES_REPO")
	if repo == "" {
		return nil
	}
	apiKey := os.Getenv("HUGGINGFACE_API_KEY")
	url := fmt.Sprintf("%s/datasets/%s/resolve/main/fixtures.jsonl", hubBaseURL, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ hub fixtures unavailable, falling back to local CHAIN_FIXTURES_DIR\n")
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "⚠ hub fixtures unavailable, falling back to local CHAIN_FIXTURES_DIR\n")
		if resp != nil {
			resp.Body.Close() //nolint:errcheck
		}
		return nil
	}
	defer resp.Body.Close() //nolint:errcheck

	var cases []EvalCase
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var meta FixtureMeta
		if err := json.Unmarshal(line, &meta); err != nil || meta.Command == "" {
			continue
		}
		// Hub fixtures use fixture name from meta; state loaded from local fallback dir if present.
		name := meta.Source + "_" + strings.ReplaceAll(meta.Command, "/", "")
		cases = append(cases, EvalCase{Input: ChainInput{
			Command:  meta.Command,
			Fixture:  name,
			Expected: meta.Expected,
		}})
	}
	return cases
}

// mergeFixtures merges hub fixtures into local cases, deduplicating by fixture name.
func mergeFixtures(local, hub []EvalCase) []EvalCase {
	seen := make(map[string]bool, len(local))
	for _, c := range local {
		seen[c.Input.Fixture] = true
	}
	merged := append([]EvalCase(nil), local...)
	for _, c := range hub {
		if !seen[c.Input.Fixture] {
			merged = append(merged, c)
			seen[c.Input.Fixture] = true
		}
	}
	return merged
}

// FixtureMeta is loaded from fixture.json in each discovered fixture directory.
type FixtureMeta struct {
	Command    string        `json:"command"`
	Source     string        `json:"source"`     // hand_crafted | flowbench_adapted | ifeval_adapted
	Difficulty string        `json:"difficulty"` // standard | hard
	Expected   ChainExpected `json:"expected"`
}

func buildDataset(fixturesDir, fixture string) []EvalCase {
	all := append(hardcodedFixtures(), discoverFixtures(fixturesDir)...)
	if fixture == "" {
		return all
	}
	for _, c := range all {
		if c.Input.Fixture == fixture {
			return []EvalCase{c}
		}
	}
	fmt.Fprintf(os.Stderr, "chain-eval: fixture '%s' not found\n", fixture)
	return nil
}

// discoverFixtures scans fixturesDir for subdirectories containing fixture.json
// and returns an EvalCase for each one found.
func discoverFixtures(fixturesDir string) []EvalCase {
	entries, err := os.ReadDir(fixturesDir)
	if err != nil {
		return nil
	}
	var cases []EvalCase
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(fixturesDir, e.Name(), "fixture.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue // no fixture.json, skip
		}
		var meta FixtureMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ fixture '%s': bad fixture.json: %v - skipped\n", e.Name(), err)
			continue
		}
		if meta.Command == "" {
			continue
		}
		cases = append(cases, EvalCase{Input: ChainInput{
			Command:  meta.Command,
			Fixture:  e.Name(),
			Expected: meta.Expected,
		}})
	}
	return cases
}

func hardcodedFixtures() []EvalCase {
	return []EvalCase{
		{Input: ChainInput{
			Command: "/chain next", Fixture: "tdd_approved_no_epic",
			Expected: ChainExpected{PriorityStep: 6, IconMap: map[string]string{"PRD": "✅", "FDD": "✅", "TDD": "✅"}},
		}},
		{Input: ChainInput{
			Command: "/chain next", Fixture: "pomo_pending",
			// /chain next does not render a tree — icon_map omitted (n/a, scores 1.0)
			Expected: ChainExpected{PriorityStep: 1},
		}},
		{Input: ChainInput{
			Command: "/chain validate", Fixture: "release_gate_violation",
			// validate output: gate table only, no chain tree — drop icon_map
			Expected: ChainExpected{Violations: []string{"release gate", "missing"}},
		}},
		{Input: ChainInput{
			Command: "/chain next", Fixture: "design_check_blocked",
			// /chain next does not render a tree — icon_map omitted (n/a, scores 1.0)
			Expected: ChainExpected{PriorityStep: 4},
		}},
		{Input: ChainInput{
			Command: "/chain next", Fixture: "design_check_clear",
			// /chain next does not render a tree — icon_map omitted (n/a, scores 1.0)
			Expected: ChainExpected{PriorityStep: 5},
		}},
		{Input: ChainInput{
			Command: "/chain next", Fixture: "batch_dispatch_ready",
			// /chain next does not render a tree — icon_map omitted (n/a, scores 1.0)
			Expected: ChainExpected{PriorityStep: 7},
		}},
		{Input: ChainInput{
			Command: "/chain status", Fixture: "all_approved",
			Expected: ChainExpected{IconMap: map[string]string{"PRD": "✅", "FDD": "✅", "TDD": "✅", "Epic": "✅"}},
		}},
	}
}

type scoreCollector struct {
	mu    sync.Mutex
	byDim map[string][]float64
}

func (c *scoreCollector) record(dim string, score float64) {
	c.mu.Lock()
	c.byDim[dim] = append(c.byDim[dim], score)
	c.mu.Unlock()
}

func (c *scoreCollector) avg(dim string) (float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	vals := c.byDim[dim]
	if len(vals) == 0 {
		return 0, false
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals)), true
}

func dryRun(cfg runConfig) int {
	fixturesDir := os.Getenv("CHAIN_FIXTURES_DIR")
	if fixturesDir == "" {
		fixturesDir = "cmd/chain-eval/fixtures"
	}
	promptsDir := os.Getenv("CHAIN_PROMPTS_DIR")
	if promptsDir == "" {
		promptsDir = "docs/core/prompts"
	}
	promptPath := filepath.Join(promptsDir, "command_chain.md")
	if _, err := os.Stat(promptPath); err != nil {
		fmt.Fprintf(os.Stderr, "chain-eval: WARN: prompt file not found: %s\n", promptPath)
	} else {
		fmt.Printf("chain-eval: prompt OK: %s\n", promptPath)
	}

	cases := buildDataset(fixturesDir, cfg.fixture)
	if len(cases) == 0 {
		fmt.Fprintln(os.Stderr, "chain-eval: no fixtures to run")
		return 1
	}
	failed := 0
	for _, c := range cases {
		dir := filepath.Join(fixturesDir, c.Input.Fixture)
		content, err := loadFixture(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "chain-eval: FAIL fixture '%s': %v\n", c.Input.Fixture, err)
			failed++
			continue
		}
		fmt.Printf("chain-eval: OK   fixture '%s' (%d chars)\n", c.Input.Fixture, len(content))
	}
	fmt.Printf("\n%d/%d fixtures ready", len(cases)-failed, len(cases))
	if failed > 0 {
		fmt.Printf(" (%d missing)\n", failed)
		return 1
	}
	fmt.Printf(" - ready to eval\n")
	return 0
}

var dimensions = []string{"next_action", "validate_recall", "icon_accuracy"}

func allPass(coll *scoreCollector, threshold float64) bool {
	for _, dim := range dimensions {
		avg, ok := coll.avg(dim)
		if ok && avg < threshold {
			return false
		}
	}
	return true
}

func printScoreTable(coll *scoreCollector, threshold float64) {
	fmt.Println()
	fmt.Printf("%-20s  %-10s  %-10s  %s\n", "Dimension", "Avg Score", "Margin", "")
	fmt.Println(strings.Repeat("-", 50))
	for _, dim := range dimensions {
		avg, ok := coll.avg(dim)
		if !ok {
			fmt.Printf("%-20s  n/a\n", dim)
			continue
		}
		status := "PASS"
		margin := avg - threshold
		if avg < threshold {
			status = "FAIL"
		}
		fmt.Printf("%-20s  %.4f  %+.4f  %s\n", dim, avg, margin, status)
	}
	fmt.Printf("%-20s  %.4f\n", "Threshold", threshold)
	fmt.Println()
}

func promptSHA(prompt string) string {
	h := 0
	for _, b := range []byte(prompt) {
		h = h*31 + int(b)
	}
	return fmt.Sprintf("%08x", uint32(h))
}
