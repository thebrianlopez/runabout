package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

func digestCmd() *cobra.Command {
	var (
		queueDB string
		profile string
		limit   int
		since   string
	)

	cmd := &cobra.Command{
		Use:   "digest",
		Short: "List recent scored items",
		RunE: func(cmd *cobra.Command, args []string) error {
			queueDB = resolveQueueDB(queueDB)
			q, err := NewQueue(queueDB, false)
			if err != nil {
				return fmt.Errorf("opening queue: %w", err)
			}
			defer q.Close()

			d, err := time.ParseDuration(since)
			if err != nil {
				return fmt.Errorf("--since must be a Go duration (e.g. 24h, 168h): %w", err)
			}
			sinceTime := time.Now().Add(-d)

			items, err := q.RecentScored(sinceTime, limit)
			if err != nil {
				return fmt.Errorf("digest: %w", err)
			}

			if profile != "" {
				filtered := items[:0]
				for _, it := range items {
					if it.Profile == profile {
						filtered = append(filtered, it)
					}
				}
				items = filtered
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(items)
		},
	}

	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().StringVar(&profile, "profile", "", "filter by profile")
	cmd.Flags().IntVar(&limit, "limit", 20, "max results")
	cmd.Flags().StringVar(&since, "since", "24h", "time window as Go duration (e.g. 48h, 168h)")

	return cmd
}
