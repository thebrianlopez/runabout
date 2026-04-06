package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func searchCmd() *cobra.Command {
	var (
		queueDB string
		profile string
		limit   int
	)

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search over scored queue items",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			queueDB = resolveQueueDB(queueDB)
			q, err := NewQueue(queueDB, false)
			if err != nil {
				return fmt.Errorf("opening queue: %w", err)
			}
			defer q.Close()

			items, err := q.SearchFTS5(args[0], profile, limit)
			if err != nil {
				return fmt.Errorf("search: %w", err)
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(items)
		},
	}

	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().StringVar(&profile, "profile", "", "filter by profile (empty = all)")
	cmd.Flags().IntVar(&limit, "limit", 10, "max results")

	return cmd
}
