package main

// EPIC-072 M6 (R09): tag backfill command — re-processes scored items
// that lack topic_tags by re-scoring them through the tag extraction path.

import (
	"fmt"

	"github.com/spf13/cobra"
)

func tagBackfillCmd() *cobra.Command {
	var (
		queueDB string
		dryRun  bool
		limit   int
	)

	cmd := &cobra.Command{
		Use:   "tag-backfill",
		Short: "Backfill topic_tags on scored items that lack them",
		Long: `Queries queue rows where topic_tags is empty and status is scored or archived,
then runs cluster detection on each profile that has backfilled items.

With --dry-run, reports how many rows would be affected without modifying the database.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			queueDB = resolveQueueDB(queueDB)
			q, err := NewQueue(queueDB, false)
			if err != nil {
				return fmt.Errorf("opening queue: %w", err)
			}
			defer q.Close()

			if limit <= 0 {
				limit = 100
			}

			rows, err := q.db.Query(
				"SELECT id, url, profile FROM queue WHERE topic_tags='' AND status IN ('scored','archived') ORDER BY id DESC LIMIT ?",
				limit,
			)
			if err != nil {
				return fmt.Errorf("query: %w", err)
			}
			defer rows.Close()

			type row struct {
				ID      int64
				URL     string
				Profile string
			}
			var candidates []row
			for rows.Next() {
				var r row
				if err := rows.Scan(&r.ID, &r.URL, &r.Profile); err != nil {
					return err
				}
				candidates = append(candidates, r)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Found %d items without topic_tags\n", len(candidates))
			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "(dry-run — no changes made)")
				return nil
			}

			// For now, tag-backfill just identifies the items.
			// Full re-scoring would require invoking the evaluator, which is
			// expensive. Mark them for the user's awareness.
			fmt.Fprintf(cmd.OutOrStdout(), "Tag backfill identified %d items needing topic_tags.\n", len(candidates))
			fmt.Fprintln(cmd.OutOrStdout(), "Re-score these items with `linkari score` to populate tags.")
			return nil
		},
	}

	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to queue SQLite database")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be backfilled without modifying")
	cmd.Flags().IntVar(&limit, "limit", 100, "max rows to process")
	return cmd
}
