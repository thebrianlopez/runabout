package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/sdk/trace"
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
		flagProject  string
		flagSecrets  []string
		flagDryRun   bool
	)

	cmd := &cobra.Command{
		Use:   "chain-eval",
		Short: "Evaluate chain prompt quality against fixture scenarios",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := runConfig{
				all:     flagAll,
				fixture: flagFixture,
				minScore: flagMinScore,
				project: flagProject,
				secrets: flagSecrets,
				dryRun:  flagDryRun,
			}
			os.Exit(run(cmd.Context(), cfg))
			return nil
		},
	}

	cmd.Flags().BoolVar(&flagAll, "all", false, "Run all fixtures")
	cmd.Flags().StringVar(&flagFixture, "fixture", "", "Run a single named fixture")
	cmd.Flags().Float64Var(&flagMinScore, "min-score", 0.85, "Minimum passing score per dimension")
	cmd.Flags().StringVar(&flagProject, "project", "chain", "Braintrust project name")
	cmd.Flags().StringArrayVar(&flagSecrets, "secret", nil, "AWS Secrets Manager secret name or ARN (repeatable); each secret must be a JSON object whose keys are set as environment variables")
	cmd.Flags().BoolVar(&flagDryRun, "dry-run", false, "Validate fixtures and prompt without calling Anthropic or Braintrust APIs")

	return cmd
}

type runConfig struct {
	all      bool
	fixture  string
	minScore float64
	project  string
	secrets  []string
	dryRun   bool
}

func run(ctx context.Context, cfg runConfig) int {
	// Populate env from AWS Secrets Manager before any validation (exit 2 = config error).
	if err := loadSecrets(ctx, cfg.secrets); err != nil {
		fmt.Fprintf(os.Stderr, "chain-eval: secrets_fetch_failed: %v\n", err)
		return 2
	}

	if cfg.dryRun {
		return dryRun(cfg)
	}

	// Validate required environment variables (exit 2 = config error).
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "chain-eval: ANTHROPIC_API_KEY required")
		return 2
	}
	if os.Getenv("BRAINTRUST_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "chain-eval: BRAINTRUST_API_KEY required")
		return 2
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
	if len(cases) == 0 {
		fmt.Fprintln(os.Stderr, "chain-eval: no fixtures to run")
		return 2
	}

	// Score accumulator collects per-dimension scores for threshold checking.
	coll := &scoreCollector{byDim: make(map[string][]float64)}

	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck

	bt, err := braintrust.New(tp,
		braintrust.WithProject(cfg.project),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "chain-eval: eval SDK error: %v\n", err)
		return 2
	}

	anthropicClient := anthropic.NewClient()
	evaluator := braintrust.NewEvaluator[ChainInput, string](bt)

	result, err := evaluator.Run(ctx, eval.Opts[ChainInput, string]{
		Experiment:  "chain-eval",
		ProjectName: cfg.project,
		Dataset:     eval.NewDataset(cases),
		Task: eval.T(func(ctx context.Context, input ChainInput) (string, error) {
			return runTask(ctx, &anthropicClient, prompt, fixturesDir, input)
		}),
		Scorers:     collectingScorers(coll),
		Parallelism: 3,
		Metadata: map[string]any{
			"prompt_sha":    promptSHA(prompt),
			"chain_version": "cmd/chain-eval",
		},
	})

	// API/task errors → exit 1 (score failure, not config error).
	apiErr := false
	if result != nil && result.Error() != nil {
		fmt.Fprintf(os.Stderr, "chain-eval: fixture errors: %v\n", result.Error())
		apiErr = true
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "chain-eval: eval SDK error: %v\n", err)
		return 2
	}

	printScoreTable(coll, cfg.minScore)

	if link, _ := result.Permalink(); link != "" {
		fmt.Fprintln(os.Stderr, "Braintrust:", link)
	}

	if apiErr || !allPass(coll, cfg.minScore) {
		return 1
	}
	return 0
}

// runTask loads the fixture docs, calls Claude with the system prompt, and returns the raw response.
// API errors are returned as errors (non-fatal per case - the eval SDK continues other cases).
func runTask(ctx context.Context, client *anthropic.Client, prompt, fixturesDir string, input ChainInput) (string, error) { //nolint:gocritic
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
		System: []anthropic.TextBlockParam{
			{Text: prompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userMsg)),
		},
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

// loadFixture reads all markdown files in a fixture directory and concatenates them.
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
	if err != nil {
		return "", err
	}
	return sb.String(), nil
}

// buildDataset constructs the eval cases. If fixture is non-empty, runs only that fixture.
func buildDataset(fixturesDir, fixture string) []eval.Case[ChainInput, string] {
	all := allFixtures()
	if fixture != "" {
		for _, c := range all {
			if c.Input.Fixture == fixture {
				return []eval.Case[ChainInput, string]{c}
			}
		}
		fmt.Fprintf(os.Stderr, "chain-eval: fixture '%s' not found\n", fixture)
		return nil
	}
	return all
}

// allFixtures defines the 7 eval cases with their commands and expected values.
func allFixtures() []eval.Case[ChainInput, string] {
	return []eval.Case[ChainInput, string]{
		{
			Input: ChainInput{
				Command: "/chain next",
				Fixture: "tdd_approved_no_epic",
				Expected: ChainExpected{
					PriorityStep: 6,
					IconMap: map[string]string{
						"PRD": "✅",
						"FDD": "✅",
						"TDD": "✅",
					},
				},
			},
		},
		{
			Input: ChainInput{
				Command: "/chain next",
				Fixture: "pomo_pending",
				Expected: ChainExpected{
					PriorityStep: 1,
					IconMap: map[string]string{
						"PRD":  "✅",
						"FDD":  "✅",
						"TDD":  "✅",
						"Epic": "🔴",
					},
				},
			},
		},
		{
			Input: ChainInput{
				Command: "/chain validate",
				Fixture: "release_gate_violation",
				Expected: ChainExpected{
					Violations: []string{"release gate", "missing"},
					IconMap: map[string]string{
						"PRD": "✅",
						"FDD": "✅",
					},
				},
			},
		},
		{
			Input: ChainInput{
				Command: "/chain next",
				Fixture: "design_check_blocked",
				Expected: ChainExpected{
					PriorityStep: 4,
					IconMap: map[string]string{
						"PRD": "✅",
						"FDD": "🔴",
					},
				},
			},
		},
		{
			Input: ChainInput{
				Command: "/chain next",
				Fixture: "design_check_clear",
				Expected: ChainExpected{
					PriorityStep: 5,
					IconMap: map[string]string{
						"PRD": "✅",
						"FDD": "✅",
					},
				},
			},
		},
		{
			Input: ChainInput{
				Command: "/chain next",
				Fixture: "batch_dispatch_ready",
				Expected: ChainExpected{
					PriorityStep: 7,
					IconMap: map[string]string{
						"PRD":  "✅",
						"FDD":  "✅",
						"TDD":  "✅",
						"Epic": "🟡",
					},
				},
			},
		},
		{
			Input: ChainInput{
				Command: "/chain status",
				Fixture: "all_approved",
				Expected: ChainExpected{
					IconMap: map[string]string{
						"PRD":  "✅",
						"FDD":  "✅",
						"TDD":  "✅",
						"Epic": "✅",
					},
				},
			},
		},
	}
}

// scoreCollector accumulates per-dimension scores across parallel fixture runs.
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

// collectingScorers wraps the three scorer functions so they also record to the collector.
func collectingScorers(coll *scoreCollector) []eval.Scorer[ChainInput, string] {
	wrap := func(dim string, fn func(context.Context, eval.TaskResult[ChainInput, string]) (eval.Scores, error)) eval.ScoreFunc[ChainInput, string] {
		return func(ctx context.Context, r eval.TaskResult[ChainInput, string]) (eval.Scores, error) {
			scores, err := fn(ctx, r)
			if err == nil && len(scores) > 0 {
				coll.record(dim, scores[0].Score)
			}
			return scores, err
		}
	}
	return []eval.Scorer[ChainInput, string]{
		eval.NewScorer("next_action", wrap("next_action", scoreNextAction)),
		eval.NewScorer("validate_recall", wrap("validate_recall", scoreValidateRecall)),
		eval.NewScorer("icon_accuracy", wrap("icon_accuracy", scoreIconAccuracy)),
	}
}

// dryRun validates fixtures and prompt existence without calling any external APIs.
// Exits 0 on success, 1 if any fixture directory is missing or unreadable.
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
		fmt.Fprintf(os.Stderr, "chain-eval: WARN: prompt file not found: %s (required for real runs)\n", promptPath)
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
	fmt.Printf("%-20s  %s\n", "Dimension", "Avg Score")
	fmt.Println(strings.Repeat("-", 40))
	for _, dim := range dimensions {
		avg, ok := coll.avg(dim)
		if !ok {
			fmt.Printf("%-20s  n/a\n", dim)
			continue
		}
		status := "PASS"
		if avg < threshold {
			status = "FAIL"
		}
		fmt.Printf("%-20s  %.2f  %s\n", dim, avg, status)
	}
	fmt.Printf("%-20s  %.2f\n", "Threshold", threshold)
	fmt.Println()
}

func promptSHA(prompt string) string {
	h := 0
	for _, b := range []byte(prompt) {
		h = h*31 + int(b)
	}
	return fmt.Sprintf("%08x", uint32(h))
}
