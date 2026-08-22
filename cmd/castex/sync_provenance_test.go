package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// provTestClient wraps MemS3Client to expose per-key ETags and to count
// GetObject calls, so tests can assert that a rejected object is never fetched.
type provTestClient struct {
	*MemS3Client
	etags map[string]string
	gets  []string
}

func newProvTestClient() *provTestClient {
	return &provTestClient{MemS3Client: NewMemS3Client(), etags: map[string]string{}}
}

func (c *provTestClient) PutWithETag(key string, data []byte, etag string) {
	c.Put(key, data)
	c.etags[key] = etag
}

func (c *provTestClient) ListObjects(ctx context.Context, prefix string) ([]ObjectMeta, error) {
	objs, err := c.MemS3Client.ListObjects(ctx, prefix)
	if err != nil {
		return nil, err
	}
	for i := range objs {
		objs[i].ETag = c.etags[objs[i].Key]
	}
	return objs, nil
}

func (c *provTestClient) GetObject(ctx context.Context, key string) ([]byte, error) {
	c.gets = append(c.gets, key)
	return c.MemS3Client.GetObject(ctx, key)
}

// runProvSync executes one sync with a discardable cobra command.
func runProvSync(t *testing.T, cfg SyncConfig, client S3Client) SyncResult {
	t.Helper()
	cmd := newSyncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	result, err := RunSync(cmd, cfg, client)
	if err != nil {
		t.Fatalf("RunSync error: %v", err)
	}
	return result
}

// readProvLines returns the parsed JSON objects of a local events file.
func readProvLines(t *testing.T, path string) []map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []map[string]json.RawMessage
	for _, line := range splitLines(data) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("line is not a JSON object: %s", line)
		}
		out = append(out, m)
	}
	return out
}

// PROV-CT1: downloaded events carry a correct, complete provenance envelope.
func TestSyncProvenance_CT1_DownloadedEventsTagged(t *testing.T) {
	dir := t.TempDir()
	client := newProvTestClient()
	client.PutWithETag("2026-05-01.jsonl",
		[]byte(`{"session_id":"sr1","event_id":"er1","created_at":"20260501T100000Z"}`+"\n"),
		"etag-abc")

	cfg := newSyncCfg(t, dir)
	cfg.Remote = "s3://bucket/events"
	cfg.Peer = "peer-alpha"

	result := runProvSync(t, cfg, client)
	if result.Downloaded != 1 {
		t.Fatalf("expected 1 download, got %d", result.Downloaded)
	}

	lines := readProvLines(t, filepath.Join(dir, "2026-05-01.jsonl"))
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	rawEnv, ok := lines[0][provenanceField]
	if !ok {
		t.Fatalf("downloaded event has no %s envelope: %v", provenanceField, lines[0])
	}
	var info ProvenanceInfo
	if err := json.Unmarshal(rawEnv, &info); err != nil {
		t.Fatalf("envelope not parseable: %v", err)
	}
	if info.Source != ProvenanceRemote {
		t.Errorf("source: want %q, got %q", ProvenanceRemote, info.Source)
	}
	if info.Remote != "s3://bucket/events" {
		t.Errorf("remote: want s3://bucket/events, got %q", info.Remote)
	}
	if info.Peer != "peer-alpha" {
		t.Errorf("peer: want peer-alpha, got %q", info.Peer)
	}
	if info.ObjectKey != "2026-05-01.jsonl" {
		t.Errorf("object_key: want 2026-05-01.jsonl, got %q", info.ObjectKey)
	}
	if info.ETag != "etag-abc" {
		t.Errorf("etag: want etag-abc, got %q", info.ETag)
	}
	if info.SyncedAt == "" {
		t.Error("synced_at is empty")
	}

	// The original payload must survive tagging intact.
	var sid string
	if err := json.Unmarshal(lines[0]["session_id"], &sid); err != nil || sid != "sr1" {
		t.Errorf("payload corrupted: session_id=%q err=%v", sid, err)
	}
}

// PROV-CT2: locally produced events are left byte-for-byte untouched, and read
// back as local. Absence of the envelope is the local marker.
func TestSyncProvenance_CT2_LocalEventsUntouched(t *testing.T) {
	dir := t.TempDir()
	localLine := `{"session_id":"sl1","event_id":"el1","created_at":"20260501T100000Z"}` + "\n"
	localPath := filepath.Join(dir, "2026-05-02.jsonl")
	if err := os.WriteFile(localPath, []byte(localLine), 0o644); err != nil {
		t.Fatal(err)
	}

	client := newProvTestClient()
	client.PutWithETag("2026-05-03.jsonl",
		[]byte(`{"session_id":"sr2","event_id":"er2"}`+"\n"), "etag-x")

	cfg := newSyncCfg(t, dir)
	cfg.Remote = "s3://bucket/events"
	runProvSync(t, cfg, client)

	after, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != localLine {
		t.Errorf("local file was rewritten:\n want %q\n got  %q", localLine, string(after))
	}
	if got := provenanceOf([]byte(localLine)).Source; got != ProvenanceLocal {
		t.Errorf("untagged local event: want source %q, got %q", ProvenanceLocal, got)
	}
	// And the downloaded neighbour is distinguishable from it.
	dl := readProvLines(t, filepath.Join(dir, "2026-05-03.jsonl"))
	if _, ok := dl[0][provenanceField]; !ok {
		t.Error("downloaded event is indistinguishable from a local one")
	}
}

// PROV-CT3: the default filter is permissive, explicitly.
//
// This test fails if AllowAllProvenance ever starts rejecting, or if
// resolveProvenanceFilter stops defaulting to it. Tightening the default must
// be a deliberate change that breaks this test, not a silent one.
func TestSyncProvenance_CT3_DefaultFilterIsPermissive(t *testing.T) {
	decision := AllowAllProvenance(ProvenanceInfo{Source: ProvenanceRemote, Remote: "s3://anywhere"})
	if !decision.Allow {
		t.Fatalf("AllowAllProvenance must allow by default, got Allow=false reason=%q", decision.Reason)
	}

	resolved := resolveProvenanceFilter(nil)
	if resolved == nil {
		t.Fatal("resolveProvenanceFilter(nil) returned nil; callers would panic")
	}
	if d := resolved(ProvenanceInfo{Source: ProvenanceRemote}); !d.Allow {
		t.Errorf("default resolved filter must allow, got Allow=false reason=%q", d.Reason)
	}

	// End to end: a config that sets no filter downloads normally.
	dir := t.TempDir()
	client := newProvTestClient()
	client.PutWithETag("2026-05-04.jsonl", []byte(`{"session_id":"s","event_id":"e"}`+"\n"), "")
	cfg := newSyncCfg(t, dir)
	if cfg.ProvenanceFilter != nil {
		t.Fatal("newSyncCfg unexpectedly set a filter; this test needs the nil default")
	}
	result := runProvSync(t, cfg, client)
	if result.Downloaded != 1 || result.Rejected != 0 {
		t.Errorf("permissive default: want down=1 rejected=0, got down=%d rejected=%d",
			result.Downloaded, result.Rejected)
	}
}

// PROV-CT4: the filter hook is actually consulted, once per candidate object,
// with fully populated provenance.
//
// This test fails if the hook were skipped entirely on the download path.
func TestSyncProvenance_CT4_FilterHookInvoked(t *testing.T) {
	dir := t.TempDir()
	client := newProvTestClient()
	client.PutWithETag("2026-05-05.jsonl", []byte(`{"session_id":"s1","event_id":"e1"}`+"\n"), "tag1")
	client.PutWithETag("2026-05-06.jsonl", []byte(`{"session_id":"s2","event_id":"e2"}`+"\n"), "tag2")

	var seen []ProvenanceInfo
	cfg := newSyncCfg(t, dir)
	cfg.Remote = "s3://bucket/events"
	cfg.Peer = "peer-beta"
	cfg.ProvenanceFilter = func(info ProvenanceInfo) ProvenanceDecision {
		seen = append(seen, info)
		return ProvenanceDecision{Allow: true, Reason: "test_allow"}
	}

	result := runProvSync(t, cfg, client)
	if len(seen) != 2 {
		t.Fatalf("filter must be called once per candidate object: want 2 calls, got %d", len(seen))
	}
	if result.Downloaded != 2 {
		t.Errorf("want 2 downloads, got %d", result.Downloaded)
	}
	for _, info := range seen {
		if info.Source != ProvenanceRemote {
			t.Errorf("filter saw source %q, want %q", info.Source, ProvenanceRemote)
		}
		if info.Remote != "s3://bucket/events" || info.Peer != "peer-beta" {
			t.Errorf("filter saw incomplete origin: %+v", info)
		}
		if info.ObjectKey == "" || info.ETag == "" {
			t.Errorf("filter saw empty object identity: %+v", info)
		}
	}
}

// PROV-CT5: a rejecting filter blocks the write, is counted, and prevents the
// fetch entirely.
func TestSyncProvenance_CT5_RejectingFilterBlocksDownload(t *testing.T) {
	dir := t.TempDir()
	client := newProvTestClient()
	client.PutWithETag("2026-05-07.jsonl", []byte(`{"session_id":"s1","event_id":"e1"}`+"\n"), "tag1")

	cfg := newSyncCfg(t, dir)
	cfg.ProvenanceFilter = func(ProvenanceInfo) ProvenanceDecision {
		return ProvenanceDecision{Allow: false, Reason: "untrusted_peer"}
	}

	cmd := newSyncCmd()
	var stderr bytes.Buffer
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&stderr)
	result, err := RunSync(cmd, cfg, client)
	if err != nil {
		t.Fatalf("RunSync error: %v", err)
	}

	if result.Downloaded != 0 {
		t.Errorf("rejected object must not be downloaded, got %d", result.Downloaded)
	}
	if result.Rejected != 1 {
		t.Errorf("want Rejected=1, got %d", result.Rejected)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-05-07.jsonl")); !os.IsNotExist(err) {
		t.Error("rejected object was written to disk")
	}
	if len(client.gets) != 0 {
		t.Errorf("rejected object must not be fetched, got GetObject calls: %v", client.gets)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("provenance_rejected")) {
		t.Errorf("rejection not reported on stderr, got: %q", stderr.String())
	}
}

// PROV-CT6: a remote cannot forge a local-looking provenance tag. Whatever
// envelope the remote object carries is discarded and replaced at ingest.
func TestSyncProvenance_CT6_RemoteCannotForgeLocalTag(t *testing.T) {
	dir := t.TempDir()
	client := newProvTestClient()
	forged := `{"session_id":"sf","event_id":"ef","` + provenanceField +
		`":{"source":"local","peer":"trusted-insider"}}` + "\n"
	client.PutWithETag("2026-05-08.jsonl", []byte(forged), "etag-f")

	cfg := newSyncCfg(t, dir)
	cfg.Remote = "s3://bucket/events"
	cfg.Peer = "peer-real"
	runProvSync(t, cfg, client)

	lines := readProvLines(t, filepath.Join(dir, "2026-05-08.jsonl"))
	var info ProvenanceInfo
	if err := json.Unmarshal(lines[0][provenanceField], &info); err != nil {
		t.Fatalf("envelope not parseable: %v", err)
	}
	if info.Source != ProvenanceRemote {
		t.Errorf("forged local tag survived ingest: source=%q", info.Source)
	}
	if info.Peer != "peer-real" {
		t.Errorf("forged peer survived ingest: peer=%q", info.Peer)
	}
}

// PROV-RG1: re-syncing after a download is a zero-op and produces no conflicts.
//
// Regression guard for the tagging rewrite: the local copy now differs
// byte-wise from the remote copy, so a byte-exact comparison would report every
// previously downloaded event as a fresh conflict on every subsequent sync.
func TestSyncProvenance_RG1_ReSyncAfterDownloadIsClean(t *testing.T) {
	dir := t.TempDir()
	client := newProvTestClient()
	client.PutWithETag("2026-05-09.jsonl",
		[]byte(`{"session_id":"s9","event_id":"e9","created_at":"20260509T100000Z"}`+"\n"), "etag-9")

	cfg := newSyncCfg(t, dir)
	cfg.Remote = "s3://bucket/events"

	first := runProvSync(t, cfg, client)
	if first.Downloaded != 1 {
		t.Fatalf("first sync: want 1 download, got %d", first.Downloaded)
	}

	second := runProvSync(t, cfg, client)
	if second.Downloaded != 0 || second.Uploaded != 0 {
		t.Errorf("re-sync must be a zero-op, got down=%d up=%d", second.Downloaded, second.Uploaded)
	}
	if second.Conflicts != 0 {
		t.Errorf("provenance tagging must not manufacture conflicts, got %d", second.Conflicts)
	}
}

// PROV-CT7: tagEventLines drops only non-object lines and tags everything else.
func TestSyncProvenance_CT7_TagEventLinesKeepsAndDrops(t *testing.T) {
	data := []byte(`{"a":1}` + "\n" + `[1,2,3]` + "\n" + "\n" + `not json` + "\n" + `{"b":2}` + "\n")
	tagged, kept, dropped := tagEventLines(data, ProvenanceInfo{Source: ProvenanceRemote})
	if kept != 2 {
		t.Errorf("want 2 kept, got %d", kept)
	}
	if dropped != 2 {
		t.Errorf("want 2 dropped (array + garbage; blank lines are not counted), got %d", dropped)
	}
	for _, line := range splitLines(tagged) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("emitted non-object line: %s", line)
		}
		if _, ok := m[provenanceField]; !ok {
			t.Errorf("emitted line without provenance envelope: %s", line)
		}
	}
}

// PROV-CT8: provenanceOf classifies absent, valid, and malformed envelopes.
func TestSyncProvenance_CT8_ProvenanceOfClassification(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want ProvenanceSource
	}{
		{"absent envelope means local", `{"session_id":"s"}`, ProvenanceLocal},
		{"valid remote envelope", `{"session_id":"s","` + provenanceField + `":{"source":"remote"}}`, ProvenanceRemote},
		{"malformed envelope is treated as remote", `{"session_id":"s","` + provenanceField + `":"garbage"}`, ProvenanceRemote},
		{"empty source is treated as remote", `{"session_id":"s","` + provenanceField + `":{}}`, ProvenanceRemote},
		{"unparseable line defaults to local", `not json`, ProvenanceLocal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := provenanceOf([]byte(tc.raw)).Source; got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// PROV-CT9: sameEventContent ignores provenance and key ordering, but still
// detects a real payload difference.
func TestSyncProvenance_CT9_SameEventContent(t *testing.T) {
	plain := []byte(`{"session_id":"s","event_id":"e","created_at":"20260101T000000Z"}`)
	reordered := []byte(`{"created_at":"20260101T000000Z","event_id":"e","session_id":"s"}`)
	taggedLines, _, _ := tagEventLines(plain, ProvenanceInfo{Source: ProvenanceRemote, Peer: "p"})
	tagged := bytes.TrimSpace(taggedLines)
	different := []byte(`{"session_id":"s","event_id":"e","created_at":"20260102T000000Z"}`)

	if !sameEventContent(plain, tagged) {
		t.Error("provenance envelope must not count as a content difference")
	}
	if !sameEventContent(plain, reordered) {
		t.Error("key ordering must not count as a content difference")
	}
	if sameEventContent(plain, different) {
		t.Error("a real payload difference must be detected")
	}
}

// PROV-CT10: dry-run reports rejections without writing anything.
func TestSyncProvenance_CT10_DryRunReportsRejections(t *testing.T) {
	dir := t.TempDir()
	client := newProvTestClient()
	client.PutWithETag("2026-05-10.jsonl", []byte(`{"session_id":"s","event_id":"e"}`+"\n"), "")

	cfg := SyncConfig{
		LocalDir:        dir,
		DryRun:          true,
		Timeout:         5 * time.Second,
		ConflictLogPath: filepath.Join(t.TempDir(), "conflicts.jsonl"),
		ProvenanceFilter: func(ProvenanceInfo) ProvenanceDecision {
			return ProvenanceDecision{Allow: false, Reason: "dry_run_reject"}
		},
	}
	cmd := newSyncCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	result, err := RunSync(cmd, cfg, client)
	if err != nil {
		t.Fatalf("RunSync error: %v", err)
	}
	if result.Rejected != 1 {
		t.Errorf("want Rejected=1 in dry-run, got %d", result.Rejected)
	}
	if !bytes.Contains(out.Bytes(), []byte("0 to download")) {
		t.Errorf("dry-run plan should show 0 downloads, got: %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-05-10.jsonl")); !os.IsNotExist(err) {
		t.Error("dry-run wrote a file")
	}
}
