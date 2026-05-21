package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// FixtureDump is the JSONL schema for a fixture in the HF Hub dataset.
type FixtureDump struct {
	FixtureName string        `json:"fixture_name"`
	Command     string        `json:"command"`
	Source      string        `json:"source"`
	Difficulty  string        `json:"difficulty"`
	DocsState   string        `json:"docs_state"`
	Expected    ChainExpected `json:"expected"`
}

// dumpFixturesCmd returns the `chain-eval dump-fixtures` subcommand.
func dumpFixturesCmd() *cobra.Command {
	var flagOutput string

	cmd := &cobra.Command{
		Use:   "dump-fixtures",
		Short: "Write all fixtures as JSONL to stdout or a file",
		RunE: func(cmd *cobra.Command, args []string) error {
			fixturesDir := os.Getenv("CHAIN_FIXTURES_DIR")
			if fixturesDir == "" {
				fixturesDir = "cmd/chain-eval/fixtures"
			}

			dumps, err := collectDumps(fixturesDir)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if flagOutput != "" {
				f, err := os.Create(flagOutput)
				if err != nil {
					return fmt.Errorf("open output: %w", err)
				}
				defer f.Close() //nolint:errcheck
				out = f
			}

			for _, d := range dumps {
				line, err := json.Marshal(d)
				if err != nil {
					return fmt.Errorf("marshal fixture %s: %w", d.FixtureName, err)
				}
				fmt.Fprintln(out, string(line))
			}

			fmt.Fprintf(os.Stderr, "chain-eval: %d fixtures written\n", len(dumps))
			return nil
		},
	}

	cmd.Flags().StringVarP(&flagOutput, "output", "o", "", "Write JSONL to file instead of stdout")
	return cmd
}

// pushFixturesCmd returns the `chain-eval push-fixtures` subcommand.
func pushFixturesCmd() *cobra.Command {
	var flagSecrets []string

	cmd := &cobra.Command{
		Use:   "push-fixtures",
		Short: "Dump fixtures as JSONL and push to HF_FIXTURES_REPO on HF Hub",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if err := loadSecrets(ctx, flagSecrets); err != nil {
				return fmt.Errorf("secrets: %w", err)
			}

			apiKey := os.Getenv("HUGGINGFACE_API_KEY")
			if apiKey == "" {
				return fmt.Errorf("HUGGINGFACE_API_KEY required")
			}
			repo := os.Getenv("HF_FIXTURES_REPO")
			if repo == "" {
				return fmt.Errorf("HF_FIXTURES_REPO required (format: owner/dataset-name)")
			}

			fixturesDir := os.Getenv("CHAIN_FIXTURES_DIR")
			if fixturesDir == "" {
				fixturesDir = "cmd/chain-eval/fixtures"
			}

			dumps, err := collectDumps(fixturesDir)
			if err != nil {
				return err
			}

			// Build full JSONL content.
			var sb bytes.Buffer
			for _, d := range dumps {
				line, err := json.Marshal(d)
				if err != nil {
					return fmt.Errorf("marshal fixture %s: %w", d.FixtureName, err)
				}
				sb.Write(line)
				sb.WriteByte('\n')
			}

			if err := hubPushFile(ctx, repo, apiKey, "fixtures.jsonl", sb.String()); err != nil {
				if err.Error() == "hub push: API 404" {
					return fmt.Errorf("hub push: API 404  -  repo has no commits yet.\n"+
						"Initialize it first: https://huggingface.co/datasets/%s\n"+
						"Click 'Initialize this repository with a dataset card', then retry.", repo)
				}
				return fmt.Errorf("hub push: %w", err)
			}

			fmt.Fprintf(os.Stderr, "✅ Pushed %d fixtures to https://huggingface.co/datasets/%s\n", len(dumps), repo)
			return nil
		},
	}

	cmd.Flags().StringArrayVar(&flagSecrets, "secret", nil, "AWS Secrets Manager secret ARN (repeatable)")
	return cmd
}

// collectDumps builds a FixtureDump for every fixture (hardcoded + discovered).
func collectDumps(fixturesDir string) ([]FixtureDump, error) {
	cases := buildDataset(fixturesDir, "")

	// Build a source/difficulty lookup from fixture.json where available.
	metaByName := make(map[string]FixtureMeta, len(cases))
	for _, c := range discoverFixtures(fixturesDir) {
		metaPath := filepath.Join(fixturesDir, c.Input.Fixture, "fixture.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var m FixtureMeta
		if err := json.Unmarshal(data, &m); err == nil {
			metaByName[c.Input.Fixture] = m
		}
	}

	var dumps []FixtureDump
	for _, c := range cases {
		dir := filepath.Join(fixturesDir, c.Input.Fixture)
		state, err := loadFixture(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ fixture '%s': load failed: %v - skipped\n", c.Input.Fixture, err)
			continue
		}

		source := "hand_crafted"
		difficulty := "standard"
		if m, ok := metaByName[c.Input.Fixture]; ok {
			if m.Source != "" {
				source = m.Source
			}
			if m.Difficulty != "" {
				difficulty = m.Difficulty
			}
		}

		dumps = append(dumps, FixtureDump{
			FixtureName: c.Input.Fixture,
			Command:     c.Input.Command,
			Source:      source,
			Difficulty:  difficulty,
			DocsState:   state,
			Expected:    c.Input.Expected,
		})
	}
	return dumps, nil
}

// hubPushFile uploads a full file to an HF Hub dataset via the commit API.
func hubPushFile(ctx context.Context, repo, apiKey, path, content string) error {
	payload := map[string]any{
		"commit_message": fmt.Sprintf("chain-eval fixture sync %s", time.Now().UTC().Format("20060102T150405Z")),
		"operations": []map[string]any{{
			"operation": "addOrUpdate",
			"path":      path,
			"content":   content,
		}},
	}
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/api/datasets/%s/commit/main", hubBaseURL, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("API: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("API %d", resp.StatusCode)
	}
	return nil
}
