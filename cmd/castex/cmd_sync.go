package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// S3Client is the interface for remote object store operations.
type S3Client interface {
	ListObjects(ctx context.Context, prefix string) ([]ObjectMeta, error)
	GetObject(ctx context.Context, key string) ([]byte, error)
	PutObject(ctx context.Context, key string, data []byte) error
}

// ObjectMeta describes one remote object.
type ObjectMeta struct {
	Key  string
	ETag string
	Size int64
}

// SyncConfig holds flags for the sync command.
type SyncConfig struct {
	Remote          string // s3://bucket/prefix
	DryRun          bool
	Timeout         time.Duration
	LocalDir        string // ~/.automation-metrics/events
	ConflictLogPath string
}

// SyncResult is the output of a sync run.
type SyncResult struct {
	Uploaded   int
	Downloaded int
	Conflicts  int
	Duration   time.Duration
}

// SyncConflictEntry is one row in sync-conflicts.jsonl.
type SyncConflictEntry struct {
	DedupKey      string          `json:"dedup_key"`
	SessionID     string          `json:"session_id"`
	EventID       string          `json:"event_id"`
	LocalVersion  conflictVersion `json:"local_version"`
	RemoteVersion conflictVersion `json:"remote_version"`
	Resolution    string          `json:"resolution"` // "local_wins" or "remote_wins"
	ConflictAt    string          `json:"conflict_at"`
}

type conflictVersion struct {
	CreatedAt string `json:"created_at"`
	Source    string `json:"source"`
}

// syncEvent holds the dedup key fields from one JSONL line.
type syncEvent struct {
	SessionID string `json:"session_id"`
	EventID   string `json:"event_id"`
	CreatedAt string `json:"created_at"`
	raw       []byte
}

func newSyncCmd() *cobra.Command {
	var cfg SyncConfig

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Merge local automation-metrics events with a remote S3 store",
		RunE: func(cmd *cobra.Command, args []string) error {
			if cfg.Remote == "" {
				cfg.Remote = os.Getenv("CASTEX_SYNC_REMOTE")
			}
			if cfg.Remote == "" {
				return fmt.Errorf("[E404] remote_not_configured: set CASTEX_SYNC_REMOTE or use --remote flag")
			}
			home, _ := os.UserHomeDir()
			if cfg.LocalDir == "" {
				cfg.LocalDir = filepath.Join(home, ".automation-metrics", "events")
			}
			if cfg.ConflictLogPath == "" {
				cfg.ConflictLogPath = filepath.Join(home, ".castex", "sync-conflicts.jsonl")
			}
			if cfg.Timeout == 0 {
				cfg.Timeout = 30 * time.Second
			}

			client, err := newS3ClientFromRemote(cfg.Remote)
			if err != nil {
				return fmt.Errorf("[E401] remote_unreachable: %w", err)
			}
			result, err := RunSync(cmd, cfg, client)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "sync complete: uploaded=%d downloaded=%d conflicts=%d duration=%s\n",
				result.Uploaded, result.Downloaded, result.Conflicts, result.Duration.Round(time.Millisecond))
			return nil
		},
	}

	home, _ := os.UserHomeDir()
	cmd.Flags().StringVar(&cfg.Remote, "remote", "", "remote S3 URI (e.g. s3://bucket/events/); overrides CASTEX_SYNC_REMOTE")
	cmd.Flags().BoolVar(&cfg.DryRun, "dry-run", false, "print sync plan without transferring")
	cmd.Flags().DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "sync timeout deadline")
	cmd.Flags().StringVar(&cfg.LocalDir, "local-dir", filepath.Join(home, ".automation-metrics", "events"), "local events directory")
	return cmd
}

// RunSync is the testable entry point for the sync command.
func RunSync(cmd *cobra.Command, cfg SyncConfig, client S3Client) (SyncResult, error) {
	start := time.Now()

	if _, err := os.Stat(cfg.LocalDir); os.IsNotExist(err) {
		return SyncResult{}, fmt.Errorf("[E405] events_dir_missing: %s", cfg.LocalDir)
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	type syncResult struct {
		result SyncResult
		err    error
	}
	ch := make(chan syncResult, 1)
	go func() {
		r, err := doSync(ctx, cmd, cfg, client)
		ch <- syncResult{r, err}
	}()

	select {
	case <-ctx.Done():
		return SyncResult{}, fmt.Errorf("[E402] sync_timeout: exceeded %s deadline; increase --timeout", cfg.Timeout)
	case r := <-ch:
		if r.err != nil {
			return SyncResult{}, r.err
		}
		r.result.Duration = time.Since(start)
		return r.result, nil
	}
}

func doSync(ctx context.Context, cmd *cobra.Command, cfg SyncConfig, client S3Client) (SyncResult, error) {
	var result SyncResult

	// Build local index.
	localIndex, err := buildLocalIndex(cfg.LocalDir)
	if err != nil {
		return result, err
	}

	// Build remote index.
	remoteObjects, err := client.ListObjects(ctx, "")
	if err != nil {
		return result, fmt.Errorf("[E401] remote_unreachable: %w", err)
	}
	remoteIndex := map[string]bool{}
	for _, obj := range remoteObjects {
		remoteIndex[obj.Key] = true
	}

	// Determine upload plan: local files not on remote.
	var toUpload []string
	for localFile := range localIndex {
		if !remoteIndex[localFile] {
			toUpload = append(toUpload, localFile)
		}
	}

	// Determine download plan: remote files not local.
	var toDownload []string
	for _, obj := range remoteObjects {
		if _, ok := localIndex[obj.Key]; !ok {
			toDownload = append(toDownload, obj.Key)
		}
	}

	// Handle conflicts: files present in both - check dedup keys.
	conflicts, conflictEntries := detectConflicts(ctx, cfg, client, localIndex, remoteObjects)
	result.Conflicts = conflicts

	if cfg.DryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "sync plan: %d to upload, %d to download, %d conflicts\n",
			len(toUpload), len(toDownload), conflicts)
		return result, nil
	}

	// Upload local-only files.
	for _, localFile := range toUpload {
		data, err := os.ReadFile(filepath.Join(cfg.LocalDir, localFile))
		if err != nil {
			continue
		}
		if err := client.PutObject(ctx, localFile, data); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "[E406] upload_failed: %s: %v\n", localFile, err)
			continue
		}
		result.Uploaded++
	}

	// Download remote-only files (append-only: never delete local).
	for _, remoteKey := range toDownload {
		data, err := client.GetObject(ctx, remoteKey)
		if err != nil {
			continue
		}
		localPath := filepath.Join(cfg.LocalDir, remoteKey)
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(localPath, data, 0o644); err != nil {
			continue
		}
		result.Downloaded++
	}

	// Append conflicts to sync-conflicts.jsonl.
	if len(conflictEntries) > 0 {
		if err := appendConflicts(cfg.ConflictLogPath, conflictEntries); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "conflict log write warning: %v\n", err)
		}
	}

	return result, nil
}

// buildLocalIndex returns a map of relative filename → dedupKey set for events in that file.
func buildLocalIndex(dir string) (map[string]map[string]syncEvent, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	index := map[string]map[string]syncEvent{}
	for _, f := range files {
		base := filepath.Base(f)
		events, err := readSyncEvents(f)
		if err != nil {
			continue
		}
		index[base] = events
	}
	return index, nil
}

func readSyncEvents(path string) (map[string]syncEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	events := map[string]syncEvent{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev syncEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		ev.raw = append([]byte(nil), line...)
		key := dedupKey(ev.SessionID, ev.EventID)
		events[key] = ev
	}
	return events, scanner.Err()
}

// dedupKey is SHA1(session_id + ":" + event_id).
func dedupKey(sessionID, eventID string) string {
	h := sha1.Sum([]byte(sessionID + ":" + eventID))
	return fmt.Sprintf("%x", h)
}

func detectConflicts(ctx context.Context, cfg SyncConfig, client S3Client,
	localIndex map[string]map[string]syncEvent, remoteObjects []ObjectMeta,
) (int, []SyncConflictEntry) {
	var count int
	var entries []SyncConflictEntry
	now := time.Now().UTC().Format("20060102T150405Z")

	for _, obj := range remoteObjects {
		localEvents, ok := localIndex[obj.Key]
		if !ok {
			continue // remote-only: handled as download
		}
		// Get remote content and compare dedup keys.
		remoteData, err := client.GetObject(ctx, obj.Key)
		if err != nil {
			continue
		}
		remoteEvents := parseRemoteEvents(remoteData)
		for key, remoteEv := range remoteEvents {
			localEv, exists := localEvents[key]
			if !exists {
				continue
			}
			// Same key in both - check for content conflict.
			if string(localEv.raw) == string(remoteEv.raw) {
				continue // identical - no conflict
			}
			// Conflict: pick winner by earlier created_at.
			resolution := "local_wins"
			if remoteEv.CreatedAt != "" && localEv.CreatedAt != "" && remoteEv.CreatedAt < localEv.CreatedAt {
				resolution = "remote_wins"
			}
			count++
			entries = append(entries, SyncConflictEntry{
				DedupKey:  key,
				SessionID: localEv.SessionID,
				EventID:   localEv.EventID,
				LocalVersion: conflictVersion{
					CreatedAt: localEv.CreatedAt,
					Source:    "local",
				},
				RemoteVersion: conflictVersion{
					CreatedAt: remoteEv.CreatedAt,
					Source:    "remote",
				},
				Resolution: resolution,
				ConflictAt: now,
			})
		}
	}
	return count, entries
}

func parseRemoteEvents(data []byte) map[string]syncEvent {
	events := map[string]syncEvent{}
	for _, line := range splitLines(data) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev syncEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		ev.raw = append([]byte(nil), line...)
		key := dedupKey(ev.SessionID, ev.EventID)
		events[key] = ev
	}
	return events
}

func appendConflicts(path string, entries []SyncConflictEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		fmt.Fprintf(f, "%s\n", b)
	}
	return nil
}

// --- In-memory S3 stub for testing ---

// MemS3Client is an in-memory S3 stub for tests.
type MemS3Client struct {
	objects map[string][]byte
	Err     error // if non-nil, all operations return this error
}

func NewMemS3Client() *MemS3Client {
	return &MemS3Client{objects: map[string][]byte{}}
}

func (m *MemS3Client) Put(key string, data []byte) {
	m.objects[key] = append([]byte(nil), data...)
}

func (m *MemS3Client) ListObjects(_ context.Context, _ string) ([]ObjectMeta, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	var out []ObjectMeta
	for k, v := range m.objects {
		out = append(out, ObjectMeta{Key: k, Size: int64(len(v))})
	}
	return out, nil
}

func (m *MemS3Client) GetObject(_ context.Context, key string) ([]byte, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	data, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	return append([]byte(nil), data...), nil
}

func (m *MemS3Client) PutObject(_ context.Context, key string, data []byte) error {
	if m.Err != nil {
		return m.Err
	}
	m.objects[key] = append([]byte(nil), data...)
	return nil
}

// newS3ClientFromRemote parses the remote URI and returns a real S3 client.
// Currently returns an error indicating the real AWS SDK client is not wired.
// Use MemS3Client directly in tests.
func newS3ClientFromRemote(remote string) (S3Client, error) {
	if !strings.HasPrefix(remote, "s3://") {
		return nil, fmt.Errorf("unsupported remote scheme: %s (expected s3://)", remote)
	}
	// TODO(castex-sync): wire real aws-sdk-go-v2/service/s3 client here
	return nil, fmt.Errorf("real S3 client not yet wired; use MemS3Client for integration tests")
}
