package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func scoreCmd() *cobra.Command {
	var (
		queueDB string
		url     string
		score   int
		verdict string
		profile string
		slug    string
		tags    string
	)

	cmd := &cobra.Command{
		Use:   "score",
		Short: "Write score and verdict to the queue database",
		Long: `Persist a score and verdict for a URL in the Linkari queue.

If the URL exists in the queue with status=relayed (from an Android share),
the existing row is updated. Otherwise a new row is inserted (CLI-originated).

Already-scored URLs are returned without modification (idempotent).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if score < 0 || score > 100 {
				return fmt.Errorf("--score must be 0-100, got %d", score)
			}

			queueDB = resolveQueueDB(queueDB)
			q, err := NewQueue(queueDB, false)
			if err != nil {
				return fmt.Errorf("opening queue: %w", err)
			}
			defer q.Close()

			item, _, err := q.ScoreByURL(url, score, verdict, tags, profile, slug)
			if err != nil {
				return fmt.Errorf("score: %w", err)
			}

			// Auto-archive if score meets profile threshold.
			threshold := archiveThreshold(item.Profile)
			if threshold >= 0 && item.Score != nil && *item.Score >= threshold {
				if archErr := q.Archive(item.ID); archErr == nil {
					item.Status = "archived"
				}
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(item)
		},
	}

	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().StringVar(&url, "url", "", "URL to score (required)")
	cmd.Flags().IntVar(&score, "score", 0, "score 0-100")
	cmd.Flags().StringVar(&verdict, "verdict", "", "verdict text")
	cmd.Flags().StringVar(&profile, "profile", "eng", "scoring profile")
	cmd.Flags().StringVar(&slug, "slug", "", "workspace slug")
	cmd.Flags().StringVar(&tags, "tags", "", "comma-separated tags")
	cmd.MarkFlagRequired("url")
	cmd.MarkFlagRequired("score")

	return cmd
}
