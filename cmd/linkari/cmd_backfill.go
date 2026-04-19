package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

type backfillResult struct {
	Found    int `json:"found"`
	Matched  int `json:"matched"`
	Inserted int `json:"inserted"`
	Skipped  int `json:"skipped"`
}

// scoreJSON is defined in score_sidecar.go (shared with watchdog rescue path).

func backfillCmd() *cobra.Command {
	var (
		queueDB string
		dryRun  bool
	)

	cmd := &cobra.Command{
		Use:   "backfill [directory]",
		Short: "Ingest existing _score.json files into the queue database",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			dir := filepath.Join(home, "code", "personal", "url_work")
			if len(args) > 0 {
				dir = args[0]
			}

			queueDB = resolveQueueDB(queueDB)
			q, err := NewQueue(queueDB, false)
			if err != nil {
				return fmt.Errorf("opening queue: %w", err)
			}
			defer q.Close()

			var result backfillResult
			err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil // skip unreadable dirs
				}
				if d.IsDir() || d.Name() != "_score.json" {
					return nil
				}
				result.Found++

				data, err := os.ReadFile(path)
				if err != nil {
					result.Skipped++
					return nil
				}

				var s scoreJSON
				if err := json.Unmarshal(data, &s); err != nil {
					result.Skipped++
					return nil
				}
				if s.URL == "" {
					result.Skipped++
					return nil
				}

				if dryRun {
					fmt.Fprintf(os.Stderr, "dry-run: url=%s score=%d profile=%s\n", s.URL, s.Score, s.Profile)
					return nil
				}

				_, inserted, err := q.ScoreByURL(s.URL, s.Score, s.Verdict, s.Tags, s.Profile, s.Slug, "", "")
				if err != nil {
					result.Skipped++
					return nil
				}
				if inserted {
					result.Inserted++
				} else {
					result.Matched++
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("walk %s: %w", dir, err)
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		},
	}

	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be done without writing")

	return cmd
}
