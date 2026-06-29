package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"log/slog"

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
	var queueDB, dest string
	var overwrite bool
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Create a SQLite backup",
		RunE: func(cmd *cobra.Command, args []string) error {
			queueDB = resolveQueueDB(queueDB)
			if dest == "" {
				return fmt.Errorf("--dest is required")
			}
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
		},
	}
	cmd.Flags().StringVar(&queueDB, "queue-db", "", "path to SQLite queue database (or LINKARI_QUEUE_DB)")
	cmd.Flags().StringVar(&dest, "dest", "", "destination backup path")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "allow replacing an existing backup")
	return cmd
}

func dbRestoreCmd() *cobra.Command {
	var queueDB, src string
	var force bool
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a SQLite backup",
		RunE: func(cmd *cobra.Command, args []string) error {
			queueDB = resolveQueueDB(queueDB)
			if src == "" {
				return fmt.Errorf("--src is required")
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
