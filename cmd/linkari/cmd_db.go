package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

type dbBackupMeta struct {
	SourceDB    string    `json:"source_db"`
	BackupPath  string    `json:"backup_path"`
	CreatedAt   time.Time `json:"created_at"`
	QueueDBSize int64     `json:"queue_db_size_bytes"`
}

func dbCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "db", Short: "SQLite backup and restore utilities"}
	cmd.AddCommand(dbBackupCmd())
	cmd.AddCommand(dbRestoreCmd())
	return cmd
}

func dbBackupCmd() *cobra.Command {
	var queueDB, dest, intervalStr string
	var overwrite bool
	cmd := &cobra.Command{
		Use:   "backup <path>",
		Short: "Create a SQLite backup (once or recurring with --interval)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			queueDB = resolveQueueDB(queueDB)
			if len(args) == 1 {
				dest = args[0]
			}
			if dest == "" {
				return fmt.Errorf("backup path is required")
			}

			var interval time.Duration
			if intervalStr != "" {
				var err error
				interval, err = time.ParseDuration(intervalStr)
				if err != nil {
					return fmt.Errorf("--interval: %w", err)
				}
			}

			// Watch mode requires --overwrite (cycles replace the dest)
			if interval > 0 && !overwrite {
				return fmt.Errorf("watch mode (--interval > 0) requires --overwrite (each cycle replaces the snapshot)")
			}

			// One-shot backup if interval <= 0
			if interval <= 0 {
				if !overwrite {
					if _, err := os.Stat(dest); err == nil {
						return fmt.Errorf("backup destination exists: %s", dest)
					}
				}
				q, err := NewQueue(queueDB, false)
				if err != nil {
					return fmt.Errorf("opening queue: %w", err)
				}
				defer q.Close()
				return doBackupOnce(q, queueDB, dest)
			}

			// Watch mode: recurring snapshots
			q, err := NewQueue(queueDB, false)
			if err != nil {
				return fmt.Errorf("opening queue: %w", err)
			}
			defer q.Close()
			return runBackupWatch(cmd.Context(), q, dest, interval)
		},
	}
	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().StringVar(&dest, "dest", "", "destination backup path")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "allow replacing an existing backup (required for --interval)")
	cmd.Flags().StringVar(&intervalStr, "interval", "", "watch mode: backup every <duration> (e.g. 6h, 30m; requires --overwrite)")
	return cmd
}

// doBackupOnce performs a single backup cycle.
func doBackupOnce(q *Queue, queueDB, dest string) error {
	start := time.Now()
	if err := q.Snapshot(dest); err != nil {
		return err
	}
	st, _ := os.Stat(queueDB)
	meta := dbBackupMeta{SourceDB: queueDB, BackupPath: dest, CreatedAt: time.Now().UTC()}
	if st != nil {
		meta.QueueDBSize = st.Size()
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal backup metadata: %w", err)
	}
	sidecar := dest + ".backup-meta.json"
	if err := os.WriteFile(sidecar, b, 0o600); err != nil {
		return fmt.Errorf("write backup metadata: %w", err)
	}
	slog.Info("db_backup_complete", "source_db", queueDB, "dest", dest, "bytes", meta.QueueDBSize, "duration_ms", time.Since(start).Milliseconds())
	return nil
}

func dbRestoreCmd() *cobra.Command {
	var queueDB, src string
	var force bool
	cmd := &cobra.Command{
		Use:   "restore <path>",
		Short: "Restore a SQLite backup",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			queueDB = resolveQueueDB(queueDB)
			if len(args) == 1 {
				src = args[0]
			}
			if src == "" {
				return fmt.Errorf("restore path is required")
			}
			if _, err := os.Stat(src); err != nil {
				return fmt.Errorf("stat source backup: %w", err)
			}
			walPath := src + "-wal"
			if st, err := os.Stat(walPath); err == nil && st.Size() > 0 && !force {
				return fmt.Errorf("source WAL is non-empty: %s (use --force to override)", walPath)
			}
			if _, err := NewQueue(src, false); err != nil {
				return fmt.Errorf("integrity check source: %w", err)
			}
			dstDir := filepath.Dir(queueDB)
			if err := os.MkdirAll(dstDir, 0o700); err != nil {
				return fmt.Errorf("create queue dir: %w", err)
			}
			tmp, err := os.CreateTemp(dstDir, "queue.db.restore-*")
			if err != nil {
				return fmt.Errorf("create temp restore: %w", err)
			}
			tmpPath := tmp.Name()
			tmp.Close()
			if err := copyFileAtomic(src, tmpPath); err != nil {
				os.Remove(tmpPath)
				return err
			}
			if err := renameFile(tmpPath, queueDB); err != nil {
				os.Remove(tmpPath)
				return fmt.Errorf("atomic restore rename: %w", err)
			}
			slog.Info("db_restore_complete", "source", src, "dest", queueDB)
			return nil
		},
	}
	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().StringVar(&src, "src", "", "source backup path")
	cmd.Flags().BoolVar(&force, "force", false, "allow restore with a non-empty WAL")
	return cmd
}

// runBackupWatch runs an in-process ticker loop, snapshotting every interval until canceled.
//
// Returns context.DeadlineExceeded if the context timeout expires, or nil on graceful shutdown.
// Failed cycles log an error but do not terminate the loop - the prior snapshot is retained.
func runBackupWatch(ctx context.Context, q *Queue, dest string, interval time.Duration) error {
	// Ensure dest directory exists and is writable (fail fast on startup)
	destDir := filepath.Dir(dest)
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		slog.Error("sidecar_backup_pv_missing", "dest_dir", destDir, "error", err)
		return fmt.Errorf("sidecar_backup_pv_missing: %w", err)
	}

	// Test writeability
	testFile := filepath.Join(destDir, ".linkari-backup-test")
	if err := os.WriteFile(testFile, []byte("test"), 0o600); err != nil {
		slog.Error("sidecar_backup_pv_missing", "error", err)
		os.Remove(testFile)
		return fmt.Errorf("sidecar_backup_pv_missing: dest not writable: %w", err)
	}
	os.Remove(testFile)

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigChan)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	cycle := 0
	for {
		cycle++
		select {
		case <-ctx.Done():
			slog.Info("sidecar_backup_shutdown", "cycles_completed", cycle-1)
			return ctx.Err()

		case <-sigChan:
			slog.Info("sidecar_backup_shutdown", "cycles_completed", cycle-1)
			return nil

		case <-ticker.C:
			start := time.Now()
			if err := q.Snapshot(dest); err != nil {
				slog.Error("sidecar_backup_failed", "error", err, "cycle", cycle)
				continue // Non-fatal: retain prior snapshot, continue loop
			}

			// Write metadata
			st, _ := os.Stat(dest)
			meta := dbBackupMeta{
				SourceDB:    "queue.db",
				BackupPath:  dest,
				CreatedAt:   time.Now().UTC(),
				QueueDBSize: 0,
			}
			if st != nil {
				meta.QueueDBSize = st.Size()
			}
			b, err := json.MarshalIndent(meta, "", "  ")
			if err != nil {
				slog.Error("sidecar_backup_failed", "error", fmt.Sprintf("marshal meta: %v", err), "cycle", cycle)
				continue
			}

			metaPath := dest + ".backup-meta.json"
			if err := os.WriteFile(metaPath, b, 0o600); err != nil {
				slog.Error("sidecar_backup_failed", "error", fmt.Sprintf("write meta: %v", err), "cycle", cycle)
				continue
			}

			slog.Info("sidecar_backup_cycle", "dest", dest, "bytes", meta.QueueDBSize, "duration_ms", time.Since(start).Milliseconds(), "cycle", cycle)
		}
	}
}

func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	return nil
}

var renameFile = os.Rename
